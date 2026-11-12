package cliapp

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vkh/spacemosquito/internal/app"
	"github.com/vkh/spacemosquito/internal/bootstrap"
	"github.com/vkh/spacemosquito/internal/browse"
	"github.com/vkh/spacemosquito/internal/catalogindex"
	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/contentmd"
	"github.com/vkh/spacemosquito/internal/cron"
	"github.com/vkh/spacemosquito/internal/datastore"
	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/paths"
	"github.com/vkh/spacemosquito/internal/scraper"
	"github.com/vkh/spacemosquito/internal/search"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/internal/spaceops"
	"github.com/vkh/spacemosquito/internal/storage"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logger"
	"github.com/vkh/spacemosquito/pkg/logging"
	"go.uber.org/zap"
)

// Version is stamped at link time via -ldflags "-X github.com/vkh/spacemosquito/internal/cliapp.Version=...".
var Version = "dev"

// Run is the CLI entrypoint. args should include the program name (os.Args).
func Run(args []string) int {
	if len(args) < 2 {
		printUsage()
		return 1
	}

	if args[1] == "version" || args[1] == "--version" {
		fmt.Println(Version)
		checkForUpdate(loadConfigQuietly())
		return 0
	}

	log, err := logger.NewProduction(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logger: %v\n", err)
		return 1
	}
	defer log.Sync()

	cmd := args[1]
	if cmd == "init" {
		runInit(args[2:], log)
		return 0
	}

	cfgPath, err := paths.ResolveConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve config path: %v\n", err)
		return 1
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		return 1
	}
	if err := paths.NormalizeConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to normalize config paths: %v\n", err)
		return 1
	}
	if cmd != "serve" {
		// No network here: show what the last check found.
		printCachedUpdateNotice(cfg)
	}

	switch cmd {
	case "bootstrap":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito bootstrap import-saved [--from PATH] [--force] [--dry-run]")
			return 1
		}
		switch args[2] {
		case "import-saved":
			runBootstrapImportSaved(cfg, args[3:], log)
		default:
			fmt.Fprintf(os.Stderr, "unknown bootstrap subcommand: %s\n", args[2])
			return 1
		}
	case "migrate-down":
		runMigrateDown(cfg, log)
	case "save":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito save <url>")
			return 1
		}
		runSave(cfg, args[2], log)
	case "crawl":
		fs := flag.NewFlagSet("crawl", flag.ExitOnError)
		force := fs.Bool("force", false, "re-scrape all pages and re-download all assets")
		fs.SetOutput(os.Stderr)
		if err := fs.Parse(args[2:]); err != nil {
			return 1
		}
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito crawl [--force] <space-url>")
			return 1
		}
		runCrawl(cfg, fs.Arg(0), *force, log)
	case "crawl-page":
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito crawl-page <space-key> <confluence-id>")
			return 1
		}
		confluenceID, err := strconv.Atoi(args[3])
		if err != nil || confluenceID <= 0 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito crawl-page <space-key> <confluence-id>")
			fmt.Fprintln(os.Stderr, "confluence-id must be a positive integer")
			return 1
		}
		runCrawlPage(cfg, args[2], confluenceID, log)
	case "reindex":
		withContent, withEmbeddings, spaceKey := false, false, ""
		rest := args[2:]
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "--content":
				withContent = true
			case "--embeddings":
				withEmbeddings = true
			case "--space":
				if i+1 < len(rest) {
					i++
					spaceKey = rest[i]
				}
			}
		}
		runReindex(cfg, withContent, log)
		if withEmbeddings {
			if err := runEmbeddings(cfg, spaceKey, log); err != nil {
				fmt.Fprintf(os.Stderr, "embeddings: %v\n", err)
				return 1
			}
		}
	case "search":
		modeArg, rest := extractFlag(args[2:], "--mode")
		mode, modeErr := search.ParseMode(modeArg)
		query, spaceKey, limit, err := parseSearchArgs(rest)
		if err == nil {
			err = modeErr
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			fmt.Fprintln(os.Stderr, "usage: spacemosquito search <query> [space-key] [--limit N] [--mode hybrid|keyword|semantic]")
			return 1
		}
		runSearch(cfg, query, spaceKey, limit, mode, log)
	case "get-page":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito get-page <confluence_id> [space_key]")
			return 1
		}
		spaceKey := ""
		if len(args) >= 4 {
			spaceKey = args[3]
		}
		return runGetPage(cfg, args[2], spaceKey, log)
	case "stats":
		runStats(cfg, log)
	case "delete-space":
		fs := flag.NewFlagSet("delete-space", flag.ExitOnError)
		deleteFiles := fs.Bool("delete-files", false, "also delete saved/<space-key> files")
		if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito delete-space [--delete-files] <space-key>")
			return 1
		}
		return runDeleteSpace(cfg, fs.Arg(0), *deleteFiles, log)
	case "export-html":
		fs := flag.NewFlagSet("export-html", flag.ExitOnError)
		space := fs.String("space", "", "export only this space key")
		if err := fs.Parse(args[2:]); err != nil || fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito export-html [--space KEY] <dir>")
			return 1
		}
		return runExportHTML(cfg, fs.Arg(0), *space, log)
	case "cron":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: spacemosquito cron <list|config|run-now>")
			return 1
		}
		switch args[2] {
		case "list":
			runCronList(cfg, log)
		case "config":
			runCronConfig(cfg)
		case "run-now":
			runCronRunNow(cfg, log)
		default:
			fmt.Fprintf(os.Stderr, "unknown cron subcommand: %s\n", args[2])
			fmt.Fprintln(os.Stderr, "usage: spacemosquito cron <list|config|run-now>")
			return 1
		}
	case "serve":
		runServe(cfg, log)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		return 1
	}
	return 0
}

