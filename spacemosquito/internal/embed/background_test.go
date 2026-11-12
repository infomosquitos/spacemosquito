package embed

import (
	"context"
	"testing"
	"time"

	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

func TestBackgroundIndexesAtStartAndOnNotify(t *testing.T) {
	db, ctx := openIndexDB(t)
	seedPages(t, db, ctx, "AAA", map[int]string{1: "first page"})

	oldDebounce, oldPoll := backgroundDebounce, backgroundPoll
	backgroundDebounce, backgroundPoll = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { backgroundDebounce, backgroundPoll = oldDebounce, oldPoll })

	bg := NewBackground(NewIndexer(db, &countingEmbedder{model: "m1"}, logging.Sugar{}), logging.Sugar{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go bg.Run(runCtx)

	waitEmbedded := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st, err := db.GetEmbeddingStats(ctx, "m1")
			if err == nil && st.EmbeddedPages == want && !bg.Status().Running {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("embedded pages = %+v, want %d (status %+v)", st, want, bg.Status())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitEmbedded(1)

	space, _ := db.GetSpaceByKey(ctx, "AAA")
	if err := db.UpsertPage(ctx, &store.Page{SpaceID: space.ID, ConfluenceID: 2, Title: "Second", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	bg.Notify()
	waitEmbedded(2)
	if st := bg.Status(); st.LastError != "" || st.Total != 1 || st.Done != 1 {
		t.Fatalf("status = %+v", st)
	}
}
