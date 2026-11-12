package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Store is the database abstraction used by API, MCP, scraper, and cron.
type Store interface {
	CreateSpace(ctx context.Context, key, name, url string) (uuid.UUID, error)
	GetSpaceByKey(ctx context.Context, key string) (*Space, error)
	ListSpaces(ctx context.Context) ([]Space, error)
	UpdateSpaceLastCrawled(ctx context.Context, spaceKey string) error
	// UpdateSpacePagesTotal stores the discovery page total for idle crawled/total UI.
	// Does not touch last_crawled (call UpdateSpaceLastCrawled when a crawl finishes).
	UpdateSpacePagesTotal(ctx context.Context, spaceKey string, pagesTotal int) error
	CountPagesBySpaceID(ctx context.Context, spaceID uuid.UUID) (int, error)
	// CountPagesByBodyFormat returns CSF (storage) vs HTML (rendered/unknown) counts for a space.
	CountPagesByBodyFormat(ctx context.Context, spaceID uuid.UUID) (BodyFormatCounts, error)
	UpdatePageBodyFormat(ctx context.Context, pageID uuid.UUID, bodyFormat string) error
	DeleteSpace(ctx context.Context, spaceKey string) error

	UpsertPage(ctx context.Context, page *Page) error
	// UpsertFolder saves a Confluence folder of a space's page tree.
	UpsertFolder(ctx context.Context, folder *Folder) error
	// ListFolders returns a space's folders.
	ListFolders(ctx context.Context, spaceKey string) ([]Folder, error)
	// SaveUserNames stores display names by Confluence account ID.
	SaveUserNames(ctx context.Context, names map[string]string) error
	// UserNames returns all stored display names by account ID.
	UserNames(ctx context.Context) (map[string]string, error)
	// UpdatePageHistory sets a saved page's Confluence history; empty
	// fields keep the stored values. No error when the page is not saved.
	UpdatePageHistory(ctx context.Context, spaceKey string, confluenceID int, h PageHistory) error
	GetPage(ctx context.Context, spaceKey string, confluenceID int) (*Page, error)
	GetPageByConfluenceID(ctx context.Context, confluenceID int, spaceKey string) (*Page, string, error)
	ListPages(ctx context.Context, spaceKey string, limit int, afterConfluenceID *int) ([]Page, error)
	ListAllPages(ctx context.Context) ([]Page, error)
	ListPageSummaries(ctx context.Context, spaceKey string, limit int, afterConfluenceID *int) ([]PageSummary, error)
	DeleteStalePages(ctx context.Context, spaceID uuid.UUID, crawlStart time.Time) (int64, error)

	SearchPages(ctx context.Context, query, spaceKey string, limit int) ([]SearchResult, error)
	IndexPageContent(ctx context.Context, spaceKey string, confluenceID int) error
	IndexAllPageContents(ctx context.Context) error
	GetPageStats(ctx context.Context) (*PageStats, error)
	// SearchResultsByPageIDs returns search result rows (no excerpt or
	// score) for pages found by semantic search.
	SearchResultsByPageIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]SearchResult, error)

	// Semantic index (ADR-019).
	// ListEmbedSources returns every saved page's title and content, or
	// one space's when spaceKey is set.
	ListEmbedSources(ctx context.Context, spaceKey string) ([]EmbedSource, error)
	// GetEmbedSource returns one page's title and content.
	GetEmbedSource(ctx context.Context, spaceKey string, confluenceID int) (*EmbedSource, error)
	// EmbeddedSourceHashes returns, per page embedded with model, the hash
	// of the title and content its chunks were built from.
	EmbeddedSourceHashes(ctx context.Context, model string) (map[uuid.UUID]string, error)
	// PageChunkVectors returns a page's stored vectors by chunk content hash.
	PageChunkVectors(ctx context.Context, pageID uuid.UUID) (map[string][]byte, error)
	// SavePageChunks replaces a page's chunks in one transaction.
	SavePageChunks(ctx context.Context, pageID uuid.UUID, model, sourceHash string, chunks []PageChunk) error
	// ForEachChunkVector streams every vector of model.
	ForEachChunkVector(ctx context.Context, model string, fn func(ChunkVector) error) error
	// ChunkText returns one chunk's Markdown and heading path.
	ChunkText(ctx context.Context, pageID uuid.UUID, chunkIndex int) (text, headingPath string, err error)
	GetEmbeddingStats(ctx context.Context, model string) (*EmbeddingStats, error)

	Close()
}
