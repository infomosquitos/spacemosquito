package embed

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vkh/spacemosquito/internal/config"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

// noHomebrew hides the machine's Homebrew prefixes (a real install's model
// would otherwise be found).
func noHomebrew(t *testing.T) {
	old := homebrewPrefixes
	homebrewPrefixes = func() []string { return nil }
	t.Cleanup(func() { homebrewPrefixes = old })
}

func libName() string {
	switch runtime.GOOS {
	case "darwin":
		return "libllama.dylib"
	case "windows":
		return "llama.dll"
	}
	return "libllama.so"
}

func helperName() string {
	if runtime.GOOS == "windows" {
		return "spacemosquito-embedder.exe"
	}
	return "spacemosquito-embedder"
}

func TestLocateReleaseArchiveLayout(t *testing.T) {
	noHomebrew(t)
	root := t.TempDir()
	bin := filepath.Join(root, "Caskroom", "spacemosquito", "0.9.0")
	touch(t, filepath.Join(bin, helperName()))
	touch(t, filepath.Join(bin, "lib", libName()))
	// Model cask installs into <prefix>/share, next to <prefix>/bin.
	prefixBin := filepath.Join(root, "prefix", "bin")
	touch(t, filepath.Join(root, "prefix", "share", "spacemosquito", "models", ModelFile))

	p, err := locate(config.EmbedderConfig{}, filepath.Join(root, "data"), []string{prefixBin, bin})
	if err != nil {
		t.Fatal(err)
	}
	if p.Helper != filepath.Join(bin, helperName()) || p.Lib != filepath.Join(bin, "lib") ||
		p.Model != filepath.Join(root, "prefix", "share", "spacemosquito", "models", ModelFile) {
		t.Fatalf("paths = %+v", p)
	}
}

func TestLocateConfigOverridesAndDataDir(t *testing.T) {
	noHomebrew(t)
	root := t.TempDir()
	helper := filepath.Join(root, "custom", "helper")
	touch(t, helper)
	data := filepath.Join(root, "data")
	touch(t, filepath.Join(data, "lib", libName()))
	touch(t, filepath.Join(data, "models", ModelFile))

	p, err := locate(config.EmbedderConfig{HelperPath: helper}, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Helper != helper || p.Lib != filepath.Join(data, "lib") || p.Model != filepath.Join(data, "models", ModelFile) {
		t.Fatalf("paths = %+v", p)
	}
}

func TestLocateMissing(t *testing.T) {
	noHomebrew(t)
	root := t.TempDir()
	touch(t, filepath.Join(root, helperName()))
	_, err := locate(config.EmbedderConfig{}, filepath.Join(root, "data"), []string{root})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "helper (") || !strings.Contains(msg, "llama.cpp libraries") || !strings.Contains(msg, "model (") {
		t.Fatalf("message should list only what is missing: %s", msg)
	}
}

func TestProcessConfigArgs(t *testing.T) {
	zero := 0
	pc := Paths{Helper: "/h", Lib: "/l", Model: "/m.gguf"}.ProcessConfig(config.EmbedderConfig{GPULayers: &zero, Threads: 4})
	want := []string{"-lib", "/l", "-model", "/m.gguf", "-gpu-layers", "0", "-threads", "4"}
	if pc.Path != "/h" || !slices.Equal(pc.Args, want) || pc.IdleTimeout != IdleTimeout {
		t.Fatalf("config = %+v", pc)
	}
	if runtime.GOOS == "linux" && (len(pc.Env) != 1 || !strings.HasPrefix(pc.Env[0], "LD_LIBRARY_PATH=/l")) {
		t.Fatalf("env = %v", pc.Env)
	}
}
