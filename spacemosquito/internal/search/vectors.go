package search

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/store"
)

// vectorIndex holds every chunk vector of one model in memory (ADR-019:
// ~5k chunks × 1024 dims ≈ 20 MB), loaded on first use and reloaded after
// the indexer changes chunks.
type vectorIndex struct {
	db store.Store

	mu        sync.Mutex
	gen       uint64 // bumped by invalidate
	loadedGen uint64
	model     string
	dims      int
	data      []float32 // len(refs) × dims, row-major
	refs      []chunkRef
}

type chunkRef struct {
	page     uuid.UUID
	space    string
	chunk    int
	headings string
}

// pageHit is a page's best chunk for a query.
type pageHit struct {
	ref   chunkRef
	score float32
}

func (v *vectorIndex) invalidate() {
	v.mu.Lock()
	v.gen++
	v.mu.Unlock()
}

// load (re)reads the vectors of model when stale.
func (v *vectorIndex) load(ctx context.Context, model string) error {
	if v.model == model && v.loadedGen == v.gen && v.refs != nil {
		return nil
	}
	gen := v.gen
	var data []float32
	var refs []chunkRef
	dims := 0
	err := v.db.ForEachChunkVector(ctx, model, func(cv store.ChunkVector) error {
		vec, err := embed.DecodeVector(cv.Vector)
		if err != nil {
			return err
		}
		if dims == 0 {
			dims = len(vec)
		}
		if len(vec) != dims {
			return fmt.Errorf("chunk %s/%d has %d dims, expected %d", cv.PageID, cv.ChunkIndex, len(vec), dims)
		}
		data = append(data, vec...)
		refs = append(refs, chunkRef{page: cv.PageID, space: cv.SpaceKey, chunk: cv.ChunkIndex, headings: cv.HeadingPath})
		return nil
	})
	if err != nil {
		return err
	}
	if refs == nil {
		refs = []chunkRef{}
	}
	v.model, v.dims, v.data, v.refs, v.loadedGen = model, dims, data, refs, gen
	return nil
}

// search returns up to k pages ranked by their best chunk's similarity to q
// (a normalized vector), optionally limited to one space.
func (v *vectorIndex) search(ctx context.Context, model string, q []float32, spaceKey string, k int) ([]pageHit, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.load(ctx, model); err != nil {
		return nil, err
	}
	if len(v.refs) == 0 {
		return nil, nil
	}
	if len(q) != v.dims {
		return nil, fmt.Errorf("query has %d dims, index has %d", len(q), v.dims)
	}
	best := map[uuid.UUID]pageHit{}
	for i, ref := range v.refs {
		if spaceKey != "" && ref.space != spaceKey {
			continue
		}
		row := v.data[i*v.dims : (i+1)*v.dims]
		var s float32
		for j, x := range row {
			s += x * q[j]
		}
		if cur, ok := best[ref.page]; !ok || s > cur.score {
			best[ref.page] = pageHit{ref: ref, score: s}
		}
	}
	hits := make([]pageHit, 0, len(best))
	for _, h := range best {
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].ref.page.String() < hits[j].ref.page.String()
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// size reports the number of loaded chunks (0 before the first load).
func (v *vectorIndex) size() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.refs)
}
