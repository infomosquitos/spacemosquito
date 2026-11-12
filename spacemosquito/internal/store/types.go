package store

import (
	"time"

	"github.com/google/uuid"
)

type Space struct {
	ID          uuid.UUID  `db:"id"`
	Key         string     `db:"key"`
	Name        string     `db:"name"`
	URL         string     `db:"url"`
	LastCrawled *time.Time `db:"last_crawled"`
	PagesTotal  int        `db:"pages_total"` // last discovery total (idle crawled/total)
	CreatedAt   time.Time  `db:"created_at"`
}

type Page struct {
	ID                 uuid.UUID `db:"id"`
	SpaceID            uuid.UUID `db:"space_id"`
	ConfluenceID       int       `db:"confluence_id"`
	Version            int       `db:"version"`
	Title              string    `db:"title"`
	ParentConfluenceID *int      `db:"parent_confluence_id"`
	Content            string    `db:"content"`
	HTMLPath           string    `db:"html_path"`
	RawHTMLPath        string    `db:"raw_html_path"`
	MetadataPath       string    `db:"metadata_path"`
	FileDir            string    `db:"file_dir"`
	// BodyFormat is "storage" (CSF/API) or "rendered" (browser HTML). Empty until backfill/save.
	BodyFormat string `db:"body_format"`
	// History is the page's Confluence authorship; CreatedAt/UpdatedAt above
	// are the local save times.
	History   PageHistory
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// BodyFormatCounts is the CSF vs HTML split for pages in a space.
type BodyFormatCounts struct {
	Storage  int // body_format = storage (CSF)
	Rendered int // everything else (HTML / unknown)
}

// PageSummary is a lightweight page row for list APIs (no content or paths).
type PageSummary struct {
	ID                 uuid.UUID `db:"id"`
	ConfluenceID       int       `db:"confluence_id"`
	Version            int       `db:"version"`
	Title              string    `db:"title"`
	ParentConfluenceID *int      `db:"parent_confluence_id"`
	CreatedAt          time.Time `db:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"`
}

// Folder is a Confluence Cloud folder: a node of the page tree without
// content. Pages inside it have it as ParentConfluenceID.
type Folder struct {
	SpaceID            uuid.UUID
	ConfluenceID       int
	Title              string
	ParentConfluenceID *int
}

// PageHistory is who created and last updated a page in Confluence, and
// when. Empty names and nil times mean unknown (e.g. browser-fallback pages).
type PageHistory struct {
	CreatedBy string     `db:"confluence_created_by" json:"created_by,omitempty"`
	CreatedAt *time.Time `db:"confluence_created_at" json:"created_at,omitempty"`
	UpdatedBy string     `db:"confluence_updated_by" json:"updated_by,omitempty"`
	UpdatedAt *time.Time `db:"confluence_updated_at" json:"updated_at,omitempty"`
}

// IsZero reports whether nothing is known.
func (h PageHistory) IsZero() bool {
	return h.CreatedBy == "" && h.CreatedAt == nil && h.UpdatedBy == "" && h.UpdatedAt == nil
}

type SearchResult struct {
	ConfluenceID int       `db:"confluence_id"`
	SpaceKey     string    `db:"space_key"`
	Title        string    `db:"title"`
	Excerpt      string    `db:"excerpt"`
	Similarity   float64   `db:"similarity"`
	FilePath     string    `db:"file_path"`
	InternalID   uuid.UUID `db:"internal_id"`
	// UpdatedBy/UpdatedAt: last Confluence update (PageHistory), if known.
	UpdatedBy string
	UpdatedAt *time.Time
}

type PageStats struct {
	TotalPages     int       `db:"total_pages"`
	TotalSpaces    int       `db:"total_spaces"`
	LastCrawled    time.Time `db:"last_crawled"`
	LastCrawledStr string    `db:"-"`
	ContentLang    string    `db:"content_lang"`
}

// EmbedSource is a page's text for semantic indexing.
type EmbedSource struct {
	PageID       uuid.UUID
	SpaceKey     string
	ConfluenceID int
	Title        string
	Content      string
}

// PageChunk is an embedded piece of a page (ADR-019).
type PageChunk struct {
	Index       int
	HeadingPath string
	Text        string
	ContentHash string
	Vector      []byte // little-endian float32
}

// ChunkVector is one stored vector with what search needs to place it.
type ChunkVector struct {
	PageID      uuid.UUID
	ChunkIndex  int
	HeadingPath string
	Vector      []byte
}

// EmbeddingStats reports semantic index coverage for a model.
type EmbeddingStats struct {
	Pages         int // all saved pages
	EmbeddedPages int // pages embedded with the model
	Chunks        int
}
