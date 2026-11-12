package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_missingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoad_malformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("database: [invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestLoad_defaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
database: {}
storage: {}
session:
  encryption_key: "12345678901234567890123456789012"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Database.Driver != "sqlite" {
		t.Errorf("database.driver = %q, want sqlite", cfg.Database.Driver)
	}
	if cfg.Database.DriverName() != "sqlite" {
		t.Errorf("DriverName() = %q, want sqlite", cfg.Database.DriverName())
	}
	if cfg.Storage.BasePath != "./saved" {
		t.Errorf("storage.base_path = %q, want ./saved", cfg.Storage.BasePath)
	}
	if cfg.MCP.Port != 56403 {
		t.Errorf("mcp.port = %d, want 56403", cfg.MCP.Port)
	}
	if cfg.MCP.Timeout != 3600 {
		t.Errorf("mcp.session_timeout = %d, want 3600", cfg.MCP.Timeout)
	}
	if cfg.Embedder.Provider != EmbedderLocal || !cfg.Embedder.Enabled() {
		t.Errorf("embedder.provider = %q, want %q (enabled)", cfg.Embedder.Provider, EmbedderLocal)
	}
}

func TestLoad_cronDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
database: {}
session:
  encryption_key: "12345678901234567890123456789012"
cron:
  full_crawl:
    enabled: true
  incremental:
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Cron.FullCrawl.Interval != "24h" {
		t.Errorf("full_crawl.interval = %q, want 24h", cfg.Cron.FullCrawl.Interval)
	}
	if cfg.Cron.Incremental.Interval != "2h" {
		t.Errorf("incremental.interval = %q, want 2h", cfg.Cron.Incremental.Interval)
	}
	if cfg.Cron.Incremental.Detection != "dom" {
		t.Errorf("incremental.detection = %q, want dom", cfg.Cron.Incremental.Detection)
	}
	if cfg.Cron.FullCrawl.MaxDuration != "4h" {
		t.Errorf("full_crawl.max_duration = %q, want 4h", cfg.Cron.FullCrawl.MaxDuration)
	}
	if cfg.Cron.Incremental.MaxDuration != "30m" {
		t.Errorf("incremental.max_duration = %q, want 30m", cfg.Cron.Incremental.MaxDuration)
	}
}

func TestParseCronDuration(t *testing.T) {
	d, err := ParseCronDuration("6h")
	if err != nil {
		t.Fatal(err)
	}
	if d != 6*time.Hour {
		t.Errorf("got %v, want 6h", d)
	}

	_, err = ParseCronDuration("not-a-duration")
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestCronJobConfig_ParseMaxDuration(t *testing.T) {
	var nilCfg *CronJobConfig
	d, err := nilCfg.ParseMaxDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d != 4*time.Hour {
		t.Errorf("nil config default = %v, want 4h", d)
	}

	empty := &CronJobConfig{}
	d, err = empty.ParseMaxDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d != 4*time.Hour {
		t.Errorf("empty MaxDuration = %v, want 4h", d)
	}

	custom := &CronJobConfig{MaxDuration: "30m"}
	d, err = custom.ParseMaxDuration()
	if err != nil {
		t.Fatal(err)
	}
	if d != 30*time.Minute {
		t.Errorf("got %v, want 30m", d)
	}

	bad := &CronJobConfig{MaxDuration: "not-a-duration"}
	_, err = bad.ParseMaxDuration()
	if err == nil {
		t.Fatal("expected error for invalid max_duration")
	}
}

func TestLoad_embedder(t *testing.T) {
	for _, tc := range []struct {
		yaml    string
		want    string
		enabled bool
		wantErr bool
	}{
		{yaml: "embedder:\n  model: nomic-embed-text\n", want: EmbedderLocal, enabled: true}, // pre-0.9 placeholder
		{yaml: "embedder:\n  provider: none\n", want: EmbedderNone},
		{yaml: "embedder:\n  provider: ollama\n", wantErr: true},
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", tc.yaml)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.yaml, err)
		}
		if cfg.Embedder.Provider != tc.want || cfg.Embedder.Enabled() != tc.enabled {
			t.Errorf("%q: provider %q enabled %v, want %q %v", tc.yaml, cfg.Embedder.Provider, cfg.Embedder.Enabled(), tc.want, tc.enabled)
		}
	}
}
