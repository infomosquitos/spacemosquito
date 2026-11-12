package embed

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vkh/spacemosquito/internal/config"
)

// ModelFile is the GGUF file of the bundled model (bge-m3, Q8_0).
const ModelFile = "bge-m3-q8_0.gguf"

// IdleTimeout stops the helper after this long without requests.
const IdleTimeout = 10 * time.Minute

// Paths are the files the helper needs.
type Paths struct {
	Helper string
	Lib    string
	Model  string
}

// ErrNotInstalled means the helper, the libraries or the model is missing.
var ErrNotInstalled = errors.New("semantic search files not found")

// Locate finds the helper, the llama.cpp libraries and the model: config
// overrides first, then next to the spacemosquito executable (release
// archive, Homebrew cask), then the Homebrew share directory (model cask),
// then dataDir.
func Locate(cfg config.EmbedderConfig, dataDir string) (Paths, error) {
	return locate(cfg, dataDir, executableDirs())
}

func locate(cfg config.EmbedderConfig, dataDir string, exeDirs []string) (Paths, error) {
	helperName := "spacemosquito-embedder"
	if runtime.GOOS == "windows" {
		helperName += ".exe"
	}

	var helperCands, libCands, modelCands []string
	if cfg.HelperPath != "" {
		helperCands = append(helperCands, cfg.HelperPath)
	}
	if cfg.LibPath != "" {
		libCands = append(libCands, cfg.LibPath)
	}
	if cfg.ModelPath != "" {
		modelCands = append(modelCands, cfg.ModelPath)
	}
	for _, d := range exeDirs {
		helperCands = append(helperCands, filepath.Join(d, helperName))
		libCands = append(libCands, filepath.Join(d, "lib"))
		modelCands = append(modelCands,
			filepath.Join(d, "models", ModelFile),
			filepath.Join(d, "..", "share", "spacemosquito", "models", ModelFile))
	}
	for _, prefix := range homebrewPrefixes() {
		modelCands = append(modelCands, filepath.Join(prefix, "share", "spacemosquito", "models", ModelFile))
	}
	if dataDir != "" {
		libCands = append(libCands, filepath.Join(dataDir, "lib"))
		modelCands = append(modelCands, filepath.Join(dataDir, "models", ModelFile))
	}

	var p Paths
	var missing []string
	if p.Helper = firstFile(helperCands); p.Helper == "" {
		missing = append(missing, "helper ("+strings.Join(helperCands, ", ")+")")
	}
	if p.Lib = firstLibDir(libCands); p.Lib == "" {
		missing = append(missing, "llama.cpp libraries ("+strings.Join(libCands, ", ")+")")
	}
	if p.Model = firstFile(modelCands); p.Model == "" {
		missing = append(missing, "model ("+strings.Join(dedupe(modelCands), ", ")+")")
	}
	if len(missing) > 0 {
		return p, fmt.Errorf("%w: %s", ErrNotInstalled, strings.Join(missing, "; "))
	}
	return p, nil
}

// ProcessConfig returns how to run the helper for these paths.
func (p Paths) ProcessConfig(cfg config.EmbedderConfig) ProcessConfig {
	args := []string{"-lib", p.Lib, "-model", p.Model}
	if cfg.GPULayers != nil {
		args = append(args, "-gpu-layers", strconv.Itoa(*cfg.GPULayers))
	}
	if cfg.Threads > 0 {
		args = append(args, "-threads", strconv.Itoa(cfg.Threads))
	}
	var env []string
	if runtime.GOOS == "linux" {
		// libffi (loaded at the helper's init) and llama.cpp's own
		// dependencies ship in the lib directory.
		ld := p.Lib
		if cur := os.Getenv("LD_LIBRARY_PATH"); cur != "" {
			ld += string(os.PathListSeparator) + cur
		}
		env = append(env, "LD_LIBRARY_PATH="+ld)
	}
	return ProcessConfig{Path: p.Helper, Args: args, Env: env, IdleTimeout: IdleTimeout}
}

// executableDirs returns the directory of the running binary as invoked
// (e.g. /opt/homebrew/bin) and with symlinks resolved (the Caskroom).
func executableDirs() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	dirs := []string{filepath.Dir(exe)}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && filepath.Dir(resolved) != dirs[0] {
		dirs = append(dirs, filepath.Dir(resolved))
	}
	return dirs
}

func homebrewPrefixes() []string {
	var out []string
	if p := os.Getenv("HOMEBREW_PREFIX"); p != "" {
		out = append(out, p)
	}
	switch runtime.GOOS {
	case "darwin":
		out = append(out, "/opt/homebrew", "/usr/local")
	case "linux":
		out = append(out, "/home/linuxbrew/.linuxbrew")
	}
	return out
}

func firstFile(cands []string) string {
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return filepath.Clean(c)
		}
	}
	return ""
}

// firstLibDir returns the first directory holding llama.cpp's main library.
func firstLibDir(cands []string) string {
	lib := "libllama.so"
	switch runtime.GOOS {
	case "darwin":
		lib = "libllama.dylib"
	case "windows":
		lib = "llama.dll"
	}
	for _, c := range cands {
		if _, err := os.Stat(filepath.Join(c, lib)); err == nil {
			return filepath.Clean(c)
		}
	}
	return ""
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		x = filepath.Clean(x)
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
