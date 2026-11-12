package embed

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/datastore"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logger"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// countingEmbedder embeds in process (fakeBackend vectors) and counts texts.
type countingEmbedder struct {
	model string
	texts int
	err   error
}

func (c *countingEmbedder) Info(context.Context) (Info, error) {
	if c.err != nil {
		return Info{}, c.err
	}
	return Info{Model: c.model, Dims: 4}, nil
}

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if c.err != nil {
		return nil, c.err
	}
	c.texts += len(texts)
	vecs, _ := fakeBackend{}.Embed(texts)
	for _, v := range vecs {
		Normalize(v)
	}
	return vecs, nil
}

func openIndexDB(t *testing.T) (store.Store, context.Context) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(dir, "test.db")},
		Storage:  config.StorageConfig{BasePath: filepath.Join(dir, "saved")},
	}
	log, err := logger.NewProduction(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := datastore.MigrateUp(cfg, filepath.Join("..", "..", "migrations"), logging.New("test", log)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db, err := datastore.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, context.Background()
}

func seedPages(t *testing.T, db store.Store, ctx context.Context, key string, pages map[int]string) {
	t.Helper()
	spaceID, err := db.CreateSpace(ctx, key, key, "https://example/spaces/"+key)
	if err != nil {
		t.Fatal(err)
	}
	for id, content := range pages {
		if err := db.UpsertPage(ctx, &store.Page{SpaceID: spaceID, ConfluenceID: id, Title: fmt.Sprintf("Page %d", id), Content: content}); err != nil {
			t.Fatal(err)
		}
	}
}

func longSections(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "## Section %d\n\n%s\n\n", i, strings.Repeat(fmt.Sprintf("sentence %d ", i), 90))
	}
	return b.String()
}

func TestIndexerBackfillIsIncremental(t *testing.T) {
	db, ctx := openIndexDB(t)
	seedPages(t, db, ctx, "AAA", map[int]string{1: longSections(3), 2: "short page"})
	seedPages(t, db, ctx, "BBB", map[int]string{3: "other space"})
	emb := &countingEmbedder{model: "m1"}
	ix := NewIndexer(db, emb, logging.Sugar{})
	changes := 0
	ix.OnChange = func() { changes++ }

	var progress []int
	res, err := ix.Backfill(ctx, "AAA", func(done, total int) { progress = append(progress, done*10+total) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 2 || res.Indexed != 2 || res.Chunks != emb.texts || changes != 2 || len(progress) != 2 {
		t.Fatalf("first run: %+v, texts %d, changes %d, progress %v", res, emb.texts, changes, progress)
	}
	firstTexts := emb.texts

	// Nothing changed: no embedding at all.
	res, err = ix.Backfill(ctx, "AAA", nil)
	if err != nil || res.Pages != 0 || res.UpToDate != 2 || emb.texts != firstTexts {
		t.Fatalf("second run: %+v, texts %d, err %v", res, emb.texts, err)
	}

	// One section edited: only its chunk is embedded again.
	src, err := db.GetEmbedSource(ctx, "AAA", 1)
	if err != nil {
		t.Fatal(err)
	}
	src.Content = strings.Replace(src.Content, "sentence 2 ", "edited 2 ", 1)
	spaceA, _ := db.GetSpaceByKey(ctx, "AAA")
	if err := db.UpsertPage(ctx, &store.Page{SpaceID: spaceA.ID, ConfluenceID: 1, Title: src.Title, Content: src.Content}); err != nil {
		t.Fatal(err)
	}
	res, err = ix.Backfill(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 2 || res.Indexed != 2 || emb.texts != firstTexts+2 {
		// page 1 (one changed chunk) + page 3 (BBB, never embedded, one chunk)
		t.Fatalf("after edit: %+v, embedded %d new texts", res, emb.texts-firstTexts)
	}

	// A new model re-embeds everything.
	emb2 := &countingEmbedder{model: "m2"}
	res, err = NewIndexer(db, emb2, logging.Sugar{}).Backfill(ctx, "", nil)
	if err != nil || res.Pages != 3 || emb2.texts == 0 {
		t.Fatalf("model change: %+v, texts %d, err %v", res, emb2.texts, err)
	}
	st, _ := db.GetEmbeddingStats(ctx, "m2")
	if st.EmbeddedPages != 3 || st.Pages != 3 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestIndexerBackfillStopsWhenUnavailable(t *testing.T) {
	db, ctx := openIndexDB(t)
	seedPages(t, db, ctx, "AAA", map[int]string{1: "a"})
	emb := &countingEmbedder{model: "m1", err: fmt.Errorf("%w: helper missing", ErrUnavailable)}
	if _, err := NewIndexer(db, emb, logging.Sugar{}).Backfill(ctx, "", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