func runInit(args []string, log *zap.Logger) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "data directory (default ~/.spacemosquito)")
	encKey := fs.String("encryption-key", "", "session encryption key (auto-generated if omitted)")
	force := fs.Bool("force-config", false, "overwrite existing config.yaml")
	downloadBrowser := fs.Bool("download-browser", false, "pre-download Chromium into the data directory")
	bootstrapMode := fs.String("bootstrap-mode", "recrawl", "bootstrap mode: recrawl | import_saved")
	bootstrapFrom := fs.String("from", "", "saved directory to import from when bootstrap-mode=import_saved")
	bootstrapForce := fs.Bool("bootstrap-force", false, "allow import into a non-empty database")
	bootstrapDryRun := fs.Bool("bootstrap-dry-run", false, "scan/import report only without DB writes")
	_ = fs.Parse(args)

	result, err := paths.InitWorkspace(paths.InitOptions{
		DataDir:       *dataDir,
		EncryptionKey: *encKey,
		ForceConfig:   *force,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init failed: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(result.ConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if err := paths.NormalizeConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "failed to normalize config paths: %v\n", err)
		os.Exit(1)
	}

	migrationsPath, err := paths.ResolveMigrations()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve migrations path: %v\n", err)
		os.Exit(1)
	}

	sugar := logging.New("cli", log)
	if err := datastore.MigrateUp(cfg, migrationsPath, sugar); err != nil {
		sugar.Errorw("migration failed", "error", err)
		os.Exit(1)
	}

	if *downloadBrowser {
		if err := scraper.DownloadChromium(sugar); err != nil {
			sugar.Errorw("browser download failed", "error", err)
			os.Exit(1)
		}
	}

	mode := strings.TrimSpace(*bootstrapMode)
	if mode == "" {
		mode = "recrawl"
	}
	switch mode {
	case "recrawl":
		// no-op bootstrap path
	case "import_saved":
		from := strings.TrimSpace(*bootstrapFrom)
		if from == "" {
			from = cfg.Storage.BasePath
		}
		reportDir, err := paths.ResolveReports()
		if err != nil {
			sugar.Errorw("resolve reports directory failed", "error", err)
			os.Exit(1)
		}
		database, err := datastore.Open(cfg, log)
		if err != nil {
			sugar.Errorw("database open failed", "error", err)
			os.Exit(1)
		}
		defer database.Close()
		report, err := bootstrap.ImportSaved(context.Background(), database, bootstrap.Options{
			FromDir:   from,
			ReportDir: reportDir,
			Force:     *bootstrapForce,
			DryRun:    *bootstrapDryRun,
		}, sugar)
		if err != nil {
			sugar.Errorw("bootstrap import-saved failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Bootstrap import-saved complete: pages=%d spaces=%d skipped=%d errors=%d\n",
			report.ImportedPages, report.ImportedSpaces, len(report.Skipped), len(report.Errors))
	default:
		fmt.Fprintf(os.Stderr, "invalid --bootstrap-mode: %q (expected recrawl or import_saved)\n", mode)
		os.Exit(1)
	}

	fmt.Printf("Data directory: %s\n", result.DataDir)
	fmt.Printf("Config:         %s\n", result.ConfigPath)
	if result.ConfigCreated {
		fmt.Println("Created default config.yaml (sqlite)")
	}
	fmt.Printf("Session file:   %s\n", result.SessionPath)
	fmt.Println()
	fmt.Println("Init complete. Run the Firefox extension, then: spacemosquito serve")
}

