package search

import (
	"time"

	"github.com/google/uuid"
	"github.com/vkh/spacemosquito/internal/store"
)

// SearchHit is the public search result shape (MCP, REST, CLI).
type SearchHit struct {
	ConfluenceID int     `json:"confluence_id"`
	SpaceKey     string  `json:"space_key"`
	Title        string  `json:"title"`
	Excerpt      string  `json:"excerpt"`
	Similarity   float64 `json:"similarity"`
	FilePath     string  `json:"file_path,omitempty"`
	InternalID   string  `json:"internal_id,omitempty"`
	// Match tells how semantic/hybrid search found the page: "keyword",
	// "semantic" or "both".
	Match string `json:"match,omitempty"`
	// HeadingPath is the section the excerpt comes from, when the excerpt
	// is the page's best-matching chunk.
	HeadingPath string `json:"heading_path,omitempty"`
	// BrowseURL is the page's path in the served catalog (REST only).
	BrowseURL string `json:"browse_url,omitempty"`
	// History holds the last Confluence update (updated_by, updated_at) when
	// known, so callers can judge how current a page is.
	History *store.PageHistory `json:"history,omitempty"`
}

// PageDetail is the public full-page shape for MCP get_page.
type PageDetail struct {
	ConfluenceID int    `json:"confluence_id"`
	SpaceKey     string `json:"space_key"`
	Title        string `json:"title"`
	Version      int    `json:"version"`
	Content      string `json:"content"`
	// UpdatedAt is when the page was last saved locally; History holds who
	// created and last updated it in Confluence, and when.
	UpdatedAt time.Time          `json:"updated_at"`
	History   *store.PageHistory `json:"history,omitempty"`
	// BodyFormat is the crawl source format: "storage" (API) or "rendered" (browser HTML).
	BodyFormat string `json:"body_format,omitempty"`
	InternalID string `json:"internal_id,omitempty"`
}

// ToSearchHits maps DB rows to API-facing search hits.
func ToSearchHits(results []store.SearchResult, exposeInternalIDs bool) []SearchHit {
	if len(results) == 0 {
		return []SearchHit{}
	}
	hits := make([]SearchHit, len(results))
	for i, r := range results {
		hits[i] = SearchHit{
			ConfluenceID: r.ConfluenceID,
			SpaceKey:     r.SpaceKey,
			Title:        r.Title,
			Excerpt:      NormalizeExcerpt(r.Excerpt, DefaultExcerptMaxRunes),
			Similarity:   r.Similarity,
			FilePath:     r.FilePath,
			Match:        r.Match,
			HeadingPath:  r.HeadingPath,
		}
		if r.UpdatedBy != "" || r.UpdatedAt != nil {
			hits[i].History = &store.PageHistory{UpdatedBy: r.UpdatedBy, UpdatedAt: r.UpdatedAt}
		}
		if exposeInternalIDs && r.InternalID != uuid.Nil {
			hits[i].InternalID = r.InternalID.String()
		}
	}
	return hits
}

// ToPageDetail maps a DB page to the MCP/REST page detail shape.
func ToPageDetail(page *store.Page, spaceKey string, exposeInternalIDs bool) PageDetail {
	detail := PageDetail{
		ConfluenceID: page.ConfluenceID,
		SpaceKey:     spaceKey,
		Title:        page.Title,
		Version:      page.Version,
		Content:      page.Content,
		UpdatedAt:    page.UpdatedAt,
	}
	if !page.History.IsZero() {
		h := page.History
		detail.History = &h
	}
	if exposeInternalIDs && page.ID != uuid.Nil {
		detail.InternalID = page.ID.String()
	}
	return detail
}
