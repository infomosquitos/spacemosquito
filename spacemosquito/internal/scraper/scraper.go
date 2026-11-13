package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/confluence"
	"github.com/vkh/spacemosquito/internal/contentmd"
	"github.com/vkh/spacemosquito/internal/contentmd/csf"
	"github.com/vkh/spacemosquito/internal/ratelimit"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/internal/storage"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// Page represents a discovered Confluence page in the tree.
type Page struct {
	ConfluenceID int    `json:"confluence_id"`
	Version      int    `json:"version"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	ParentID     *int   `json:"parent_id,omitempty"`
	Level        int    `json:"level"`
	Content      string `json:"content,omitempty"`
	CleanHTML    string `json:"clean_html,omitempty"`
	RawHTML      string `json:"raw_html,omitempty"`
	// BodyFormat is "storage" (API, Confluence Storage Format) or "rendered"
	// (browser fallback). Set explicitly: an empty storage body cannot be sniffed.
	BodyFormat   string `json:"body_format,omitempty"`
	Images       []storage.AssetRef
	Attachments  []storage.AssetRef
	FileDir      string `json:"file_dir,omitempty"`
	HTMLPath     string `json:"html_path,omitempty"`
	RawHTMLPath  string `json:"raw_html_path,omitempty"`
	MetadataPath string `json:"metadata_path,omitempty"`
	// History: Confluence created/updated by and when (discovery, API fetch).
	History store.PageHistory `json:"history,omitempty"`
	// Files are the page's file attachments (API pages only).
	Files []attachmentFile `json:"-"`
	// resolvePageID finds other pages by title (API pages only).
	resolvePageID func(spaceKey, title string) int
}

// CrawlStats tracks crawl progress.
type CrawlStats struct {
	TotalPages            int
	SkippedPages          int
	SkippedUnchanged      int
	FailedPages           int
	ImagesDownloaded      int
	AttachmentsDownloaded int
}

// Scraper manages browser lifecycle and crawl operations.
type Scraper struct {
	browser *rod.Browser
	ctx     context.Context
	cancel  context.CancelFunc
	cfg     *config.Config
	db      store.Store
	storage *storage.Writer
	assets  *storage.AssetDownloader
	log     logging.Sugar
	stats   CrawlStats
	mu      sync.Mutex
	force   bool
	pageIDs pageIDCache // page IDs by title, for attachments of other pages
	users   userCache   // display names for @-mentions
}

// SetForce makes the next crawl re-scrape every page, ignoring the unchanged
// version skip. Pair with AssetDownloader.SetForce so assets are re-fetched too.
func (s *Scraper) SetForce(force bool) {
	s.mu.Lock()
	s.force = force
	s.mu.Unlock()
}

// New creates a new Scraper with the given config and dependencies.
func New(
	cfg *config.Config,
	database store.Store,
	storageWriter *storage.Writer,
	assetDownloader *storage.AssetDownloader,
	log logging.Sugar,
) *Scraper {
	return &Scraper{
		cfg:     cfg,
		db:      database,
		storage: storageWriter,
		assets:  assetDownloader,
		log:     log,
	}
}

// LaunchBrowser creates a rod browser instance with Chromium headless (lazy, idempotent).
func (s *Scraper) LaunchBrowser() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.browser != nil {
		return nil
	}

	bin, source, err := resolveChromiumBin(s.cfg, s.log)
	if err != nil {
		return err
	}
	if s.log.Enabled() {
		s.log.Infow("launching Chromium", "source", source, "bin", bin)
	}

	url, err := buildLauncher(bin).Launch()
	if err != nil {
		return fmt.Errorf("launch chromium: %w", err)
	}

	s.browser = rod.New().ControlURL(url).MustConnect()
	if s.ctx == nil || s.cancel == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}

	if s.log.Enabled() {
		s.log.Info("rod browser created", "control_url", url)
	}
	return nil
}

func (s *Scraper) Browser() *rod.Browser {
	return s.browser
}

// SetupContextWithSession injects cookies from a session into the browser.
func (s *Scraper) SetupContextWithSession(sess *session.Session) error {
	if len(sess.Cookies) == 0 {
		return nil
	}

	cookies := make([]*proto.NetworkCookie, 0, len(sess.Cookies))
	for _, c := range sess.Cookies {
		cookie := &proto.NetworkCookie{
			Name:     c.Name,
			Value:    strings.TrimSpace(c.Value),
			Domain:   strings.TrimSpace(c.Domain),
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
		}
		switch strings.TrimSpace(c.SameSite) {
		case "None":
			cookie.SameSite = proto.NetworkCookieSameSiteNone
		case "Lax":
			cookie.SameSite = proto.NetworkCookieSameSiteLax
		case "Strict":
			cookie.SameSite = proto.NetworkCookieSameSiteStrict
		}
		if c.Expires > 0 {
			cookie.Expires = proto.TimeSinceEpoch(time.Unix(int64(c.Expires), 0).Unix() * 1000)
		}
		cookies = append(cookies, cookie)
	}

	s.browser.MustSetCookies(cookies...)

	if s.log.Enabled() {
		s.log.Infow("rod context ready",
			"cookie_count", len(sess.Cookies),
			"confluence_url", sess.ConfluenceURL)
	}

	return nil
}

// CloseBrowser tears down the browser.
func (s *Scraper) CloseBrowser() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		if s.log.Enabled() {
			s.log.Debug("rod context closed")
		}
	}
	if s.browser != nil {
		s.browser.Close()
		s.browser = nil
		if s.log.Enabled() {
			s.log.Debug("rod browser closed")
		}
	}
}

// skipUnchangedPage reports whether a discovered page should be skipped because
// the catalog already has the same or newer version. Force crawls never skip.
func skipUnchangedPage(force bool, discoveredVersion int, existing *store.Page, err error) bool {
	if force || discoveredVersion <= 0 || err != nil || existing == nil {
		return false
	}
	return existing.Version >= discoveredVersion
}

// CrawlPage refreshes a single page by space key and Confluence ID. Always
// overwrites local text and assets (no version skip); reuses an existing
// catalog file_dir when present so title renames do not orphan directories.
func (s *Scraper) CrawlPage(spaceKey string, confluenceID int, sess *session.Session) error {
	if spaceKey == "" {
		return fmt.Errorf("space key is required")
	}
	if confluenceID <= 0 {
		return fmt.Errorf("confluence id must be positive")
	}
	if sess == nil {
		return fmt.Errorf("session is required")
	}

	s.SetForce(true)
	if s.assets != nil {
		s.assets.SetForce(true)
	}
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}

	spaceURL, err := s.resolveSpaceURL(spaceKey, sess)
	if err != nil {
		return err
	}

	pg := &Page{ConfluenceID: confluenceID}
	existing, err := s.db.GetPage(s.ctx, spaceKey, confluenceID)
	if err == nil && existing != nil && existing.FileDir != "" {
		pg.FileDir = existing.FileDir
		if existing.Title != "" {
			pg.Title = existing.Title
		}
	}

	if s.log.Enabled() {
		s.log.Infow("crawl-page started",
			"space_key", spaceKey,
			"confluence_id", confluenceID,
			"space_url", spaceURL,
			"file_dir", pg.FileDir)
	}

	if err := s.ScrapePageAPI(pg, spaceKey, spaceURL, sess); err != nil {
		return err
	}
	s.writeCatalogIndex(spaceKey)

	if s.log.Enabled() {
		s.log.Infow("crawl-page completed",
			"space_key", spaceKey,
			"confluence_id", confluenceID,
			"title", pg.Title,
			"file_dir", pg.FileDir)
	}
	return nil
}

// resolveSpaceURL returns the space overview URL from the catalog, or derives
// it from the session Confluence base URL when the space row is missing.
func (s *Scraper) resolveSpaceURL(spaceKey string, sess *session.Session) (string, error) {
	if space, err := s.db.GetSpaceByKey(context.Background(), spaceKey); err == nil && space != nil && space.URL != "" {
		return space.URL, nil
	}
	base := strings.TrimRight(sess.ConfluenceURL, "/")
	if base == "" {
		return "", fmt.Errorf("cannot resolve space URL: space %q not in catalog and session has no confluence URL", spaceKey)
	}
	if sess.Flavor == session.FlavorCloud || sess.Flavor == "" {
		return base + "/wiki/spaces/" + spaceKey, nil
	}
	return base + "/spaces/" + spaceKey, nil
}

// CrawlSpace performs a full crawl of a Confluence space.
func (s *Scraper) CrawlSpace(spaceURL string, sess *session.Session) error {
	crawlStart := time.Now()

	s.log.Infow("crawl started",
		"space_url", spaceURL,
		"flavor", sess.Flavor,
		"session_captured_at", sess.CapturedAt)

	// Context is needed for DB operations
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	pageInfo, err := s.discoverSpace(spaceURL, sess)
	if err != nil {
		if session.IsUnauthorized(err) {
			s.log.Errorw("session_unauthorized",
				"operation", "discovery",
				"host", session.HostnameKey(spaceURL),
				"error", err)
			return fmt.Errorf("session expired — recapture: %w", err)
		}
		return fmt.Errorf("discover space: %w", err)
	}

	// Close browser if it was opened for fallback
	defer s.CloseBrowser()

	if s.log.Enabled() {
		s.log.Infow("space discovery complete",
			"space_key", pageInfo.SpaceKey,
			"page_count", len(pageInfo.Pages),
			"duration_ms", time.Since(crawlStart).Milliseconds())
	}

	spaceID, err := s.db.CreateSpace(s.ctx, pageInfo.SpaceKey, pageInfo.SpaceName, spaceURL)
	if err != nil {
		s.log.Warnw("failed to create space record, continuing",
			"space_key", pageInfo.SpaceKey,
			"error", err)
	} else if s.log.Enabled() {
		s.log.Infow("space record created",
			"space_key", pageInfo.SpaceKey,
			"space_id", spaceID)
	}

	// Persist total immediately so interrupted crawls still show crawled/total.
	if err := s.db.UpdateSpacePagesTotal(s.ctx, pageInfo.SpaceKey, len(pageInfo.Pages)); err != nil {
		s.log.Warnw("failed to store discovery pages_total",
			"space_key", pageInfo.SpaceKey,
			"error", err)
	}

	s.saveFolders(pageInfo.SpaceKey, pageInfo.Folders)
	index := s.startCatalogIndex(pageInfo.SpaceKey)
	defer index.finish()

	for i, pg := range pageInfo.Pages {
		s.log.Infow("crawling page",
			"space_key", pageInfo.SpaceKey,
			"page_index", i+1,
			"page_total", len(pageInfo.Pages),
			"page_id", pg.ConfluenceID,
			"version", pg.Version,
			"title", pg.Title)

		// Check if page needs scraping (bypassed when crawl --force)
		s.mu.Lock()
		force := s.force
		s.mu.Unlock()
		if pg.Version > 0 {
			existingPage, err := s.db.GetPage(s.ctx, pageInfo.SpaceKey, pg.ConfluenceID)
			if skipUnchangedPage(force, pg.Version, existingPage, err) {
				if s.log.Enabled() {
					s.log.Infow("skipping unchanged page", "page_id", pg.ConfluenceID, "version", pg.Version)
				}
				s.refreshHistory(pageInfo.SpaceKey, pg)
				s.mu.Lock()
				s.stats.SkippedUnchanged++
				s.stats.TotalPages++ // Count as processed
				s.mu.Unlock()
				continue
			}
		}

		// Try API scraping first
		err := s.ScrapePageAPI(pg, pageInfo.SpaceKey, pageInfo.SpaceURL, sess)
		if err != nil {
			if session.IsUnauthorized(err) {
				s.log.Errorw("session_unauthorized",
					"operation", "crawl_page",
					"page_id", pg.ConfluenceID,
					"host", session.HostnameKey(spaceURL),
					"error", err)
				return fmt.Errorf("session expired — recapture: %w", err)
			}
			if s.log.Enabled() {
				s.log.Warnw("API page content extraction failed, FALLING BACK to browser",
					"page_id", pg.ConfluenceID, "title", pg.Title, "error", err)
			}

			// Fallback to browser
			if s.browser == nil {
				if err := s.LaunchBrowser(); err != nil {
					s.log.Errorw("failed to launch browser for fallback", "error", err)
					s.mu.Lock()
					s.stats.FailedPages++
					s.mu.Unlock()
					continue
				}
				if err := s.SetupContextWithSession(sess); err != nil {
					s.log.Errorw("failed to setup browser context", "error", err)
					s.mu.Lock()
					s.stats.FailedPages++
					s.mu.Unlock()
					continue
				}
			}

			if err := s.ScrapePage(pg, pageInfo.SpaceKey, pageInfo.SpaceURL); err != nil {
				s.log.Errorw("browser page scrape failed",
					"space_key", pageInfo.SpaceKey,
					"page_id", pg.ConfluenceID,
					"title", pg.Title,
					"error", err)
				s.mu.Lock()
				s.stats.FailedPages++
				s.mu.Unlock()
				continue
			}
		}

		s.mu.Lock()
		s.stats.TotalPages++
		s.mu.Unlock()
		index.pageSaved()
	}

	duration := time.Since(crawlStart)
	if s.log.Enabled() {
		s.log.Infow("crawl complete",
			"space_key", pageInfo.SpaceKey,
			"pages_crawled", s.stats.TotalPages,
			"pages_skipped", s.stats.SkippedPages,
			"pages_skipped_unchanged", s.stats.SkippedUnchanged,
			"pages_failed", s.stats.FailedPages,
			"images_downloaded", s.stats.ImagesDownloaded,
			"attachments_downloaded", s.stats.AttachmentsDownloaded,
			"duration", duration.Round(time.Millisecond))
	}

	// Stale page sweep logic (Disabled for now, implement later as a manual action)
	/*
		if s.log.Enabled() {
			s.log.Infow("sweeping stale pages deleted from Confluence", "space_key", pageInfo.SpaceKey)
		}
		deletedCount, err := s.db.DeleteStalePages(s.ctx, spaceID, crawlStart)
		if err != nil {
			s.log.Warnw("failed to sweep stale pages", "space_key", pageInfo.SpaceKey, "error", err)
		} else if deletedCount > 0 && s.log.Enabled() {
			s.log.Infow("stale pages swept", "space_key", pageInfo.SpaceKey, "deleted_count", deletedCount)
		}
	*/

	if err := s.db.UpdateSpaceLastCrawled(s.ctx, pageInfo.SpaceKey); err != nil {
		s.log.Warnw("failed to update space last crawled",
			"space_key", pageInfo.SpaceKey,
			"error", err)
	}

	reconcileSpacePagesTotal(s.ctx, s.db, pageInfo.SpaceKey, len(pageInfo.Pages), s.log)

	return nil
}

// ScrapePageAPI fetches page content using the REST API.
func (s *Scraper) ScrapePageAPI(pg *Page, spaceKey, spaceURL string, sess *session.Session) error {
	baseURL := extractConfluenceBaseURL(spaceURL)

	// Attach the session to attachment downloads too — without it, SSO-protected
	// instances answer /download/attachments/ with a login page, not the file.
	s.assets.SetAuthHeaders(sess.AsHeaders())

	var apiURL string
	if sess.Flavor == session.FlavorCloud {
		apiURL = fmt.Sprintf("%s/wiki/rest/api/content/%d?expand=body.storage,version,ancestors,space,history", baseURL, pg.ConfluenceID)
	} else {
		apiURL = fmt.Sprintf("%s/rest/api/content/%d?expand=body.storage,version,ancestors,space,history", baseURL, pg.ConfluenceID)
	}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return err
	}

	for k, v := range sess.AsHeaders() {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := ratelimit.Do(client, req, s.logThrottle)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("page %d not found", pg.ConfluenceID)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return session.UnauthorizedHTTP(resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API request failed with status %d", resp.StatusCode)
	}

	var result struct {
		ID      string     `json:"id"`
		Title   string     `json:"title"`
		Version apiVersion `json:"version"`
		History apiHistory `json:"history"`
		Space   struct {
			Key string `json:"key"`
		} `json:"space"`
		Ancestors []struct {
			ID string `json:"id"`
		} `json:"ancestors"`
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
		Links struct {
			Webui string `json:"webui"`
		} `json:"_links"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.Space.Key != "" && !strings.EqualFold(result.Space.Key, spaceKey) {
		return fmt.Errorf("page %d is in space %q, not %q", pg.ConfluenceID, result.Space.Key, spaceKey)
	}

	// An empty body is a real page without content of its own (typically a
	// parent of other pages); save it rather than falling back to the browser.
	storageHTML := result.Body.Storage.Value

	if result.Title != "" {
		pg.Title = result.Title
	}
	if result.Version.Number > 0 {
		pg.Version = result.Version.Number
	}
	mergeHistory(&pg.History, pageHistory(result.Version, result.History))
	s.rememberUsers(historyUsers(result.Version, result.History))
	if len(result.Ancestors) > 0 {
		var parentID int
		if _, err := fmt.Sscanf(result.Ancestors[len(result.Ancestors)-1].ID, "%d", &parentID); err == nil && parentID > 0 {
			pg.ParentID = &parentID
		}
	}
	if pg.URL == "" {
		cloud := sess.Flavor == session.FlavorCloud
		webui := result.Links.Webui
		if webui == "" {
			webui = fmt.Sprintf("/spaces/%s/pages/%d", spaceKey, pg.ConfluenceID)
		}
		pg.URL = confluence.WebUIURL(baseURL, webui, cloud)
	}

	pg.RawHTML = storageHTML
	pg.BodyFormat = "storage"

	if s.log.Enabled() {
		s.log.Debugw("page content captured via API",
			"url", pg.URL,
			"html_size", len(storageHTML))
	}

	// Process content (asset downloads etc still use the scraper's logic)
	cleanHTML, images, attachments, err := s.extractContent(storageHTML, pg.Title, baseURL)
	if err != nil {
		return fmt.Errorf("extract content: %w", err)
	}

	pg.CleanHTML = cleanHTML
	pg.Images = images
	pg.Attachments = attachments

	pg.resolvePageID = s.pageIDResolver(baseURL, spaceKey, sess)
	s.resolveMentions(baseURL, storageHTML, sess)

	// File attachments (pptx, pdf, …): best-effort, a failed listing still
	// saves the page.
	if files, err := s.fetchAttachments(baseURL, pg.ConfluenceID, sess); err != nil {
		s.log.Warnw("list attachments failed", "page_id", pg.ConfluenceID, "error", err)
	} else {
		pg.Files = files
	}

	// Save to disk and DB (shared logic with browser scraping)
	return s.savePageMetadata(pg, spaceKey, spaceURL, sess.Flavor == session.FlavorCloud)
}

