package search

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/datastore"
	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logger"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// wordEmbedder: one dimension per vocabulary word, so "meaning" is word
// overlap independent of FTS's all-terms rule.
type wordEmbedder struct{ err error }

var vocab = []string{"price", "sap", "lunch", "hiking", "export", "receiver"}

func (w wordEmbedder) Info(context.Context) (embed.Info, error) {
	if w.err != nil {
		return embed.Info{}, w.err
	}
	return embed.Info{Model: "words", Dims: len(vocab)}, nil
}

func (w wordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if w.err != nil {
		return nil, w.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(vocab))
		lower := strings.ToLower(t)
		for j, word := range vocab {
			v[j] = float32(strings.Count(lower, word))
		}
		out[i] = embed.Normalize(v)
	}
	return out, nil
}

func setupService(t *testing.T) (*Service, store.Store, context.Context) {
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
		t.Fatal(err)
	}
	db, err := datastore.Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	ctx := context.Background()

	pages := []struct {
		space, title, content string
		id                    int
	}{
		{"ERP", "ZCONDA -> PriceRow Import", "## Mapping\n\nCondition records from SAP become price rows; SAP price conditions are imported nightly.", 1},
		{"ERP", "CCP-761 Price list export", "## Export\n\nThe price list export runs weekly.", 2},
		{"TEAM", "Team offsite", "Lunch at noon, hiking afterwards.", 3},
		{"TEAM", "Receiver setup", "The receiver validates messages.", 4},
	}
	for _, p := range pages {
		spaceID, err := db.CreateSpace(ctx, p.space, p.space, "https://example/spaces/"+p.space)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertPage(ctx, &store.Page{SpaceID: spaceID, ConfluenceID: p.id, Title: p.title, Content: p.content}); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(db, wordEmbedder{}, logging.Sugar{})
	ix := embed.NewIndexer(db, wordEmbedder{}, logging.Sugar{})
	ix.OnChange = svc.Invalidate
	if _, err := ix.Backfill(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	return svc, db, ctx
}

func titles(rs []store.SearchResult) string {
	var t []string
	for _, r := range rs {
		t = append(t, fmt.Sprintf("%s[%s]", r.Title, r.Match))
	}
	return strings.Join(t, ", ")
}

func TestHybridFindsByMeaning(t *testing.T) {
	svc, _, ctx := setupService(t)
	q := "how do we handle price updates from SAP"

	kw, err := svc.Search(ctx, Request{Query: q, Mode: ModeKeyword})
	if err != nil || len(kw.Results) != 0 || kw.Mode != ModeKeyword {
		t.Fatalf("keyword: %s (%v)", titles(kw.Results), err)
	}

	res, err := svc.Search(ctx, Request{Query: q, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeHybrid || len(res.Results) == 0 {
		t.Fatalf("hybrid: mode %s, %s", res.Mode, titles(res.Results))
	}
	top := res.Results[0]
	if top.Title != "ZCONDA -> PriceRow Import" || top.Match != "semantic" {
		t.Fatalf("top = %s", titles(res.Results))
	}
	if top.HeadingPath != "Mapping" || !strings.Contains(top.Excerpt, "Condition records") || top.SpaceKey != "ERP" || top.ConfluenceID != 1 {
		t.Fatalf("excerpt/heading not from chunk: %+v", top)
	}
}

func TestHybridKeepsExactMatchesOnTop(t *testing.T) {
	svc, _, ctx := setupService(t)
	res, err := svc.Search(ctx, Request{Query: "CCP-761"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) == 0 || res.Results[0].ConfluenceID != 2 || res.Results[0].Match != "both" {
		t.Fatalf("results = %s", titles(res.Results))
	}
}

func TestSemanticSpaceFilter(t *testing.T) {
	svc, _, ctx := setupService(t)
	res, err := svc.Search(ctx, Request{Query: "price", SpaceKey: "TEAM", Mode: ModeSemantic})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Results {
		if r.SpaceKey != "TEAM" {
			t.Fatalf("space filter ignored: %s", titles(res.Results))
		}
	}
}

func TestSearchFallsBackToKeyword(t *testing.T) {
	_, db, ctx := setupService(t)
	for name, svc := range map[string]*Service{
		"no embedder": NewService(db, nil, logging.Sugar{}),
		"unavailable": NewService(db, wordEmbedder{err: fmt.Errorf("%w: down", embed.ErrUnavailable)}, logging.Sugar{}),
	} {
		res, err := svc.Search(ctx, Request{Query: "receiver"})
		if err != nil || res.Mode != ModeKeyword || len(res.Results) != 1 || res.Results[0].ConfluenceID != 4 {
			t.Errorf("%s: mode %s, %s, %v", name, res.Mode, titles(res.Results), err)
		}
	}
}

func TestVectorsReloadAfterIndexing(t *testing.T) {
	svc, db, ctx := setupService(t)
	if _, err := svc.Search(ctx, Request{Query: "hiking", Mode: ModeSemantic}); err != nil {
		t.Fatal(err)
	}
	before := svc.vecs.size()

	space, _ := db.GetSpaceByKey(ctx, "TEAM")
	if err := db.UpsertPage(ctx, &store.Page{SpaceID: space.ID, ConfluenceID: 5, Title: "Hiking trails", Content: "hiking hiking"}); err != nil {
		t.Fatal(err)
	}
	ix := embed.NewIndexer(db, wordEmbedder{}, logging.Sugar{})
	ix.OnChange = svc.Invalidate
	if _, err := ix.Backfill(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Search(ctx, Request{Query: "hiking", Mode: ModeSemantic, Limit: 1})
	if err != nil || len(res.Results) != 1 || res.Results[0].ConfluenceID != 5 {
		t.Fatalf("new page not found: %s, %v", titles(res.Results), err)
	}
	if svc.vecs.size() <= before {
		t.Fatalf("vectors not reloaded: %d -> %d", before, svc.vecs.size())
	}
}

func TestParseMode(t *testing.T) {
	for _, ok := range []string{"", "keyword", "semantic", "hybrid"} {
		if _, err := ParseMode(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, err := ParseMode("fuzzy"); err == nil {
		t.Error("fuzzy accepted")
	}
}

func TestEvaluate(t *testing.T) {
	svc, _, ctx := setupService(t)
	cases := []EvalCase{
		{Question: "how do we handle price updates from SAP", Expect: []int{1}},
		{Question: "CCP-761", Expect: []int{2}},
		{Question: "where do we go hiking", Expect: []int{3}},
	}
	scores, err := svc.Evaluate(ctx, cases, []Mode{ModeKeyword, ModeHybrid}, 5)
	if err != nil {
		t.Fatal(err)
	}
	kw, hy := scores[0], scores[1]
	if kw.Recall >= hy.Recall || hy.Recall != 1 || hy.MRR != 1 {
		t.Fatalf("keyword %+v, hybrid %+v", kw, hy)
	}
	if len(kw.Missed) != 2 {
		t.Fatalf("keyword missed %v", kw.Missed)
	}
}
