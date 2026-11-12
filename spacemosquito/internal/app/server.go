package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vkh/spacemosquito/internal/api"
	"github.com/vkh/spacemosquito/internal/bootstrap"
	"github.com/vkh/spacemosquito/internal/browse"
	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/cron"
	"github.com/vkh/spacemosquito/internal/datastore"
	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/mcp"
	"github.com/vkh/spacemosquito/internal/paths"
	"github.com/vkh/spacemosquito/internal/scraper"
	"github.com/vkh/spacemosquito/internal/search"
	"github.com/vkh/spacemosquito/internal/session"
	"github.com/vkh/spacemosquito/internal/storage"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logger"
	"github.com/vkh/spacemosquito/pkg/logging"
	"go.uber.org/zap"
)

func New() (*zap.Logger, *config.Config, error) {
	log, err := logger.NewProduction(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create logger: %w", err)
	}

	log.Info("initializing SpaceMosquito")

	cfgPath, err := paths.ResolveConfig()
	if err != nil {
		log.Fatal("failed to resolve config path", zap.Error(err))
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal("failed to load config", zap.Error(err))
	}
	if err := paths.NormalizeConfig(cfg); err != nil {
		log.Fatal("failed to normalize config paths", zap.Error(err))
	}

	// Also log to a file (config log_file), so the log outlives the terminal.
	if dataDir, err := paths.ResolveDataDir(); err == nil {
		if file := cfg.ServeLogFile(dataDir); file != "" {
			fileLog, err := logger.NewProduction(&logger.Config{Level: "info", Format: "json", File: file})
			if err != nil {
				log.Warn("log file unavailable, logging to the terminal only", zap.String("file", file), zap.Error(err))
			} else {
				log = fileLog
				log.Info("logging to file", zap.String("file", file))
			}
		}
	}

	return log, cfg, nil
}

type serverComponents struct {
	database        store.Store
	cfg             *config.Config
	sessionStore    *session.Store
	storageWriter   *storage.Writer
	assetDownloader *storage.AssetDownloader
	scraperInstance *scraper.Scraper
	mcpServer       *mcp.Server
	cronManager     *cron.Manager
	cronScheduler   *cron.Scheduler
	semantic        *embed.Semantic
	mux             *http.ServeMux
	log             *zap.Logger
}

