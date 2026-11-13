package embed

import (
	"context"
	"path/filepath"
	"sync/atomic"

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

	off atomic.Bool // turned off at runtime (SetEnabled)
}

// SettingKey stores the runtime on/off switch (store settings, migration
// 015): "off" turns semantic search off; anything else, or unset, is on.
const SettingKey = "semantic_search"

// LoadEnabled applies the stored on/off switch.
func (s *Semantic) LoadEnabled(ctx context.Context, db store.Store) error {
	if s == nil {
		return nil
	}
	v, _, err := db.GetSetting(ctx, SettingKey)
	if err != nil {
		return err
	}
	s.SetEnabled(v != "off")
	return nil
}

// SaveEnabled stores and applies the on/off switch.
func (s *Semantic) SaveEnabled(ctx context.Context, db store.Store, on bool) error {
	v := "on"
	if !on {
		v = "off"
	}
	if err := db.SetSetting(ctx, SettingKey, v); err != nil {
		return err
	}
	s.SetEnabled(on)
	return nil
}

// Enabled reports whether semantic search is available and switched on.
func (s *Semantic) Enabled() bool { return s != nil && !s.off.Load() }

// SetEnabled switches semantic search at runtime. Off pauses indexing and
// stops the helper (freeing the model's memory); stored vectors are kept, so
// turning it back on only indexes what changed meanwhile.
func (s *Semantic) SetEnabled(on bool) {
	if s == nil || s.off.Swap(!on) == !on {
		return
	}
	s.Background.SetPaused(!on)
	if !on {
		s.Embedder.Stop()
	}
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

// Model is the stored model name of the installed model (Info.Model),
// known without starting the helper.
func (s *Semantic) Model() string { return filepath.Base(s.Paths.Model) }

// Report is semantic search's state for stats output.
type Report struct {
	// Available: installed and not turned off in config.yaml; Enabled:
	// also switched on at runtime.
	Available     bool   `json:"available"`
	Enabled       bool   `json:"enabled"`
	Model         string `json:"model,omitempty"`
	Pages         int    `json:"pages"`
	EmbeddedPages int    `json:"embedded_pages"`
	Chunks        int    `json:"chunks"`
	HelperRunning bool   `json:"helper_running"`
	Indexing      Status `json:"indexing"`
	Error         string `json:"error,omitempty"`
}

// Report returns coverage and indexing state; s may be nil (off).
func (s *Semantic) Report(ctx context.Context, db store.Store) Report {
	if s == nil {
		return Report{}
	}
	r := Report{Available: true, Enabled: s.Enabled(), Model: s.Model(), HelperRunning: s.Embedder.Running(), Indexing: s.Background.Status()}
	st, err := db.GetEmbeddingStats(ctx, r.Model)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Pages, r.EmbeddedPages, r.Chunks = st.Pages, st.EmbeddedPages, st.Chunks
	return r
}