// savePageMetadata saves the scraped page to disk and database.
// If pg.FileDir is already set (e.g. crawl-page reusing an existing catalog
// path), that directory is wiped then reused; otherwise a new dir is created
// as `{confluenceID}-{sanitizedTitle}`.
func (s *Scraper) savePageMetadata(pg *Page, spaceKey, spaceURL string, cloud bool) error {
	var dir string
	var err error
	if pg.FileDir != "" {
		dir = pg.FileDir
		if err := s.storage.ClearPageDir(dir); err != nil {
			return fmt.Errorf("clear page dir: %w", err)
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("ensure page dir: %w", err)
		}
	} else {
		dir, err = s.storage.MakePageDir(spaceKey, pg.Title, pg.ConfluenceID)
		if err != nil {
			return fmt.Errorf("make page dir: %w", err)
		}
		pg.FileDir = dir
	}

	contentMD, assets, err := s.renderMarkdown(pg, spaceURL, cloud)
	if err != nil {
		return fmt.Errorf("convert content markdown: %w", err)
	}
	if err := s.storage.SaveMarkdown(dir, contentMD); err != nil {
		return fmt.Errorf("save content markdown: %w", err)
	}

	// The browse UI renders pages from content.md (ADR-018). Browser-fallback
	// pages also keep their extracted HTML: it is the source of their
	// content.md (reindex --content). A refreshed CSF page drops index.html
	// written by earlier versions.
	if pg.isStorage() {
		if err := os.Remove(filepath.Join(dir, "index.html")); err != nil && !os.IsNotExist(err) {
			s.log.Warnw("remove legacy index.html failed", "dir", dir, "error", err)
		}
		pg.HTMLPath = ""
	} else {
		if err := s.storage.SaveHTML(dir, pg.CleanHTML); err != nil {
			return fmt.Errorf("save clean html: %w", err)
		}
		pg.HTMLPath = dir + "/index.html"
	}

	// CSF conversion schedules asset downloads (images, diagrams); fetch them
	// into the page dir and record them alongside any HTML-path images.
	csfImages, csfDiagrams := s.downloadCSFAssets(dir, assets)
	pg.Images = append(pg.Images, csfImages...)
	if len(pg.Files) > 0 {
		pg.Attachments = s.downloadAttachments(dir, pg.Files)
	}

	if err := s.storage.SaveRawHTML(dir, pg.RawHTML); err != nil {
		s.log.Warnw("save raw html failed",
			"page_id", pg.ConfluenceID,
			"title", pg.Title,
			"error", err)
	}
	pg.RawHTMLPath = dir + "/raw.html"

	bodyFormat := "rendered"
	if pg.isStorage() {
		bodyFormat = "storage"
	}
	meta := &storage.Metadata{
		Title:         pg.Title,
		ConfluenceURL: pg.URL,
		SpaceKey:      spaceKey,
		Author:        pg.History.CreatedBy,
		CreatedAt:     derefTime(pg.History.CreatedAt),
		UpdatedBy:     pg.History.UpdatedBy,
		UpdatedAt:     derefTime(pg.History.UpdatedAt),
		Images:        pg.Images,
		Diagrams:      csfDiagrams,
		Attachments:   pg.Attachments,
		BodyFormat:    bodyFormat,
		SavedAt:       time.Now(),
	}
	if pg.ParentID != nil {
		meta.ParentTitle = ""
	}
	if err := s.storage.SaveMetadata(dir, meta); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	pg.MetadataPath = dir + "/metadata.json"

	var space *store.Space
	space, err = s.db.GetSpaceByKey(context.Background(), spaceKey)
	if err != nil {
		s.log.Infow("space not found, auto-creating", "space_key", spaceKey)
		sURL := spaceURL
		if sURL == "" {
			sURL = confluence.DeriveSpaceURL(pg.URL, spaceKey)
		}
		if sURL == "" {
			s.log.Warnw("failed to auto-create space: no space or page URL to derive from",
				"space_key", spaceKey)
			return nil
		}
		spaceID, err := s.db.CreateSpace(context.Background(), spaceKey, spaceKey, sURL)
		if err != nil {
			s.log.Warnw("failed to auto-create space, skipping db save",
				"space_key", spaceKey, "error", err)
			return nil
		}
		space = &store.Space{ID: spaceID, Key: spaceKey, Name: spaceKey, URL: sURL}
	}

	var parentID *int
	if pg.ParentID != nil && *pg.ParentID > 0 {
		parentID = pg.ParentID
	}

	dbPage := &store.Page{
		SpaceID:            space.ID,
		ConfluenceID:       pg.ConfluenceID,
		Version:            pg.Version,
		Title:              pg.Title,
		ParentConfluenceID: parentID,
		Content:            contentMD,
		HTMLPath:           pg.HTMLPath,
		RawHTMLPath:        dir + "/raw.html",
		MetadataPath:       dir + "/metadata.json",
		FileDir:            dir,
		BodyFormat:         bodyFormat,
		History:            pg.History,
	}

	if err := s.db.UpsertPage(s.ctx, dbPage); err != nil {
		s.log.Warnw("save page to db failed",
			"page_id", pg.ConfluenceID,
			"title", pg.Title,
			"error", err)
	}

	if err := s.db.UpdateSpaceLastCrawled(s.ctx, spaceKey); err != nil {
		s.log.Warnw("failed to update space last crawled",
			"space_key", spaceKey,
			"error", err)
	}

	return nil
}

