package api

import (
	"net/http"
	"strconv"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/search"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

type SearchHandler struct {
	db    store.Store
	store *session.Store
	cfg   *config.Config
	log   logging.Sugar
	// Search runs keyword/semantic/hybrid search; nil = keyword only.
	Service *search.Service
	// Semantic reports semantic index coverage in Stats; nil = off.
	Semantic *embed.Semantic
}

func NewSearchHandler(database store.Store, store *session.Store, cfg *config.Config, log logging.Sugar) *SearchHandler {
	return &SearchHandler{
		db:    database,
		store: store,
		cfg:   cfg,
		log:   log,
	}
}

func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' is required"})
		return
	}

	spaceKey := r.URL.Query().Get("space_key")
	limitStr := r.URL.Query().Get("limit")
	limit := 10
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	mode, err := search.ParseMode(r.URL.Query().Get("mode"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var results []store.SearchResult
	modeUsed := search.ModeKeyword
	if h.Service != nil {
		var res search.Response
		res, err = h.Service.Search(r.Context(), search.Request{Query: query, SpaceKey: spaceKey, Limit: limit, Mode: mode})
		results, modeUsed = res.Results, res.Mode
	} else {
		results, err = h.db.SearchPages(r.Context(), query, spaceKey, limit)
	}
	if err != nil {
		h.log.Errorw("search failed", "query", query, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search failed: " + err.Error()})
		return
	}

	if results == nil {
		results = []store.SearchResult{}
	}

	hits := search.ToSearchHits(results, h.cfg.MCP.ExposeInternalIDs)
	// highlight=1 keeps search.MatchStart/MatchEnd around matched terms in
	// excerpts (used by the browse UI).
	highlight := r.URL.Query().Get("highlight") == "1"
	for i := range hits {
		if highlight {
			hits[i].Excerpt = search.HighlightedExcerpt(results[i].Excerpt, search.DefaultExcerptMaxRunes)
		}
		hits[i].BrowseURL = PageBrowsePath(h.cfg.Storage.BasePath, hits[i].SpaceKey, hits[i].FilePath)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"query":   query,
		"mode":    modeUsed,
		"count":   len(hits),
		"results": hits,
	})
}

func (h *SearchHandler) Reindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	confluenceIDStr := r.URL.Query().Get("confluence_id")
	if confluenceIDStr != "" {
		confluenceID, err := strconv.Atoi(confluenceIDStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid confluence_id"})
			return
		}
		spaceKey := r.URL.Query().Get("space_key")
		if spaceKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "space_key is required with confluence_id"})
			return
		}
		if err := h.db.IndexPageContent(r.Context(), spaceKey, confluenceID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"message": "page reindexed"})
		return
	}

	if err := h.db.IndexAllPageContents(r.Context()); err != nil {
		h.log.Errorw("reindex all failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reindex failed: " + err.Error()})
		return
	}

	h.log.Info("all pages reindexed")
	writeJSON(w, http.StatusOK, map[string]string{"message": "all pages reindexed"})
}

func (h *SearchHandler) Stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.db.GetPageStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*store.PageStats
		Semantic embed.Report `json:"semantic"`
	}{stats, h.Semantic.Report(r.Context(), h.db)})
}
