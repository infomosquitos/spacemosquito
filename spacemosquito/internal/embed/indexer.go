package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// embedBatch is how many chunks go to the helper per request.
const embedBatch = 16

// Indexer keeps a page's chunks and vectors in step with its content.
type Indexer struct {
	db  store.Store
	emb Embedder
	log logging.Sugar
	// OnChange runs after a page's chunks were saved (search drops its
	// cached vectors).
	OnChange func()
}

// NewIndexer returns an Indexer writing to db with emb.
func NewIndexer(db store.Store, emb Embedder, log logging.Sugar) *Indexer {
	return &Indexer{db: db, emb: emb, log: log}
}

// SourceHash identifies what a page's chunks were built from.
func SourceHash(model, title, content string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + title + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// IndexPage chunks the page and embeds the chunks whose text changed
// (unchanged ones keep their vectors). It returns how many were embedded.
func (ix *Indexer) IndexPage(ctx context.Context, src store.EmbedSource) (int, error) {
	info, err := ix.emb.Info(ctx)
	if err != nil {
		return 0, err
	}
	chunks := ChunkPage(src.Title, src.Content)
	old, err := ix.db.PageChunkVectors(ctx, src.PageID, info.Model)
	if err != nil {
		return 0, err
	}

	out := make([]store.PageChunk, len(chunks))
	var todo []int
	for i, c := range chunks {
		out[i] = store.PageChunk{Index: c.Index, HeadingPath: c.HeadingPath, Text: c.Text, ContentHash: c.Hash}
		if v, ok := old[c.Hash]; ok {
			out[i].Vector = v
		} else {
			todo = append(todo, i)
		}
	}
	for start := 0; start < len(todo); start += embedBatch {
		batch := todo[start:min(start+embedBatch, len(todo))]
		texts := make([]string, len(batch))
		for j, i := range batch {
			texts[j] = chunks[i].Embed
		}
		vecs, err := ix.emb.Embed(ctx, texts)
		if err != nil {
			return 0, err
		}
		for j, i := range batch {
			if len(vecs[j]) != info.Dims {
				return 0, fmt.Errorf("vector has %d dims, model has %d", len(vecs[j]), info.Dims)
			}
			out[i].Vector = EncodeVector(vecs[j])
		}
	}

	if err := ix.db.SavePageChunks(ctx, src.PageID, info.Model, SourceHash(info.Model, src.Title, src.Content), out); err != nil {
		return 0, err
	}
	if ix.OnChange != nil {
		ix.OnChange()
	}
	return len(todo), nil
}

// BackfillResult summarizes a Backfill run.
type BackfillResult struct {
	Pages    int // pages that needed (re-)embedding
	Indexed  int
	Failed   int
	Chunks   int // chunks embedded
	UpToDate int
}

// Backfill indexes every page (of spaceKey, when set) whose chunks are
// missing, from another model, or built from older content. progress, if
// set, is called after each page. It stops early with ErrUnavailable when
// the embedder cannot run; other per-page errors are logged and skipped.
func (ix *Indexer) Backfill(ctx context.Context, spaceKey string, progress func(done, total int)) (BackfillResult, error) {
	var res BackfillResult
	info, err := ix.emb.Info(ctx)
	if err != nil {
		return res, err
	}
	sources, err := ix.db.ListEmbedSources(ctx, spaceKey)
	if err != nil {
		return res, err
	}
	done, err := ix.db.EmbeddedSourceHashes(ctx, info.Model)
	if err != nil {
		return res, err
	}
	var stale []store.EmbedSource
	for _, src := range sources {
		if done[src.PageID] == SourceHash(info.Model, src.Title, src.Content) {
			res.UpToDate++
			continue
		}
		stale = append(stale, src)
	}
	res.Pages = len(stale)

	for i, src := range stale {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		n, err := ix.IndexPage(ctx, src)
		switch {
		case errors.Is(err, ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return res, err
		case err != nil:
			res.Failed++
			if ix.log.Enabled() {
				ix.log.Warnw("embedding page failed", "space", src.SpaceKey, "page_id", src.ConfluenceID, "title", src.Title, "error", err)
			}
		default:
			res.Indexed++
			res.Chunks += n
		}
		if progress != nil {
			progress(i+1, len(stale))
		}
	}
	return res, nil
}