// isStorage reports whether the page body is Confluence Storage Format,
// trusting BodyFormat and sniffing RawHTML only when it is unset.
func (pg *Page) isStorage() bool {
	if pg.BodyFormat != "" {
		return pg.BodyFormat == "storage"
	}
	return csf.IsStorageFormat(pg.RawHTML)
}

// renderMarkdown produces the page's Markdown, routing by input format: pages
// whose raw body is Confluence Storage Format go through the rule-based CSF
// converter; browser-fallback rendered HTML uses the generic converter.
func (s *Scraper) renderMarkdown(pg *Page, spaceURL string, cloud bool) (string, []csf.AssetRequest, error) {
	if pg.isStorage() {
		ctx := &csf.RenderContext{
			PageID:  pg.ConfluenceID,
			BaseURL: extractConfluenceBaseURL(spaceURL),
			Cloud:   cloud,
			Files:   s.fileLinks(pg.Files),

			ResolvePageID: pg.resolvePageID,
			UserName:      s.userName,
		}
		md, assets, err := csf.CSFToMarkdown(pg.RawHTML, ctx)
		if err != nil {
			return "", nil, fmt.Errorf("csf to markdown: %w", err)
		}
		return md, assets, nil
	}
	md, err := contentmd.HTMLToMarkdown(pg.CleanHTML)
	return md, nil, err
}

