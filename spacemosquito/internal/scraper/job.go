package scraper

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/internal/storage"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

type CrawlJob struct {
	ID          string     `json:"id"`
	SpaceURL    string     `json:"space_url"`
	Status      JobStatus  `json:"status"`
	Progress    int        `json:"progress"`
	TotalPages  int        `json:"total_pages"`
	Completed   int        `json:"completed"`
	Failed      int        `json:"failed"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type JobSnapshot struct {
	Jobs      []CrawlJob `json:"jobs"`
	Total     int        `json:"total"`
	Running   int        `json:"running"`
	Completed int        `json:"completed"`
	Failed    int        `json:"failed"`
	Pending   int        `json:"pending"`
}

type CrawlJobManager struct {
	jobs    map[string]*CrawlJob
	cancels map[string]context.CancelFunc
	mu      sync.RWMutex
	log     logging.Sugar
	cfg     *config.Config
	db      store.Store
	store   *session.Store
	storage *storage.Writer
	assets  *storage.AssetDownloader
	// PageSaved, if set, runs after each page a crawl saves (the semantic
	// index picks it up). It must not block.
	PageSaved func()
}

type CrawlRunner struct {
	manager *CrawlJobManager
	log     logging.Sugar
}

func NewJobManager(cfg *config.Config, database store.Store, store *session.Store, storageWriter *storage.Writer, assetDownloader *storage.AssetDownloader, log logging.Sugar) *CrawlJobManager {
	return &CrawlJobManager{
		jobs:    make(map[string]*CrawlJob),
		cancels: make(map[string]context.CancelFunc),
		log:     log,
		cfg:     cfg,
		db:      database,
		store:   store,
		storage: storageWriter,
		assets:  assetDownloader,
	}
}

func (m *CrawlJobManager) CreateJob(spaceURL string) (*CrawlJob, error) {
	job := &CrawlJob{
		ID:        uuid.New().String(),
		SpaceURL:  spaceURL,
		Status:    JobStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	if m.log.Enabled() {
		m.log.Infow("crawl job created",
			"job_id", job.ID,
			"space_url", spaceURL)
	}

	return job, nil
}

func (m *CrawlJobManager) GetJob(jobID string) (*CrawlJob, error) {
	m.mu.RLock()
	job, exists := m.jobs[jobID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	return job, nil
}

// HasActiveJob reports whether a crawl of spaceKey is pending or running.
func (m *CrawlJobManager) HasActiveJob(spaceKey string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, job := range m.jobs {
		if (job.Status == JobStatusPending || job.Status == JobStatusRunning) &&
			session.GetSpaceKeyFromURL(job.SpaceURL) == spaceKey {
			return true
		}
	}
	return false
}

func (m *CrawlJobManager) ListJobs() *JobSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := &JobSnapshot{
		Jobs: make([]CrawlJob, 0, len(m.jobs)),
	}

	for _, job := range m.jobs {
		snapshot.Jobs = append(snapshot.Jobs, *job)
		switch job.Status {
		case JobStatusRunning:
			snapshot.Running++
		case JobStatusCompleted:
			snapshot.Completed++
		case JobStatusFailed:
			snapshot.Failed++
		case JobStatusPending:
			snapshot.Pending++
		}
	}

	snapshot.Total = len(m.jobs)

	return snapshot
}

func (m *CrawlJobManager) CancelJob(jobID string) error {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}

	if job.Status != JobStatusPending && job.Status != JobStatusRunning {
		m.mu.Unlock()
		return fmt.Errorf("cannot cancel job with status: %s", job.Status)
	}

	job.Status = JobStatusCancelled
	job.UpdatedAt = time.Now()
	now := time.Now()
	job.CompletedAt = &now
	cancel := m.cancels[jobID]
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if m.log.Enabled() {
		m.log.Infow("crawl job cancelled", "job_id", jobID)
	}

	return nil
}

// RunJob starts a pending crawl. parent may be context.Background(); a
// child context is cancelled by CancelJob so the page loop can stop between pages.
func (m *CrawlJobManager) RunJob(ctx context.Context, jobID string) error {
	return m.runJob(ctx, jobID, nil)
}

// runJob is the testable entrypoint. If run is nil, the real CrawlRunner is used.
func (m *CrawlJobManager) runJob(ctx context.Context, jobID string, run func(context.Context, *CrawlJob) error) (retErr error) {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}

	if job.Status == JobStatusCancelled {
		m.mu.Unlock()
		return context.Canceled
	}

	if job.Status != JobStatusPending {
		m.mu.Unlock()
		return fmt.Errorf("job is not pending: %s", job.Status)
	}

	jobCtx, cancel := context.WithCancel(ctx)
	job.Status = JobStatusRunning
	now := time.Now()
	job.StartedAt = &now
	job.UpdatedAt = now
	m.cancels[jobID] = cancel
	m.mu.Unlock()

	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, jobID)
		m.mu.Unlock()
	}()

	if m.log.Enabled() {
		m.log.Infow("crawl job started",
			"job_id", jobID,
			"space_url", job.SpaceURL)
	}

	// A panic in the crawl (e.g. from the headless browser) must still end
	// the job: otherwise it stays "running", blocking new crawls and deletes
	// of the space until serve restarts.
	defer func() {
		if r := recover(); r != nil {
			if m.log.Enabled() {
				m.log.Errorw("crawl panicked",
					"job_id", jobID,
					"panic", fmt.Sprintf("%v", r))
			}
			m.finishJob(job, fmt.Errorf("crawl crashed: %v", r))
			retErr = fmt.Errorf("crawl crashed: %v", r)
		}
	}()

	if run == nil {
		run = func(c context.Context, j *CrawlJob) error {
			runner := &CrawlRunner{manager: m, log: m.log}
			return runner.Run(c, j)
		}
	}

	err := run(jobCtx, job)
	m.finishJob(job, err)

	if m.log.Enabled() {
		m.mu.RLock()
		status := job.Status
		m.mu.RUnlock()
		switch status {
		case JobStatusCancelled:
			m.log.Infow("crawl job cancelled",
				"job_id", jobID,
				"completed", job.Completed,
				"failed", job.Failed)
		case JobStatusFailed:
			m.log.Errorw("crawl job failed",
				"job_id", jobID,
				"error", err)
		default:
			m.log.Infow("crawl job completed",
				"job_id", jobID,
				"completed", job.Completed,
				"failed", job.Failed)
		}
	}

	return err
}

func (m *CrawlJobManager) finishJob(job *CrawlJob, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job.UpdatedAt = time.Now()
	if job.Status == JobStatusCancelled {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		job.Status = JobStatusCancelled
		now := time.Now()
		job.CompletedAt = &now
		job.Error = ""
		return
	}
	if err != nil {
		job.Status = JobStatusFailed
		job.Error = err.Error()
		now := time.Now()
		job.CompletedAt = &now
		return
	}
	job.Status = JobStatusCompleted
	now := time.Now()
	job.CompletedAt = &now
}

func (r *CrawlRunner) Run(ctx context.Context, job *CrawlJob) error {
	// Create a new scraper instance for this job
	scraper := New(
		r.manager.cfg,
		r.manager.db,
		r.manager.storage,
		r.manager.assets,
		r.log,
	)

	// Initialize context for the scraper to prevent nil pointer dereferences in DB calls
	scraper.ctx = ctx

	// Load session
	encKey := r.manager.cfg.Session.EncryptionKey
	if encKey == "" {
		return fmt.Errorf("encryption key not configured")
	}

	sess, err := r.manager.store.GetForURL(encKey, job.SpaceURL)
	if err != nil {
		return fmt.Errorf("load session for %s: %w", job.SpaceURL, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Discover and crawl
	pageInfo, err := scraper.discoverSpace(job.SpaceURL, sess)
	if err != nil {
		if session.IsUnauthorized(err) {
			r.abortForUnauthorized(job, encKey, "discovery", err)
			return fmt.Errorf("session expired — recapture: %w", err)
		}
		return fmt.Errorf("discover space: %w", err)
	}

	keepSessionValid := r.sessionKeeper(job, encKey)
	keepSessionValid()

	job.TotalPages = len(pageInfo.Pages)
	now := time.Now()
	job.UpdatedAt = now
	if r.manager.log.Enabled() {
		r.manager.log.Infow("discovery complete",
			"job_id", job.ID,
			"total_pages", job.TotalPages)
	}

	// Persist total as soon as discovery finishes so cancel/fail still shows crawled/total.
	// Use Background so a near-simultaneous cancel does not skip the write.
	persistCtx := context.Background()
	persistDiscoveryPagesTotal(persistCtx, r.manager.db, pageInfo.SpaceKey, pageInfo.SpaceName, job.SpaceURL, job.TotalPages, r.log)

	scraper.saveFolders(pageInfo.SpaceKey, pageInfo.Folders)
	index := scraper.startCatalogIndex(pageInfo.SpaceKey)
	defer index.finish()

	for i := 0; i < len(pageInfo.Pages); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pg := pageInfo.Pages[i]

		// Check if page needs scraping (Incremental logic)
		if pg.Version > 0 {
			existingPage, err := scraper.db.GetPage(ctx, pageInfo.SpaceKey, pg.ConfluenceID)
			if err == nil && existingPage.Version >= pg.Version {
				if r.log.Enabled() {
					r.log.Infow("skipping unchanged page", "job_id", job.ID, "page_id", pg.ConfluenceID, "version", pg.Version)
				}
				scraper.refreshHistory(pageInfo.SpaceKey, pg)
				r.manager.mu.Lock()
				job.Completed++
				if job.TotalPages > 0 {
					job.Progress = int(float64(job.Completed) / float64(job.TotalPages) * 100)
				}
				job.UpdatedAt = time.Now()
				r.manager.mu.Unlock()
				continue
			}
		}

		// Try API scraping first
		err := scraper.ScrapePageAPI(pg, pageInfo.SpaceKey, pageInfo.SpaceURL, sess)
		if err != nil {
			if session.IsUnauthorized(err) {
				r.abortForUnauthorized(job, encKey, "crawl_page", err)
				return fmt.Errorf("session expired — recapture: %w", err)
			}
			if r.log.Enabled() {
				r.log.Warnw("API page scrape failed, falling back to browser",
					"job_id", job.ID, "page_id", pg.ConfluenceID, "error", err)
			}

			// Fallback to browser
			if scraper.browser == nil {
				if err := scraper.LaunchBrowser(); err != nil {
					r.manager.mu.Lock()
					job.Failed++
					r.manager.mu.Unlock()
					continue
				}
				if err := scraper.SetupContextWithSession(sess); err != nil {
					r.manager.mu.Lock()
					job.Failed++
					r.manager.mu.Unlock()
					continue
				}
			}

			if err := scraper.ScrapePage(pg, pageInfo.SpaceKey, pageInfo.SpaceURL); err != nil {
				r.manager.mu.Lock()
				job.Failed++
				r.manager.mu.Unlock()
				if r.log.Enabled() {
					r.log.Errorw("browser page scrape failed",
						"job_id", job.ID,
						"page_id", pg.ConfluenceID,
						"error", err)
				}
				continue
			}
		} else {
			keepSessionValid()
		}

		r.manager.mu.Lock()
		job.Completed++
		if job.TotalPages > 0 {
			job.Progress = int(float64(job.Completed) / float64(job.TotalPages) * 100)
		}
		job.UpdatedAt = time.Now()
		r.manager.mu.Unlock()
		index.pageSaved()
		if r.manager.PageSaved != nil {
			r.manager.PageSaved()
		}

		if r.log.Enabled() {
			r.log.Infow("page crawled",
				"job_id", job.ID,
				"page", i+1,
				"total", job.TotalPages,
				"completed", job.Completed)
		}
	}

	// Update space page count
	if job.Failed == job.TotalPages && job.TotalPages > 0 {
		return fmt.Errorf("all %d pages failed to crawl", job.TotalPages)
	}

	if err := r.manager.db.UpdateSpaceLastCrawled(ctx, pageInfo.SpaceKey); err != nil {
		r.log.Warnw("failed to update space last crawled",
			"job_id", job.ID,
			"error", err)
	}

	reconcileSpacePagesTotal(context.Background(), r.manager.db, pageInfo.SpaceKey, job.TotalPages, r.log)

	return nil
}

// sessionValidRefresh is how often a crawl that keeps getting pages from
// Confluence renews the session's validation, so the extension does not
// report it expired (and gate its buttons) during a crawl longer than
// session.ValidateTTL.
const sessionValidRefresh = 5 * time.Minute

// sessionKeeper returns a func to call after each successful Confluence
// request; it renews the validation at most every sessionValidRefresh.
func (r *CrawlRunner) sessionKeeper(job *CrawlJob, encKey string) func() {
	var last time.Time
	return func() {
		if time.Since(last) < sessionValidRefresh {
			return
		}
		last = time.Now()
		if err := r.manager.store.MarkValidatedForURL(encKey, job.SpaceURL); err != nil {
			r.log.Warnw("failed to renew session validation", "job_id", job.ID, "error", err)
		}
	}
}

func (r *CrawlRunner) abortForUnauthorized(job *CrawlJob, encKey, operation string, err error) {
	r.log.Errorw("session_unauthorized",
		"operation", operation,
		"job_id", job.ID,
		"host", session.HostnameKey(job.SpaceURL),
		"completed", job.Completed,
		"failed", job.Failed,
		"total", job.TotalPages,
		"error", err)
	if clearErr := r.manager.store.ClearValidationForURL(encKey, job.SpaceURL); clearErr != nil {
		r.log.Warnw("failed to clear session validation", "job_id", job.ID, "error", clearErr)
	} else {
		r.log.Warnw("crawl_aborted_session",
			"job_id", job.ID,
			"completed", job.Completed,
			"total", job.TotalPages)
	}
}

func (m *CrawlJobManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, job := range m.jobs {
		if job.Status == JobStatusCompleted || job.Status == JobStatusFailed || job.Status == JobStatusCancelled {
			delete(m.jobs, id)
			delete(m.cancels, id)
		}
	}

	if m.log.Enabled() {
		m.log.Infow("crawl jobs cleaned up", "remaining", len(m.jobs))
	}
}