func setupComponents(cfg *config.Config, log *zap.Logger) (*serverComponents, error) {
	migrationsPath, err := paths.ResolveMigrations()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve migrations path: %w", err)
	}

	sugar := logging.New("db", log)
	if err := datastore.MigrateUp(cfg, migrationsPath, sugar); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	database, err := datastore.Open(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if _, err := bootstrap.BackfillPageBodyFormats(context.Background(), database, sugar); err != nil {
		log.Warn("body_format backfill failed", zap.Error(err))
	}

	sessionStore := session.NewStore(cfg.Session.FilePath, logging.New("session", log))

	storageWriter := storage.NewWriter(cfg.Storage.BasePath, logging.New("storage", log))
	assetDownloader := storage.NewAssetDownloader(logging.New("assets", log))
	assetDownloader.SetDelay(cfg.Storage.AssetDelay())
	scraperInstance := scraper.New(cfg, database, storageWriter, assetDownloader, logging.New("scraper", log))

	searchHandler := api.NewSearchHandler(database, sessionStore, cfg, logging.New("search", log))

	crawlJobManager := scraper.NewJobManager(cfg, database, sessionStore, storageWriter, assetDownloader, logging.New("crawl", log))

	// Semantic search (ADR-019): off by config, or keyword search only when
	// the helper, libraries or model are not installed.
	semantic, err := openSemantic(cfg, database, log)
	if err != nil {
		log.Warn("semantic search unavailable; keyword search only", zap.Error(err))
	}
	var queryEmbedder embed.Embedder // stays a nil interface when semantic search is off
	if semantic != nil {
		crawlJobManager.PageSaved = semantic.Background.Notify
		queryEmbedder = semantic.Embedder
	}
	searchService := search.NewService(database, queryEmbedder, logging.New("search", log))
	if semantic != nil {
		semantic.Indexer.OnChange = searchService.Invalidate
	}
	searchHandler.Service = searchService
	crawlHandler := api.NewCrawlHandler(crawlJobManager, cfg, logging.New("crawl_api", log))

	mcpServer := mcp.New(database, sessionStore, cfg, logging.New("mcp", log))
	mcpServer.Search = searchService
	mcp.ServerInstance = mcpServer

	cronConfigPath, err := paths.ResolveCronConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve cron config path: %w", err)
	}
	cronManager := cron.NewManager(cronConfigPath, logging.New("cron_config", log))

	cronScheduler := cron.NewScheduler(cfg, cronManager, database, sessionStore, storageWriter, assetDownloader, logging.New("cron", log))
	cronAPI := api.NewCronAPIHandler(cfg, cronManager, cronScheduler, logging.New("cron_api", log))
	cronSpaceAPI := api.NewCronSpaceHandler(cronManager, cronScheduler, logging.New("cron_space", log))

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /api/session", api.New(sessionStore, cfg, logging.New("api", log)).CreateSession)
	mux.HandleFunc("DELETE /api/session", api.New(sessionStore, cfg, logging.New("api", log)).DeleteSession)
	mux.HandleFunc("GET /api/session/status", api.New(sessionStore, cfg, logging.New("api", log)).SessionStatus)
	mux.HandleFunc("POST /api/session/validate", api.New(sessionStore, cfg, logging.New("api", log)).ValidateSession)
	mux.HandleFunc("GET /api/search", searchHandler.Search)
	mux.HandleFunc("POST /api/search/reindex", searchHandler.Reindex)
	mux.HandleFunc("GET /api/search/stats", searchHandler.Stats)
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		mcpServer.HandleRequest(w, r)
	})
	mux.HandleFunc("POST /api/crawl", crawlHandler.Create)
	mux.HandleFunc("GET /api/crawl/status", crawlHandler.Status)
	mux.HandleFunc("GET /api/crawl", crawlHandler.List)
	mux.HandleFunc("POST /api/crawl/cancel", crawlHandler.Cancel)
	mux.HandleFunc("POST /api/crawl/cleanup", crawlHandler.Cleanup)

	mux.HandleFunc("GET /api/cron", cronAPI.List)
	mux.HandleFunc("POST /api/cron/start", cronAPI.StartNow)
	mux.HandleFunc("GET /api/cron/config", cronAPI.ConfigGet)
	mux.HandleFunc("POST /api/cron/config", cronAPI.ConfigUpdate)
	mux.HandleFunc("POST /api/cron/reload", cronAPI.ConfigReload)

	mux.HandleFunc("POST /api/cron/space/{key}", cronSpaceAPI.Update)
	mux.HandleFunc("GET /api/cron/space/{key}", cronSpaceAPI.ConfigGet)
	mux.HandleFunc("DELETE /api/cron/space/{key}", cronSpaceAPI.Delete)

	mux.HandleFunc("GET /api/spaces", api.SpacesHandler(database, cfg.Storage.BasePath, logging.New("spaces", log)))
	mux.HandleFunc("POST /api/spaces", api.SpacesHandler(database, cfg.Storage.BasePath, logging.New("spaces", log)))
	mux.HandleFunc("GET /api/spaces/{key}", api.SpaceByIDHandler(database, cfg.Storage.BasePath, cronManager, crawlJobManager, logging.New("spaces", log)))
	mux.HandleFunc("DELETE /api/spaces/{key}", api.SpaceByIDHandler(database, cfg.Storage.BasePath, cronManager, crawlJobManager, logging.New("spaces", log)))
	browseSite := &browse.Site{DB: database, SavedBase: cfg.Storage.BasePath, Log: logging.New("browse", log)}
	mux.Handle("GET "+browse.Prefix, browseSite.Handler())
	mux.HandleFunc("GET /api/spaces/{key}/pages", api.SpacePagesHandler(database, cfg, logging.New("spaces", log)))
	mux.HandleFunc("GET /api/pages/{confluence_id}", api.PageByConfluenceIDHandler(database, cfg, logging.New("pages", log)))
	mux.HandleFunc("GET /api/pages/{confluence_id}/compare", api.PageCompareHandler(database, sessionStore, cfg, logging.New("pages", log)))
	mux.HandleFunc("POST /api/pages/{confluence_id}/refresh", api.PageRefreshHandler(database, sessionStore, scraperInstance, cfg, logging.New("pages", log)))

	return &serverComponents{
		database:        database,
		cfg:             cfg,
		sessionStore:    sessionStore,
		storageWriter:   storageWriter,
		assetDownloader: assetDownloader,
		scraperInstance: scraperInstance,
		mcpServer:       mcpServer,
		cronManager:     cronManager,
		cronScheduler:   cronScheduler,
		semantic:        semantic,
		mux:             mux,
		log:             log,
	}, nil
}

func openSemantic(cfg *config.Config, db store.Store, log *zap.Logger) (*embed.Semantic, error) {
	dataDir, err := paths.ResolveDataDir()
	if err != nil {
		dataDir = ""
	}
	return embed.Open(cfg.Embedder, dataDir, db, logging.New("embed", log))
}

func (c *serverComponents) Start(ctx context.Context) error {
	if err := c.cronScheduler.Start(ctx); err != nil {
		c.log.Warn("failed to start cron scheduler", zap.Error(err))
	}
	bgCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	if c.semantic != nil {
		c.log.Info("semantic search on", zap.String("model", c.semantic.Paths.Model), zap.String("lib", c.semantic.Paths.Lib))
		go c.semantic.Background.Run(bgCtx)
	}

	loggingMux := api.LoggingMiddleware(c.mux, logging.New("http", c.log))
	corsMux := api.CORSMiddleware(loggingMux, logging.New("http", c.log))

	addr := fmt.Sprintf("%s:%d", c.cfg.MCP.Host, c.cfg.MCP.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: corsMux,
	}

	go func() {
		fmt.Printf("Server listening on %s\n", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			c.log.Fatal("server failed", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	c.log.Info("shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		c.log.Error("server shutdown failed", zap.Error(err))
	}

	c.scraperInstance.CloseBrowser()
	c.cronScheduler.Stop()
	stopBackground()
	c.semantic.Close()
	c.database.Close()

	c.log.Info("server stopped")
	return nil
}

func Run(ctx context.Context) error {
	log, cfg, err := New()
	if err != nil {
		return err
	}
	defer log.Sync()

	comps, err := setupComponents(cfg, log)
	if err != nil {
		return err
	}

	return comps.Start(ctx)
}
