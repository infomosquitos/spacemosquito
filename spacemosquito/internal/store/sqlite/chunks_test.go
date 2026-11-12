package sqlite_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/vkh/spacemosquito/internal/store"
)

func TestPageChunks(t *testing.T) {
	db, ctx := openTestDB(t)
	defer db.Close()

	spaceID, err := db.CreateSpace(ctx, "TST", "Test", "https://example/spaces/TST")
	if err != nil {
		t.Fatal(err)
	}
	page := &store.Page{SpaceID: spaceID, ConfluenceID: 42, Title: "Hello", Content: "body"}
	if err := db.UpsertPage(ctx, page); err != nil {
		t.Fatal(err)
	}
	src, err := db.GetEmbedSource(ctx, "TST", 42)
	if err != nil || src.Title != "Hello" || src.Content != "body" {
		t.Fatalf("source = %+v, %v", src, err)
	}
	if all, err := db.ListEmbedSources(ctx, ""); err != nil || len(all) != 1 || all[0].PageID != src.PageID {
		t.Fatalf("sources = %+v, %v", all, err)
	}

	chunks := []store.PageChunk{
		{Index: 0, HeadingPath: "A", Text: "one", ContentHash: "h0", Vector: []byte{1, 0, 0, 0}},
		{Index: 1, HeadingPath: "A > B", Text: "two", ContentHash: "h1", Vector: []byte{2, 0, 0, 0}},
	}
	if err := db.SavePageChunks(ctx, src.PageID, "m1", "src1", chunks); err != nil {
		t.Fatal(err)
	}
	hashes, err := db.EmbeddedSourceHashes(ctx, "m1")
	if err != nil || hashes[src.PageID] != "src1" {
		t.Fatalf("hashes = %v, %v", hashes, err)
	}
	if other, _ := db.EmbeddedSourceHashes(ctx, "m2"); len(other) != 0 {
		t.Fatalf("other model sees %v", other)
	}
	vecs, err := db.PageChunkVectors(ctx, src.PageID, "m1")
	if err != nil || len(vecs) != 2 || vecs["h1"][0] != 2 {
		t.Fatalf("vectors = %v, %v", vecs, err)
	}
	if other, _ := db.PageChunkVectors(ctx, src.PageID, "m2"); len(other) != 0 {
		t.Fatalf("vectors of another model reused: %v", other)
	}
	var seen []store.ChunkVector
	if err := db.ForEachChunkVector(ctx, "m1", func(cv store.ChunkVector) error {
		seen = append(seen, cv)
		return nil
	}); err != nil || len(seen) != 2 || seen[1].HeadingPath != "A > B" || seen[0].SpaceKey != "TST" {
		t.Fatalf("streamed = %+v, %v", seen, err)
	}
	if text, path, err := db.ChunkText(ctx, src.PageID, 1); err != nil || text != "two" || path != "A > B" {
		t.Fatalf("chunk text = %q %q %v", text, path, err)
	}
	st, err := db.GetEmbeddingStats(ctx, "m1")
	if err != nil || st.Pages != 1 || st.EmbeddedPages != 1 || st.Chunks != 2 {
		t.Fatalf("stats = %+v, %v", st, err)
	}
	res, err := db.SearchResultsByPageIDs(ctx, []uuid.UUID{src.PageID, uuid.New()})
	if r, ok := res[src.PageID]; err != nil || len(res) != 1 || !ok || r.Title != "Hello" || r.SpaceKey != "TST" || r.ConfluenceID != 42 {
		t.Fatalf("results by id = %+v, %v", res, err)
	}

	// Replacing keeps one set; re-saving the page keeps the chunks.
	if err := db.SavePageChunks(ctx, src.PageID, "m1", "src2", chunks[:1]); err != nil {
		t.Fatal(err)
	}
	page.Content = "changed"
	if err := db.UpsertPage(ctx, page); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.GetEmbeddingStats(ctx, "m1"); st.Chunks != 1 || st.EmbeddedPages != 1 {
		t.Fatalf("after replace/upsert: %+v", st)
	}

	// Deleting the space removes chunks and embedding rows (cascade).
	if err := db.DeleteSpace(ctx, "TST"); err != nil {
		t.Fatal(err)
	}
	if st, _ := db.GetEmbeddingStats(ctx, "m1"); st.Chunks != 0 || st.EmbeddedPages != 0 || st.Pages != 0 {
		t.Fatalf("after delete: %+v", st)
	}
}
