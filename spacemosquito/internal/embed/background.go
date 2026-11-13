package embed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vkh/spacemosquito/pkg/logging"
)

// Timing of the background runner (variables for tests).
var (
	// backgroundDebounce collects page saves (a crawl saves one every few
	// seconds) into one backfill pass.
	backgroundDebounce = 3 * time.Second
	// backgroundPoll catches changes nobody announced (cron, reindex); a
	// pass over an unchanged catalog only hashes its text.
	backgroundPoll = time.Minute
	// unavailableRetry waits before trying again after ErrUnavailable.
	unavailableRetry = 5 * time.Minute
)

// Status reports the background indexer.
type Status struct {
	Running   bool   `json:"running"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	LastError string `json:"last_error,omitempty"`
}

// Background keeps the semantic index current inside serve.
type Background struct {
	ix   *Indexer
	log  logging.Sugar
	wake chan struct{}

	paused atomic.Bool

	mu         sync.Mutex
	status     Status
	cancelPass context.CancelFunc
}

// SetPaused stops indexing (cancelling a running pass) or resumes it with
// a pass soon. Run keeps going while paused but does no work.
func (b *Background) SetPaused(paused bool) {
	b.paused.Store(paused)
	if !paused {
		b.Notify()
		return
	}
	b.mu.Lock()
	if b.cancelPass != nil {
		b.cancelPass()
	}
	b.mu.Unlock()
}

// NewBackground returns a runner for ix; call Run in a goroutine.
func NewBackground(ix *Indexer, log logging.Sugar) *Background {
	return &Background{ix: ix, log: log, wake: make(chan struct{}, 1)}
}

// Notify asks for a pass soon (e.g. after a page was saved). Never blocks.
func (b *Background) Notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Status returns the current pass's progress and the last error.
func (b *Background) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

// Run indexes stale pages at start, after Notify and periodically, until ctx ends.
func (b *Background) Run(ctx context.Context) {
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.wake:
			if !sleep(ctx, backgroundDebounce) {
				return
			}
		case <-time.After(wait):
		}
		wait = backgroundPoll
		if b.paused.Load() {
			continue
		}
		if err := b.pass(ctx); errors.Is(err, ErrUnavailable) {
			wait = unavailableRetry
		}
	}
}

func (b *Background) pass(parent context.Context) error {
	start := time.Now()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	b.setStatus(func(s *Status) { s.Running, s.Done, s.Total = true, 0, 0 })
	b.mu.Lock()
	b.cancelPass = cancel
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.cancelPass = nil
		b.mu.Unlock()
	}()
	if b.paused.Load() { // paused between the check in Run and here
		cancel()
	}
	res, err := b.ix.Backfill(ctx, "", func(done, total int) {
		b.setStatus(func(s *Status) { s.Done, s.Total = done, total })
		if b.log.Enabled() && (done%50 == 0 || done == total) {
			b.log.Infow("semantic index progress", "done", done, "total", total)
		}
	})
	b.setStatus(func(s *Status) {
		s.Running = false
		s.LastError = ""
		if err != nil && ctx.Err() == nil {
			s.LastError = err.Error()
		}
	})
	switch {
	case ctx.Err() != nil:
	case err != nil:
		if b.log.Enabled() {
			b.log.Warnw("semantic index paused", "error", err)
		}
	case res.Pages > 0 && b.log.Enabled():
		b.log.Infow("semantic index updated", "pages", res.Indexed, "failed", res.Failed,
			"chunks_embedded", res.Chunks, "up_to_date", res.UpToDate, "duration", time.Since(start).Round(time.Second))
	}
	return err
}

func (b *Background) setStatus(f func(*Status)) {
	b.mu.Lock()
	f(&b.status)
	b.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