// downloadCSFAssets fetches the assets a CSF conversion requested into the
// page directory and returns their metadata refs, split by kind (images vs
// draw.io diagrams). Failures are logged and skipped (best-effort, like the
// HTML image path); the emitted Markdown alt text still degrades gracefully.
func (s *Scraper) downloadCSFAssets(dir string, reqs []csf.AssetRequest) (images, diagrams []storage.AssetRef) {
	for _, r := range reqs {
		if r.URL == "" || r.Filename == "" {
			continue
		}
		sub := "images"
		if r.Kind == "diagram" {
			sub = "diagrams"
		}
		rel := filepath.Join("assets", sub, r.Filename)
		if err := s.assets.DownloadAs(filepath.Join(dir, rel), r.URL); err != nil {
			s.log.Warnw("csf asset download failed", "url", r.URL, "kind", r.Kind, "error", err)
			continue
		}
		ref := storage.AssetRef{OriginalURL: r.URL, LocalPath: rel}
		if r.Kind == "diagram" {
			diagrams = append(diagrams, ref)
		} else {
			images = append(images, ref)
		}
	}
	return images, diagrams
}

// browserPageTimeout bounds one browser page load (open, load, settle, read
// HTML). Without it a page that never settles hangs the whole crawl.
var browserPageTimeout = 60 * time.Second

