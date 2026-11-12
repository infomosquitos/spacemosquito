package cliapp

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/datastore"
	"github.com/vkh/spacemosquito/internal/embed"
	"github.com/vkh/spacemosquito/internal/paths"
	"github.com/vkh/spacemosquito/internal/search"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// runEmbeddings brings the semantic index up to date in the foreground
// (serve does the same in the background).
func runEmbeddings(cfg *config.Config, spaceKey string, log *zap.Logger) error {
	if !cfg.Embedder.Enabled() {
		return fmt.Errorf("semantic search is off (embedder.provider: none)")
	}
	// The semantic index tables arrive with a migration; serve applies it
	// too, but this may run first after an upgrade.
	migrationsPath, err := paths.ResolveMigrations()
	if err != nil {
		return err
	}
	if err := datastore.MigrateUp(cfg, migrationsPath, logging.New("cli", log)); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	database, err := datastore.Open(cfg, log)
	if err != nil {
		return err
	}
	defer database.Close()
	dataDir, _ := paths.ResolveDataDir()
	sem, err := embed.Open(cfg.Embedder, dataDir, database, logging.New("embed", log))
	if err != nil {
		return err
	}
	defer sem.Close()

	ctx := context.Background()
	start := time.Now()
	fmt.Println("Loading the embedding model…")
	info, err := sem.Embedder.Info(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Model %s (%s, %d dims, GPU: %v)\n", info.Model, info.Description, info.Dims, info.GPU)
	res, err := sem.Indexer.Backfill(ctx, spaceKey, func(done, total int) {
		fmt.Printf("\rEmbedding pages: %d / %d", done, total)
		if done == total {
			fmt.Println()
		}
	})
	if err != nil {
		return err
	}
	fmt.Printf("Semantic index: %d pages embedded (%d chunks), %d failed, %d up to date, in %s\n",
		res.Indexed, res.Chunks, res.Failed, res.UpToDate, time.Since(start).Round(time.Second))
	return nil
}

// runEvalSearch scores keyword, semantic and hybrid search on a YAML list
// of questions with the Confluence IDs that answer them (ADR-019):
//
//   - question: how do we import prices from SAP?
//     expect: [276857181]
//     space: SCCPI   # optional
func runEvalSearch(cfg *config.Config, file string, k int, log *zap.Logger) error {
	cases, err := search.LoadEvalCases(file)
	if err != nil {
		return err
	}
	database, err := datastore.Open(cfg, log)
	if err != nil {
		return err
	}
	defer database.Close()
	dataDir, _ := paths.ResolveDataDir()
	sem, err := embed.Open(cfg.Embedder, dataDir, database, logging.New("embed", log))
	if err != nil {
		return err
	}
	if sem == nil {
		return fmt.Errorf("semantic search is off (embedder.provider: none)")
	}
	defer sem.Close()
	svc := search.NewService(database, sem.Embedder, logging.New("search", log))
	scores, err := svc.Evaluate(context.Background(), cases,
		[]search.Mode{search.ModeKeyword, search.ModeSemantic, search.ModeHybrid}, k)
	if err != nil {
		return err
	}
	search.PrintEval(os.Stdout, scores, k, len(cases))
	return nil
}
