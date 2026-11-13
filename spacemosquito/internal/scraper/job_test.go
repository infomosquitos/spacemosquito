package scraper

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/pkg/logging"
)

func testJobManager(t *testing.T) *CrawlJobManager {
	t.Helper()
	return NewJobManager(&config.Config{}, nil, nil, nil, nil, logging.Sugar{})
}

func TestCrawlJobManager_CreateAndGet(t *testing.T) {
	m := testJobManager(t)
	job, err := m.CreateJob("https://example.atlassian.net/wiki/spaces/PROJ")
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == "" {
		t.Fatal("expected job ID")
	}
	if job.Status != JobStatusPending {
		t.Errorf("status = %q", job.Status)
	}

	got, err := m.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpaceURL != job.SpaceURL {
		t.Errorf("SpaceURL = %q", got.SpaceURL)
	}
}

func TestCrawlJobManager_GetJob_notFound(t *testing.T) {
	m := testJobManager(t)
	_, err := m.GetJob("missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCrawlJobManager_ListJobs_counts(t *testing.T) {
	m := testJobManager(t)
	j1, _ := m.CreateJob("https://a")
	j2, _ := m.CreateJob("https://b")

	m.mu.Lock()
	m.jobs[j1.ID].Status = JobStatusRunning
	m.jobs[j2.ID].Status = JobStatusCompleted
	m.mu.Unlock()

	snap := m.ListJobs()
	if snap.Total != 2 || snap.Running != 1 || snap.Completed != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestCrawlJobManager_CancelJob(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://a")

	if err := m.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetJob(job.ID)
	if got.Status != JobStatusCancelled {
		t.Errorf("status = %q", got.Status)
	}

	if err := m.CancelJob("missing"); err == nil {
		t.Fatal("expected error for missing job")
	}

	m.jobs[job.ID].Status = JobStatusCompleted
	if err := m.CancelJob(job.ID); err == nil {
		t.Fatal("expected error cancelling completed job")
	}
}

func TestCrawlJobManager_CancelJob_cancelsRunningContext(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://a")

	started := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- m.runJob(context.Background(), job.ID, func(ctx context.Context, j *CrawlJob) error {
			close(started)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("timed out waiting for cancel")
			}
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not start")
	}

	if err := m.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit after cancel")
	}

	got, _ := m.GetJob(job.ID)
	if got.Status != JobStatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
}

func TestCrawlJobManager_RunJob_doesNotOverwriteCancelled(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://a")

	var once sync.Once
	cancelGate := make(chan struct{})

	err := m.runJob(context.Background(), job.ID, func(ctx context.Context, j *CrawlJob) error {
		once.Do(func() {
			if cerr := m.CancelJob(j.ID); cerr != nil {
				t.Errorf("CancelJob: %v", cerr)
			}
			close(cancelGate)
		})
		<-cancelGate
		// Simulate a normal successful finish after cancel was requested —
		// finishJob must still leave status as cancelled.
		return nil
	})
	if err != nil {
		t.Fatalf("runJob err = %v", err)
	}

	got, _ := m.GetJob(job.ID)
	if got.Status != JobStatusCancelled {
		t.Fatalf("status = %q, want cancelled (must not overwrite to completed)", got.Status)
	}
}

func TestCrawlJobManager_RunJob_contextCanceledSetsCancelled(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://a")

	err := m.runJob(context.Background(), job.ID, func(ctx context.Context, j *CrawlJob) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	got, _ := m.GetJob(job.ID)
	if got.Status != JobStatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
}

func TestCrawlJobManager_Cleanup(t *testing.T) {
	m := testJobManager(t)
	done, _ := m.CreateJob("https://done")
	pending, _ := m.CreateJob("https://pending")

	m.mu.Lock()
	m.jobs[done.ID].Status = JobStatusCompleted
	m.mu.Unlock()

	m.Cleanup()
	if _, err := m.GetJob(done.ID); err == nil {
		t.Fatal("completed job should be removed")
	}
	if _, err := m.GetJob(pending.ID); err != nil {
		t.Fatal("pending job should remain")
	}
}

func TestCrawlJobManager_RunJob_notPending(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://a")
	m.mu.Lock()
	m.jobs[job.ID].Status = JobStatusRunning
	m.mu.Unlock()

	err := m.RunJob(t.Context(), job.ID)
	if err == nil {
		t.Fatal("expected error when job is not pending")
	}
}

func TestCrawlJobManager_RunJob_alreadyCancelled(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://a")
	if err := m.CancelJob(job.ID); err != nil {
		t.Fatal(err)
	}
	err := m.RunJob(context.Background(), job.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	got, _ := m.GetJob(job.ID)
	if got.Status != JobStatusCancelled {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestHasActiveJob(t *testing.T) {
	m := &CrawlJobManager{jobs: map[string]*CrawlJob{
		"a": {SpaceURL: "https://x.atlassian.net/wiki/spaces/RUN/overview", Status: JobStatusRunning},
		"b": {SpaceURL: "https://x.atlassian.net/wiki/spaces/DONE/overview", Status: JobStatusCompleted},
		"c": {SpaceURL: "https://x.atlassian.net/wiki/spaces/WAIT", Status: JobStatusPending},
	}}
	for key, want := range map[string]bool{"RUN": true, "WAIT": true, "DONE": false, "NONE": false} {
		if got := m.HasActiveJob(key); got != want {
			t.Errorf("HasActiveJob(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestCrawlJobManager_RunJob_panicEndsJobAsFailed(t *testing.T) {
	m := testJobManager(t)
	job, _ := m.CreateJob("https://x.atlassian.net/wiki/spaces/TPC/overview")

	err := m.runJob(context.Background(), job.ID, func(ctx context.Context, j *CrawlJob) error {
		panic("use of closed network connection")
	})
	if err == nil || !strings.Contains(err.Error(), "crawl crashed") {
		t.Fatalf("err = %v, want crawl crashed", err)
	}
	got, _ := m.GetJob(job.ID)
	if got.Status != JobStatusFailed || !strings.Contains(got.Error, "closed network connection") || got.CompletedAt == nil {
		t.Fatalf("job = %+v, want failed with the panic as error", got)
	}
	if m.HasActiveJob("TPC") {
		t.Fatal("crashed crawl still counts as active: blocks new crawls of the space")
	}
}