// browserPageHTML opens url in a new tab, waits for it to load and settle,
// and returns its HTML. It fails after browserPageTimeout instead of hanging,
// returns rod errors instead of panicking, and always closes the tab.
func (s *Scraper) browserPageHTML(url string) (html string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("browser page %s: %v", url, r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), browserPageTimeout)
	defer cancel()
	tab, err := s.browser.Context(ctx).Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return "", fmt.Errorf("browser open page %s: %w", url, err)
	}
	// Close with the browser's own context: ctx may have expired by now.
	defer tab.Context(s.browser.GetContext()).Close() //nolint:errcheck // best-effort tab cleanup
	page := tab.Context(ctx)
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("browser load page %s: %w", url, err)
	}
	if err := page.WaitStable(time.Second); err != nil {
		return "", fmt.Errorf("browser wait for page %s: %w", url, err)
	}
	html, err = page.HTML()
	if err != nil {
		return "", fmt.Errorf("get html: %w", err)
	}
	return html, nil
}

// ScrapePage scrapes a single page using a headless browser (legacy fallback).
func (s *Scraper) ScrapePage(pg *Page, spaceKey, spaceURL string) error {
	baseURL := extractConfluenceBaseURL(spaceURL)

	html, err := s.browserPageHTML(pg.URL)
	if err != nil {
		return err
	}

	if isErrorPage(html) {
		return fmt.Errorf("browser got an error page instead of content (url %s)", pg.URL)
	}
	pg.RawHTML = html
	pg.BodyFormat = "rendered"

	if s.log.Enabled() {
		s.log.Debugw("page content captured via browser",
			"url", pg.URL,
			"html_size", len(html))
	}

	cleanHTML, images, attachments, err := s.extractContent(html, pg.Title, baseURL)
	if err != nil {
		return fmt.Errorf("extract content: %w", err)
	}

	pg.CleanHTML = cleanHTML
	pg.Images = images
	pg.Attachments = attachments

	// Browser fallback yields rendered HTML (not CSF); flavor is irrelevant.
	return s.savePageMetadata(pg, spaceKey, spaceURL, false)
}
