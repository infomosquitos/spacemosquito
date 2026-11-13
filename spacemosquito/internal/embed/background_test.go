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

// Switched off, the runner indexes nothing (even on Notify); switched back on
// it catches up.
func TestBackgroundPausedSkipsUntilResumed(t *testing.T) {
	db, ctx := openIndexDB(t)
	seedPages(t, db, ctx, "AAA", map[int]string{1: "first page"})

	oldDebounce, oldPoll := backgroundDebounce, backgroundPoll
	backgroundDebounce, backgroundPoll = 10*time.Millisecond, time.Hour
	t.Cleanup(func() { backgroundDebounce, backgroundPoll = oldDebounce, oldPoll })

	emb := &countingEmbedder{model: "m1"}
	bg := NewBackground(NewIndexer(db, emb, logging.Sugar{}), logging.Sugar{})
	bg.SetPaused(true)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go bg.Run(runCtx)

	bg.Notify()
	time.Sleep(200 * time.Millisecond)
	if st, _ := db.GetEmbeddingStats(ctx, "m1"); st != nil && st.EmbeddedPages != 0 {
		t.Fatalf("paused runner embedded %d pages", st.EmbeddedPages)
	}

	bg.SetPaused(false)
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := db.GetEmbeddingStats(ctx, "m1")
		if err == nil && st.EmbeddedPages == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed runner did not index: %+v %v", st, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSemanticEnabledSettingPersists(t *testing.T) {
	db, ctx := openIndexDB(t)
	newSem := func() *Semantic {
		emb := &countingEmbedder{model: "m1"}
		ix := NewIndexer(db, emb, logging.Sugar{})
		return &Semantic{Embedder: NewProcess(ProcessConfig{}, logging.Sugar{}), Indexer: ix, Background: NewBackground(ix, logging.Sugar{})}
	}
	s := newSem()
	if err := s.LoadEnabled(ctx, db); err != nil || !s.Enabled() {
		t.Fatalf("default: enabled=%v err=%v, want on", s.Enabled(), err)
	}
	if err := s.SaveEnabled(ctx, db, false); err != nil || s.Enabled() || !s.Background.paused.Load() {
		t.Fatalf("after off: enabled=%v paused=%v err=%v", s.Enabled(), s.Background.paused.Load(), err)
	}
	s2 := newSem() // next serve start
	if err := s2.LoadEnabled(ctx, db); err != nil || s2.Enabled() {
		t.Fatalf("reloaded: enabled=%v err=%v, want off", s2.Enabled(), err)
	}
	if err := s2.SaveEnabled(ctx, db, true); err != nil || !s2.Enabled() || s2.Background.paused.Load() {
		t.Fatalf("after on: enabled=%v err=%v", s2.Enabled(), err)
	}
	var nilSem *Semantic
	if nilSem.Enabled() {
		t.Fatal("nil Semantic reports enabled")
	}
}
