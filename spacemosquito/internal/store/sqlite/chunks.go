package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vkh/spacemosquito/internal/store"
)

// Semantic index storage (ADR-019, migration 014).

func (d *DB) ListEmbedSources(ctx context.Context, spaceKey string) ([]store.EmbedSource, error) {
	q := `SELECT p.id, s.key, p.confluence_id, p.title, COALESCE(p.content, '')
	      FROM pages p JOIN spaces s ON s.id = p.space_id`
	var args []any
	if spaceKey != "" {
		q += ` WHERE s.key = ?`
		args = append(args, spaceKey)
	}
	q += ` ORDER BY s.key, p.confluence_id`
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list embed sources: %w", err)
	}
	defer rows.Close()
	var out []store.EmbedSource
	for rows.Next() {
		src, err := scanEmbedSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (d *DB) GetEmbedSource(ctx context.Context, spaceKey string, confluenceID int) (*store.EmbedSource, error) {
	row := d.sql.QueryRowContext(ctx,
		`SELECT p.id, s.key, p.confluence_id, p.title, COALESCE(p.content, '')
		 FROM pages p JOIN spaces s ON s.id = p.space_id
		 WHERE s.key = ? AND p.confluence_id = ?`, spaceKey, confluenceID)
	src, err := scanEmbedSource(row)
	if err != nil {
		return nil, err
	}
	return &src, nil
}

func scanEmbedSource(r rowScanner) (store.EmbedSource, error) {
	var src store.EmbedSource
	var id string
	if err := r.Scan(&id, &src.SpaceKey, &src.ConfluenceID, &src.Title, &src.Content); err != nil {
		return src, err
	}
	var err error
	src.PageID, err = uuid.Parse(id)
	return src, err
}

func (d *DB) EmbeddedSourceHashes(ctx context.Context, model string) (map[uuid.UUID]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT page_id, source_hash FROM page_embeddings WHERE model = ?`, model)
	if err != nil {
		return nil, fmt.Errorf("list page embeddings: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return nil, err
		}
		pid, err := uuid.Parse(id)
		if err != nil {
			return nil, err
		}
		out[pid] = hash
	}
	return out, rows.Err()
}

func (d *DB) PageChunkVectors(ctx context.Context, pageID uuid.UUID, model string) (map[string][]byte, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT c.content_hash, c.vector FROM page_chunks c JOIN page_embeddings e ON e.page_id = c.page_id
		 WHERE c.page_id = ? AND e.model = ?`, pageID.String(), model)
	if err != nil {
		return nil, fmt.Errorf("list page chunks: %w", err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var hash string
		var vec []byte
		if err := rows.Scan(&hash, &vec); err != nil {
			return nil, err
		}
		out[hash] = vec
	}
	return out, rows.Err()
}

func (d *DB) SavePageChunks(ctx context.Context, pageID uuid.UUID, model, sourceHash string, chunks []store.PageChunk) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	id := pageID.String()
	if _, err := tx.ExecContext(ctx, `DELETE FROM page_chunks WHERE page_id = ?`, id); err != nil {
		return fmt.Errorf("delete page chunks: %w", err)
	}
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO page_chunks (page_id, chunk_index, heading_path, text, content_hash, vector)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			id, c.Index, c.HeadingPath, c.Text, c.ContentHash, c.Vector); err != nil {
			return fmt.Errorf("insert page chunk: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO page_embeddings (page_id, model, source_hash, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (page_id) DO UPDATE SET model=excluded.model, source_hash=excluded.source_hash, updated_at=excluded.updated_at`,
		id, model, sourceHash, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("save page embedding: %w", err)
	}
	return tx.Commit()
}

func (d *DB) ForEachChunkVector(ctx context.Context, model string, fn func(store.ChunkVector) error) error {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT c.page_id, s.key, c.chunk_index, c.heading_path, c.vector
		 FROM page_chunks c
		 JOIN page_embeddings e ON e.page_id = c.page_id
		 JOIN pages p ON p.id = c.page_id
		 JOIN spaces s ON s.id = p.space_id
		 WHERE e.model = ?
		 ORDER BY c.page_id, c.chunk_index`, model)
	if err != nil {
		return fmt.Errorf("list chunk vectors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cv store.ChunkVector
		var id string
		if err := rows.Scan(&id, &cv.SpaceKey, &cv.ChunkIndex, &cv.HeadingPath, &cv.Vector); err != nil {
			return err
		}
		if cv.PageID, err = uuid.Parse(id); err != nil {
			return err
		}
		if err := fn(cv); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *DB) ChunkText(ctx context.Context, pageID uuid.UUID, chunkIndex int) (string, string, error) {
	var text, path string
	err := d.sql.QueryRowContext(ctx,
		`SELECT text, heading_path FROM page_chunks WHERE page_id = ? AND chunk_index = ?`,
		pageID.String(), chunkIndex).Scan(&text, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return text, path, err
}

func (d *DB) GetEmbeddingStats(ctx context.Context, model string) (*store.EmbeddingStats, error) {
	var st store.EmbeddingStats
	err := d.sql.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM pages),
		       (SELECT COUNT(*) FROM page_embeddings WHERE model = ?),
		       (SELECT COUNT(*) FROM page_chunks c JOIN page_embeddings e ON e.page_id = c.page_id WHERE e.model = ?)`,
		model, model).Scan(&st.Pages, &st.EmbeddedPages, &st.Chunks)
	if err != nil {
		return nil, fmt.Errorf("embedding stats: %w", err)
	}
	return &st, nil
}

func (d *DB) SearchResultsByPageIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]store.SearchResult, error) {
	out := map[uuid.UUID]store.SearchResult{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}
	rows, err := d.sql.QueryContext(ctx, `
		SELECT p.confluence_id, s.key, p.title, '', 0.0,
		       CASE WHEN p.file_dir <> '' THEN p.file_dir || '/content.md' ELSE p.html_path END, p.id,
		       p.confluence_updated_by, p.confluence_updated_at
		FROM pages p JOIN spaces s ON s.id = p.space_id
		WHERE p.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("search results by id: %w", err)
	}
	defer rows.Close()
	results, err := scanSearchResults(rows)
	if err != nil {
		return nil, err
	}
	for _, r := range results {
		out[r.InternalID] = r
	}
	return out, nil
}