func runBootstrapImportSaved(cfg *config.Config, args []string, log *zap.Logger) {
	fs := flag.NewFlagSet("bootstrap import-saved", flag.ExitOnError)
	from := fs.String("from", "", "directory containing saved pages (default config.storage.base_path)")
	force := fs.Bool("force", false, "allow import into non-empty database")
	dryRun := fs.Bool("dry-run", false, "scan and report only")
	_ = fs.Parse(args)

	sugar := logging.New("bootstrap", log)
	fromDir := strings.TrimSpace(*from)
	if fromDir == "" {
		fromDir = cfg.Storage.BasePath
	}
	reportDir, err := paths.ResolveReports()
	if err != nil {
		sugar.Errorw("resolve reports directory failed", "error", err)
		os.Exit(1)
	}

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("database open failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	report, err := bootstrap.ImportSaved(context.Background(), database, bootstrap.Options{
		FromDir:   fromDir,
		ReportDir: reportDir,
		Force:     *force,
		DryRun:    *dryRun,
	}, sugar)
	if err != nil {
		sugar.Errorw("import-saved failed", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Import complete from %s\n", report.From)
	fmt.Printf("Scanned: %d\n", report.ScannedFiles)
	fmt.Printf("Imported pages: %d\n", report.ImportedPages)
	fmt.Printf("Imported spaces: %d\n", report.ImportedSpaces)
	fmt.Printf("Deduplicated: %d\n", report.Deduplicated)
	fmt.Printf("Skipped: %d\n", len(report.Skipped))
	fmt.Printf("Errors: %d\n", len(report.Errors))
}

func runGetPage(cfg *config.Config, confluenceIDStr, spaceKey string, log *zap.Logger) int {
	sugar := logging.New("get-page", log)

	confluenceID, err := strconv.Atoi(confluenceIDStr)
	if err != nil || confluenceID <= 0 {
		fmt.Fprintln(os.Stderr, "invalid confluence_id")
		return 1
	}

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("database open failed", "error", err)
		return 1
	}
	defer database.Close()

	detail, err := search.GetPageDetail(context.Background(), database, confluenceID, spaceKey, cfg.MCP.ExposeInternalIDs)
	if err != nil {
		var ambiguous *store.AmbiguousPageError
		switch {
		case errors.As(err, &ambiguous):
			fmt.Fprintln(os.Stderr, ambiguous.Error())
			for _, c := range ambiguous.Candidates {
				fmt.Fprintf(os.Stderr, "  %s: %s\n", c.SpaceKey, c.Title)
			}
			return 2
		case errors.Is(err, store.ErrPageNotFound):
			fmt.Fprintln(os.Stderr, "page not found")
			return 1
		default:
			sugar.Errorw("get page failed", "error", err)
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(detail); err != nil {
		sugar.Errorw("encode failed", "error", err)
		return 1
	}
	return 0
}

func runMigrateDown(cfg *config.Config, log *zap.Logger) {
	migrationsPath, err := paths.ResolveMigrations()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve migrations path: %v\n", err)
		os.Exit(1)
	}

	sugar := logging.New("cli", log)
	if err := datastore.MigrateDown(cfg, migrationsPath, sugar); err != nil {
		sugar.Errorw("migration rollback failed", "error", err)
		os.Exit(1)
	}
	sugar.Info("migration rolled back")
}

func runSave(cfg *config.Config, pageURL string, log *zap.Logger) {
	sugar := logging.New("cli", log)
	w := storage.NewWriter(cfg.Storage.BasePath, sugar)

	spaceKey := "unknown"
	pageTitle := "untitled"

	dir, err := w.MakePageDir(spaceKey, pageTitle, 0) // stub: no Confluence ID
	if err != nil {
		sugar.Errorw("failed to create page dir", "error", err)
		os.Exit(1)
	}

	meta := &storage.Metadata{
		Title:         pageTitle,
		ConfluenceURL: pageURL,
		SpaceKey:      spaceKey,
		SavedAt:       time.Now(),
	}

	if err := w.SaveMetadata(dir, meta); err != nil {
		sugar.Errorw("failed to save metadata", "error", err)
		os.Exit(1)
	}

	sugar.Infow("page saved", "path", dir, "url", pageURL)
}

func runServe(cfg *config.Config, log *zap.Logger) {
	_ = cfg
	_ = log
	fmt.Println("Starting SpaceMosquito server...")
	go checkForUpdate(cfg)
	if err := app.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func runCrawl(cfg *config.Config, spaceURL string, force bool, log *zap.Logger) {
	requestID := fmt.Sprintf("crawl-%d", time.Now().Unix())
	sugar := logging.New("crawl", log)
	sugar.Infow("crawl command initiated",
		"space_url", spaceURL,
		"force", force,
		"request_id", requestID)

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	store := session.NewStore(cfg.Session.FilePath, sugar)
	if !store.HasSession() {
		sugar.Errorw("no session found — run the Firefox extension to capture cookies first")
		os.Exit(1)
	}

	encKey := cfg.Session.EncryptionKey
	if encKey == "" {
		sugar.Errorw("encryption key not configured")
		os.Exit(1)
	}

	sess, err := store.GetForURL(encKey, spaceURL)
	if err != nil {
		sugar.Errorw("failed to load session for space", "space_url", spaceURL, "error", err)
		os.Exit(1)
	}

	sugar.Infow("session loaded",
		"cookie_count", len(sess.Cookies),
		"confluence_url", sess.ConfluenceURL)

	storageWriter := storage.NewWriter(cfg.Storage.BasePath, sugar)
	assetDownloader := storage.NewAssetDownloader(sugar)
	assetDownloader.SetDelay(cfg.Storage.AssetDelay())
	assetDownloader.SetForce(force)

	s := scraper.New(cfg, database, storageWriter, assetDownloader, sugar)
	s.SetForce(force)

	if err := s.CrawlSpace(spaceURL, sess); err != nil {
		sugar.Errorw("crawl failed", "error", err)
		os.Exit(1)
	}

	sugar.Infow("crawl completed successfully", "request_id", requestID)

	fmt.Println()
	fmt.Println("=== Crawl Summary ===")
	fmt.Println()
}

func runCrawlPage(cfg *config.Config, spaceKey string, confluenceID int, log *zap.Logger) {
	requestID := fmt.Sprintf("crawl-page-%d", time.Now().Unix())
	sugar := logging.New("crawl-page", log)
	sugar.Infow("crawl-page command initiated",
		"space_key", spaceKey,
		"confluence_id", confluenceID,
		"request_id", requestID)

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	store := session.NewStore(cfg.Session.FilePath, sugar)
	if !store.HasSession() {
		sugar.Errorw("no session found — run the Firefox extension to capture cookies first")
		os.Exit(1)
	}

	encKey := cfg.Session.EncryptionKey
	if encKey == "" {
		sugar.Errorw("encryption key not configured")
		os.Exit(1)
	}

	ctx := context.Background()
	space, err := database.GetSpaceByKey(ctx, spaceKey)
	if err != nil {
		sugar.Errorw("space not found — crawl the space once first", "space_key", spaceKey, "error", err)
		os.Exit(1)
	}
	if space.URL == "" {
		sugar.Errorw("space has no URL — cannot select session host", "space_key", spaceKey)
		os.Exit(1)
	}

	sess, err := store.GetForURL(encKey, space.URL)
	if err != nil {
		sugar.Errorw("failed to load session for space", "space_url", space.URL, "error", err)
		os.Exit(1)
	}

	sugar.Infow("session loaded",
		"cookie_count", len(sess.Cookies),
		"confluence_url", sess.ConfluenceURL)

	storageWriter := storage.NewWriter(cfg.Storage.BasePath, sugar)
	assetDownloader := storage.NewAssetDownloader(sugar)
	assetDownloader.SetDelay(cfg.Storage.AssetDelay())

	s := scraper.New(cfg, database, storageWriter, assetDownloader, sugar)

	if err := s.CrawlPage(spaceKey, confluenceID, sess); err != nil {
		sugar.Errorw("crawl-page failed", "error", err)
		fmt.Fprintf(os.Stderr, "crawl-page failed: %v\n", err)
		os.Exit(1)
	}

	sugar.Infow("crawl-page completed successfully", "request_id", requestID)
	fmt.Printf("Refreshed page %d in space %s\n", confluenceID, spaceKey)
}

func runReindex(cfg *config.Config, withContent bool, log *zap.Logger) {
	sugar := logging.New("reindex", log)

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	ctx := context.Background()

	if withContent {
		savedBase, err := paths.ResolveSaved(cfg)
		if err != nil {
			sugar.Errorw("resolve saved path failed", "error", err)
			os.Exit(1)
		}
		result, err := contentmd.ReindexAll(ctx, database, savedBase)
		if err != nil {
			sugar.Errorw("content reindex failed", "error", err)
			os.Exit(1)
		}
		sugar.Infow("content reindexed",
			"updated", result.Updated,
			"skipped", result.Skipped,
			"errors", len(result.Errors))
		fmt.Printf("Content reindexed: %d updated, %d skipped\n", result.Updated, result.Skipped)
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "  error: %s\n", e)
		}
		if err := catalogindex.WriteAll(ctx, database, savedBase); err != nil {
			sugar.Errorw("catalog index failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Catalog index written: %s\n", filepath.Join(savedBase, "index.md"))
		// Pages are browsed through serve (ADR-018); drop HTML that older
		// versions generated into the catalog.
		removed, err := catalogindex.RemoveLegacyHTML(savedBase)
		if err != nil {
			sugar.Warnw("remove generated html failed", "error", err)
		}
		if removed > 0 {
			fmt.Printf("Removed %d generated HTML files (pages are rendered by serve; see export-html)\n", removed)
		}
	}

	if err := database.IndexAllPageContents(ctx); err != nil {
		sugar.Errorw("reindex failed", "error", err)
		os.Exit(1)
	}

	sugar.Info("all pages reindexed successfully")
	if withContent {
		fmt.Println("All pages reindexed successfully (content + FTS)")
	} else {
		fmt.Println("All pages reindexed successfully")
	}
}

func parseSearchArgs(args []string) (query, spaceKey string, limit int, err error) {
	limit = 10
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit":
			if i+1 >= len(args) {
				return "", "", 0, fmt.Errorf("--limit requires a value")
			}
			n, parseErr := strconv.Atoi(args[i+1])
			if parseErr != nil || n <= 0 {
				return "", "", 0, fmt.Errorf("invalid --limit value %q", args[i+1])
			}
			limit = n
			i++
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) < 1 {
		return "", "", 0, fmt.Errorf("missing search query")
	}
	return positional[0], optionalString(positional, 1), limit, nil
}

