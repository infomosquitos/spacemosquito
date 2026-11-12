-- Semantic search (ADR-019): embedded chunks of each page's Markdown.
-- page_embeddings records which model embedded a page and a hash of the
-- title + content it was built from, so changed or stale pages are found.
CREATE TABLE page_embeddings (
    page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE page_chunks (
    page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    heading_path TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    vector BLOB NOT NULL, -- little-endian float32, L2-normalized
    PRIMARY KEY (page_id, chunk_index)
);
