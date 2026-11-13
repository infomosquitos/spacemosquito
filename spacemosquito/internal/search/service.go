package search

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// Mode selects how Search ranks pages.
type Mode string

const (
	// ModeKeyword is FTS5/BM25 only (title weighted, all terms required).
	ModeKeyword Mode = "keyword"
	// ModeSemantic ranks pages by their best chunk's embedding similarity.
	ModeSemantic Mode = "semantic"
	// ModeHybrid merges both rankings with Reciprocal Rank Fusion.
	ModeHybrid Mode = "hybrid"
)

const (
	// rrfK damps the weight of top ranks in Reciprocal Rank Fusion.
	rrfK = 60
	// candidates is how many pages each side contributes before fusion.
	candidates = 50
)

// ParseMode accepts "", keyword, semantic and hybrid ("" = default).
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case "", ModeKeyword, ModeSemantic, ModeHybrid:
		return m, nil
	}
	return "", fmt.Errorf("unknown search mode %q (use keyword, semantic or hybrid)", s)
}

// Request is one search.
type Request struct {
	Query    string
	SpaceKey string
	Limit    int
	// Mode "" means hybrid when semantic search is available, else keyword.
	Mode Mode
}

// Response holds the ranked pages and the mode actually used: semantic
// and hybrid fall back to keyword when vectors are not available.
type Response struct {
	Results []store.SearchResult
	Mode    Mode
}

// Service runs keyword, semantic and hybrid search (ADR-019).
type Service struct {
	db   store.Store
	emb  embed.Embedder
	vecs *vectorIndex
	log  logging.Sugar

	// Enabled, if set, switches semantic search at runtime; when it
	// returns false every mode runs as keyword search.
	Enabled func() bool
}

// NewService returns a Service; emb nil means keyword search only.
func NewService(db store.Store, emb embed.Embedder, log logging.Sugar) *Service {
	return &Service{db: db, emb: emb, vecs: &vectorIndex{db: db}, log: log}
}

// Invalidate drops cached vectors; the indexer calls it after saving chunks.
func (s *Service) Invalidate() { s.vecs.invalidate() }

// Search ranks pages for req.
func (s *Service) Search(ctx context.Context, req Request) (Response, error) {
	if req.Limit <= 0 {
		req.Limit = 10
	}
	mode := req.Mode
	if mode == "" {
		mode = ModeHybrid
	}
	if s.emb == nil || (s.Enabled != nil && !s.Enabled()) {
		mode = ModeKeyword
	}
	if mode == ModeKeyword {
		res, err := s.db.SearchPages(ctx, req.Query, req.SpaceKey, req.Limit)
		return Response{Results: res, Mode: ModeKeyword}, err
	}

	sem, err := s.semantic(ctx, req.Query, req.SpaceKey, max(candidates, req.Limit))
	if err != nil || len(sem) == 0 {
		if err != nil && s.log.Enabled() {
			s.log.Warnw("semantic search unavailable, using keyword search", "error", err)
		}
		res, kerr := s.db.SearchPages(ctx, req.Query, req.SpaceKey, req.Limit)
		return Response{Results: res, Mode: ModeKeyword}, kerr
	}

	var kw []store.SearchResult
	if mode == ModeHybrid {
		if kw, err = s.db.SearchPages(ctx, req.Query, req.SpaceKey, max(candidates, req.Limit)); err != nil {
			return Response{}, err
		}
	}
	res, err := s.fuse(ctx, kw, sem, req.Limit)
	return Response{Results: res, Mode: mode}, err
}

func (s *Service) semantic(ctx context.Context, query, spaceKey string, k int) ([]pageHit, error) {
	info, err := s.emb.Info(ctx)
	if err != nil {
		return nil, err
	}
	vecs, err := s.emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	return s.vecs.search(ctx, info.Model, vecs[0], spaceKey, k)
}

// fuse merges keyword results and semantic hits with Reciprocal Rank
// Fusion (score = Σ 1/(rrfK + rank)); with no keyword results it is the
// semantic ranking. Pages found by meaning get their best chunk as excerpt.
func (s *Service) fuse(ctx context.Context, kw []store.SearchResult, sem []pageHit, limit int) ([]store.SearchResult, error) {
	type entry struct {
		score    float64
		kw       *store.SearchResult
		sem      *pageHit
		semScore float32
	}
	byPage := map[uuid.UUID]*entry{}
	get := func(id uuid.UUID) *entry {
		e := byPage[id]
		if e == nil {
			e = &entry{}
			byPage[id] = e
		}
		return e
	}
	for i := range kw {
		e := get(kw[i].InternalID)
		e.kw = &kw[i]
		e.score += 1 / float64(rrfK+i+1)
	}
	for i := range sem {
		e := get(sem[i].ref.page)
		e.sem = &sem[i]
		e.semScore = sem[i].score
		e.score += 1 / float64(rrfK+i+1)
	}

	ids := make([]uuid.UUID, 0, len(byPage))
	for id := range byPage {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := byPage[ids[i]], byPage[ids[j]]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.semScore != b.semScore {
			return a.semScore > b.semScore
		}
		return ids[i].String() < ids[j].String()
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}

	var missing []uuid.UUID
	for _, id := range ids {
		if byPage[id].kw == nil {
			missing = append(missing, id)
		}
	}
	rows, err := s.db.SearchResultsByPageIDs(ctx, missing)
	if err != nil {
		return nil, err
	}

	out := make([]store.SearchResult, 0, len(ids))
	for _, id := range ids {
		e := byPage[id]
		var r store.SearchResult
		switch {
		case e.kw != nil:
			r = *e.kw
			r.Match = "keyword"
			if e.sem != nil {
				r.Match = "both"
			}
		default:
			row, ok := rows[id]
			if !ok {
				continue // deleted since the vectors were loaded
			}
			r = row
			r.Match = "semantic"
			text, path, err := s.db.ChunkText(ctx, id, e.sem.ref.chunk)
			if err != nil && !errors.Is(err, context.Canceled) {
				return nil, err
			}
			r.Excerpt, r.HeadingPath = text, path
		}
		r.Similarity = e.score
		out = append(out, r)
	}
	return out, nil
}