func optionalString(ss []string, idx int) string {
	if idx < len(ss) {
		return ss[idx]
	}
	return ""
}

// extractFlag removes "name value" from args and returns the value.
func extractFlag(args []string, name string) (string, []string) {
	var rest []string
	value := ""
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			value = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return value, rest
}

func runSearch(cfg *config.Config, query, spaceKey string, limit int, mode search.Mode, log *zap.Logger) {
	sugar := logging.New("search", log)

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Semantic and hybrid search start the embedding helper (model load
	// takes a few seconds); without it, search is keyword only.
	var emb embed.Embedder
	if mode != search.ModeKeyword {
		dataDir, _ := paths.ResolveDataDir()
		sem, err := embed.Open(cfg.Embedder, dataDir, database, logging.New("embed", log))
		if err != nil {
			sugar.Warnw("semantic search unavailable", "error", err)
		}
		if sem != nil {
			defer sem.Close()
			emb = sem.Embedder
		}
	}
	res, err := search.NewService(database, emb, sugar).Search(context.Background(),
		search.Request{Query: query, SpaceKey: spaceKey, Limit: limit, Mode: mode})
	if err != nil {
		sugar.Errorw("search failed", "query", query, "error", err)
		os.Exit(1)
	}
	results := res.Results

	if results == nil {
		fmt.Println("No results found")
		return
	}

	hits := search.ToSearchHits(results, cfg.MCP.ExposeInternalIDs)

	fmt.Printf("\n=== Search Results for '%s' ===\n", query)
	if spaceKey != "" {
		fmt.Printf("Space: %s\n", spaceKey)
	}
	fmt.Printf("Total: %d results (%s search)\n\n", len(hits), res.Mode)

	for i, r := range hits {
		fmt.Printf("%d. %s (Space: %s, ID: %d)\n", i+1, r.Title, r.SpaceKey, r.ConfluenceID)
		if r.Match != "" {
			fmt.Printf("   Score: %.4f, match: %s\n", r.Similarity, r.Match)
		} else {
			fmt.Printf("   Similarity: %.4f\n", r.Similarity)
		}
		if r.HeadingPath != "" {
			fmt.Printf("   Section: %s\n", r.HeadingPath)
		}
		if r.Excerpt != "" {
			fmt.Printf("   Excerpt: %s\n", r.Excerpt)
		}
		fmt.Println()
	}
}

func runStats(cfg *config.Config, log *zap.Logger) {
	sugar := logging.New("stats", log)

	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("stats failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	stats, err := database.GetPageStats(context.Background())
	if err != nil {
		sugar.Errorw("stats failed", "error", err)
		os.Exit(1)
	}

	fmt.Println("\n=== SpaceMosquito Statistics ===")
	fmt.Printf("Total Spaces: %d\n", stats.TotalSpaces)
	fmt.Printf("Total Pages: %d\n", stats.TotalPages)
	fmt.Printf("Content Indexing: %s\n", stats.ContentLang)
	fmt.Printf("Last Crawled: %s\n", stats.LastCrawledStr)
	dataDir, _ := paths.ResolveDataDir()
	sem, err := embed.Open(cfg.Embedder, dataDir, database, sugar)
	switch {
	case err != nil:
		fmt.Printf("Semantic Search: unavailable (%v)\n", err)
	case sem == nil:
		fmt.Println("Semantic Search: off (embedder.provider: none)")
	default:
		r := sem.Report(context.Background(), database)
		if r.Error != "" {
			fmt.Printf("Semantic Search: %s, not indexed yet (%s)\n", r.Model, r.Error)
		} else {
			fmt.Printf("Semantic Search: %s, %d / %d pages embedded (%d chunks)\n", r.Model, r.EmbeddedPages, r.Pages, r.Chunks)
		}
	}
	fmt.Println()
}

// runDeleteSpace removes a space via spaceops.Delete. It cannot see crawls
// running inside `serve`; stop those first (the REST delete checks).
func runDeleteSpace(cfg *config.Config, spaceKey string, deleteFiles bool, log *zap.Logger) int {
	database, err := datastore.Open(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		return 1
	}
	defer database.Close()

	savedBase, err := paths.ResolveSaved(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve saved path failed: %v\n", err)
		return 1
	}
	cronPath, err := paths.ResolveCronConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve cron config path failed: %v\n", err)
		return 1
	}
	cronConfigs := cron.NewManager(cronPath, logging.New("cron_config", log))

	res, err := spaceops.Delete(context.Background(), database, savedBase, cronConfigs, spaceKey, spaceops.DeleteOptions{DeleteFiles: deleteFiles})
	if !res.Deleted {
		fmt.Fprintf(os.Stderr, "delete space %s: %v\n", spaceKey, err)
		return 1
	}
	fmt.Printf("Deleted space %s (%d pages removed from catalog and search)\n", spaceKey, res.Pages)
	switch {
	case res.FilesDeleted:
		fmt.Printf("Deleted saved files: %s\n", res.FilesDir)
	case res.FilesDir != "":
		fmt.Printf("Saved files kept: %s (use --delete-files to remove)\n", res.FilesDir)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	return 0
}

func runCronList(cfg *config.Config, log *zap.Logger) {
	sugar := logging.New("cron", log)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/cron", cfg.MCP.Port))
	if err != nil {
		sugar.Errorw("failed to connect to server", "error", err)
		fmt.Fprintln(os.Stderr, "Cannot connect to server. Is it running?")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Server returned status %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var jobs []struct {
		ID       string    `json:"ID"`
		NextRun  time.Time `json:"NextRun"`
		LastRun  time.Time `json:"LastRun"`
		Disabled bool      `json:"Disabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		sugar.Errorw("failed to decode response", "error", err)
		os.Exit(1)
	}

	fmt.Println("\n=== Scheduled Crawl Jobs ===")
	if len(jobs) == 0 {
		fmt.Println("No jobs configured")
		return
	}
	for _, j := range jobs {
		status := "active"
		if j.Disabled {
			status = "disabled"
		}
		fmt.Printf("  %s [%s]\n", j.ID, status)
		if !j.NextRun.IsZero() {
			fmt.Printf("    Next run: %s\n", j.NextRun.Format(time.RFC3339))
		}
		if !j.LastRun.IsZero() {
			fmt.Printf("    Last run: %s\n", j.LastRun.Format(time.RFC3339))
		}
	}
	fmt.Println()
}

func runCronConfig(cfg *config.Config) {
	fmt.Println("\n=== Cron Configuration ===")
	if cfg.Cron.FullCrawl == nil && cfg.Cron.Incremental == nil {
		fmt.Println("Cron is not configured")
		return
	}

	if cfg.Cron.FullCrawl != nil {
		fmt.Println("\nFull Crawl:")
		fmt.Printf("  Enabled:    %v\n", cfg.Cron.FullCrawl.Enabled)
		fmt.Printf("  Interval:   %s\n", cfg.Cron.FullCrawl.Interval)
		fmt.Printf("  Max Duration: %s\n", cfg.Cron.FullCrawl.MaxDuration)
		fmt.Printf("  Spaces:\n")
		for _, sp := range cfg.Cron.FullCrawl.Spaces {
			fmt.Printf("    - %s\n", sp)
		}
	}

	if cfg.Cron.Incremental != nil {
		fmt.Println("\nIncremental Scan:")
		fmt.Printf("  Enabled:    %v\n", cfg.Cron.Incremental.Enabled)
		fmt.Printf("  Interval:   %s\n", cfg.Cron.Incremental.Interval)
		fmt.Printf("  Max Duration: %s\n", cfg.Cron.Incremental.MaxDuration)
		fmt.Printf("  Detection:  %s\n", cfg.Cron.Incremental.Detection)
		fmt.Printf("  Spaces:\n")
		for _, sp := range cfg.Cron.Incremental.Spaces {
			fmt.Printf("    - %s\n", sp)
		}
	}
	fmt.Println()
}

func runCronRunNow(cfg *config.Config, log *zap.Logger) {
	sugar := logging.New("cron", log)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://localhost:%d/api/cron/start", cfg.MCP.Port), "application/json", nil)
	if err != nil {
		sugar.Errorw("failed to connect to server", "error", err)
		fmt.Fprintln(os.Stderr, "Cannot connect to server. Is it running?")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Server returned status %d\n", resp.StatusCode)
		os.Exit(1)
	}

	fmt.Println("Jobs triggered successfully")
}

func printUsage() {
	fmt.Println("Usage: spacemosquito <command> [arguments]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init           Create data dir, config, and run migrations")
	fmt.Println("    --data-dir <path>         Data directory (default ~/.spacemosquito)")
	fmt.Println("    --encryption-key <key>    Session key (auto-generated if omitted)")
	fmt.Println("    --download-browser        Pre-download Chromium for offline use")
	fmt.Println("    --bootstrap-mode <mode>   Bootstrap mode: recrawl | import_saved")
	fmt.Println("    --from <path>             Saved path for import_saved mode")
	fmt.Println("    --bootstrap-force         Allow import into non-empty DB")
	fmt.Println("    --bootstrap-dry-run       Scan/report only for import_saved mode")
	fmt.Println("  bootstrap import-saved      Import pages from saved/ into DB")
	fmt.Println("    --from <path>             Saved directory (default config storage path)")
	fmt.Println("    --force                   Allow import into non-empty DB")
	fmt.Println("    --dry-run                 Scan and report only")
	fmt.Println("  migrate-down   Rollback last migration")
	fmt.Println("  save <url>     Save a Confluence page")
	fmt.Println("  crawl [--force] <url>  Crawl a Confluence space (--force re-scrapes all pages and assets)")
	fmt.Println("  crawl-page <space-key> <id>  Refresh one page (always overwrite text + assets)")
	fmt.Println("  search <q>     Search pages (optional: <space-key> [--limit N])")
	fmt.Println("  get-page <id>  Get page by Confluence ID (optional: <space-key>)")
	fmt.Println("  reindex        Rebuild FTS indexes (--content regenerates Markdown too;")
	fmt.Println("                 --embeddings [--space KEY] updates the semantic index now)")
	fmt.Println("  delete-space [--delete-files] <space-key>  Remove a space from the catalog (saved files kept unless --delete-files)")
	fmt.Println("  export-html [--space KEY] <dir>  Write the catalog as static HTML for browsing without serve")
	fmt.Println("  stats          Show database statistics")
	fmt.Println("  cron list      List scheduled crawl jobs")
	fmt.Println("  cron config    Show cron configuration")
	fmt.Println("  cron run-now   Trigger all jobs immediately")
	fmt.Println("  serve          Start the API and MCP server")
	fmt.Println("  version        Print binary version")
	fmt.Println()
	fmt.Println("Environment:")
	fmt.Println("  SPACEMOSQUITO_DATA_DIR       Override default data directory")
	fmt.Println("  CONFIG_PATH                  Override config file path")
	fmt.Println("  SPACEMOSQUITO_MIGRATIONS_DIR Override migrations directory (dev)")
	fmt.Println("  MIGRATIONS_PATH              Override migrations root directory")
	fmt.Println("  CRON_CONFIG_PATH             Override cron overrides file")
	fmt.Println("  CHROMIUM_PATH                Override Chromium/Chrome executable")
}

func runExportHTML(cfg *config.Config, outDir, spaceKey string, log *zap.Logger) int {
	sugar := logging.New("export", log)
	database, err := datastore.Open(cfg, log)
	if err != nil {
		sugar.Errorw("failed to connect to database", "error", err)
		return 1
	}
	defer database.Close()
	savedBase, err := paths.ResolveSaved(cfg)
	if err != nil {
		sugar.Errorw("resolve saved path failed", "error", err)
		return 1
	}
	site := &browse.Site{DB: database, SavedBase: savedBase, Log: sugar}
	res, err := site.Export(context.Background(), outDir, spaceKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "export failed: %v\n", err)
		return 1
	}
	abs, _ := filepath.Abs(outDir)
	fmt.Printf("Exported %d pages in %d spaces. Open %s\n", res.Pages, res.Spaces, filepath.Join(abs, "index.html"))
	return 0
}
