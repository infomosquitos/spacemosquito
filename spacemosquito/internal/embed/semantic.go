package embed

import (
	"github.com/vkh/spacemosquito/internal/config"
	"github.com/vkh/spacemosquito/internal/store"
	"github.com/vkh/spacemosquito/pkg/logging"
)

// Semantic bundles what semantic search needs at runtime: the helper
// process, the indexer and the background runner.
type Semantic struct {
	Paths      Paths
	Embedder   *Process
	Indexer    *Indexer
	Background *Background
}

// Open prepares semantic search. It returns nil (and no error) when the
// config turns it off, and an ErrNotInstalled error when files are missing.
// Nothing starts until the first embed or Background.Run.
func Open(cfg config.EmbedderConfig, dataDir string, db store.Store, log logging.Sugar) (*Semantic, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	p, err := Locate(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	proc := NewProcess(p.ProcessConfig(cfg), log)
	ix := NewIndexer(db, proc, log)
	return &Semantic{
		Paths:      p,
		Embedder:   proc,
		Indexer:    ix,
		Background: NewBackground(ix, log),
	}, nil
}

// Close stops the helper.
func (s *Semantic) Close() {
	if s != nil {
		s.Embedder.Close()
	}
}
