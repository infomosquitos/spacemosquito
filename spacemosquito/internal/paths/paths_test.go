package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vkh/spacemosquito/internal/config"
)

func TestResolveDataDir_default(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	SetDataDir("")

	dir, err := ResolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, defaultDirName)
	if dir != want {
		t.Fatalf("ResolveDataDir() = %q, want %q", dir, want)
	}
}

func TestResolveDataDir_envOverride(t *testing.T) {
	SetDataDir("")
	base := t.TempDir()
	t.Setenv(EnvDataDir, base)

	dir, err := ResolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != base {
		t.Fatalf("ResolveDataDir() = %q, want %q", dir, base)
	}
}

func TestResolveDataDir_flagOverride(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	base := t.TempDir()
	SetDataDir(base)
	t.Cleanup(func() { SetDataDir("") })

	dir, err := ResolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != base {
		t.Fatalf("ResolveDataDir() = %q, want %q", dir, base)
	}
}

func TestResolveConfig_default(t *testing.T) {
	t.Setenv(EnvConfigPath, "")
	t.Setenv(EnvDataDir, "")
	SetDataDir("")

	path, err := ResolveConfig()
	if err != nil {
		t.Fatal(err)
	}
	dataDir, _ := ResolveDataDir()
	want := filepath.Join(dataDir, "config.yaml")
	if path != want {
		t.Fatalf("ResolveConfig() = %q, want %q", path, want)
	}
}

func TestResolveMigrations_devTree(t *testing.T) {
	t.Setenv(EnvMigrations, "")
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/paths -> repo root spacemosquito/migrations
	repoMigrations := filepath.Join(filepath.Dir(filename), "..", "..", "migrations")
	if _, err := os.Stat(repoMigrations); err != nil {
		t.Skip("migrations directory not found")
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(filepath.Dir(filename), "..", ".."))
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	path, err := ResolveMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "migrations" {
		t.Fatalf("ResolveMigrations() = %q", path)
	}
}

func TestNormalizeConfig_embedderPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	SetDataDir("")
	cfg := &config.Config{Embedder: config.EmbedderConfig{ModelPath: "~/models/m.gguf", LibPath: ""}}
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "models", "m.gguf"); cfg.Embedder.ModelPath != want {
		t.Errorf("model_path = %q, want %q", cfg.Embedder.ModelPath, want)
	}
	if cfg.Embedder.LibPath != "" {
		t.Errorf("empty lib_path became %q", cfg.Embedder.LibPath)
	}
}
