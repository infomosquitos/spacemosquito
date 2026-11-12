package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vkh/spacemosquito/internal/config"
)

const (
	EnvDataDir       = "SPACEMOSQUITO_DATA_DIR"
	EnvConfigPath    = "CONFIG_PATH"
	EnvMigrations    = "MIGRATIONS_PATH"
	EnvMigrationsDir = "SPACEMOSQUITO_MIGRATIONS_DIR"
	EnvCronConfig    = "CRON_CONFIG_PATH"
	defaultDirName   = ".spacemosquito"
	defaultDBName    = "spacemosquito.db"
)

var (
	mu          sync.RWMutex
	dataDirFlag string
)

// SetDataDir overrides the data directory for this process (e.g. CLI --data-dir).
func SetDataDir(dir string) {
	mu.Lock()
	defer mu.Unlock()
	dataDirFlag = dir
}

func dataDirOverride() string {
	mu.RLock()
	defer mu.RUnlock()
	return dataDirFlag
}

// ResolveDataDir returns the root data directory for all local state.
func ResolveDataDir() (string, error) {
	if dir := dataDirOverride(); dir != "" {
		return absPath(dir)
	}
	if dir := os.Getenv(EnvDataDir); dir != "" {
		return absPath(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, defaultDirName), nil
}

// ResolveConfig returns the config file path (CONFIG_PATH or $DATA_DIR/config.yaml).
func ResolveConfig() (string, error) {
	if path := os.Getenv(EnvConfigPath); path != "" {
		return absPath(path)
	}
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// ResolveSession returns the session encryption file path.
func ResolveSession(cfg *config.Config) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.Session.FilePath) != "" {
		return absPath(expandHome(cfg.Session.FilePath))
	}
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.enc"), nil
}

// ResolveSaved returns the saved pages directory.
func ResolveSaved(cfg *config.Config) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.Storage.BasePath) != "" {
		return absPath(expandHome(cfg.Storage.BasePath))
	}
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "saved"), nil
}

// ResolveCronConfig returns the cron overrides JSON path.
func ResolveCronConfig() (string, error) {
	if path := os.Getenv(EnvCronConfig); path != "" {
		return absPath(path)
	}
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cron-config.json"), nil
}

// ResolveDB returns the SQLite database file path.
func ResolveDB(cfg *config.Config) (string, error) {
	name := defaultDBName
	if cfg != nil && strings.TrimSpace(cfg.Database.Path) != "" {
		name = cfg.Database.Path
	}
	if filepath.IsAbs(name) {
		return name, nil
	}
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// ResolveBrowser returns the rod Chromium download/cache directory.
func ResolveBrowser() (string, error) {
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "browser"), nil
}

// ResolveReports returns the bootstrap/report output directory.
func ResolveReports() (string, error) {
	dir, err := ResolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "reports"), nil
}

// ResolveMigrations returns the migrations root directory.
func ResolveMigrations() (string, error) {
	if path := os.Getenv(EnvMigrationsDir); path != "" {
		return absPath(path)
	}
	if path := os.Getenv(EnvMigrations); path != "" {
		return absPath(path)
	}

	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "migrations")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, "migrations"), nil
}

// NormalizeConfig fills empty path fields from the resolved data directory.
func NormalizeConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	saved, err := ResolveSaved(cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Storage.BasePath) == "" {
		cfg.Storage.BasePath = saved
	} else {
		expanded, err := absPath(expandHome(cfg.Storage.BasePath))
		if err != nil {
			return err
		}
		cfg.Storage.BasePath = expanded
	}

	session, err := ResolveSession(cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Session.FilePath) == "" {
		cfg.Session.FilePath = session
	} else {
		expanded, err := absPath(expandHome(cfg.Session.FilePath))
		if err != nil {
			return err
		}
		cfg.Session.FilePath = expanded
	}

	dbPath, err := ResolveDB(cfg)
	if err != nil {
		return err
	}
	cfg.Database.Path = dbPath

	for _, p := range []*string{&cfg.Embedder.ModelPath, &cfg.Embedder.LibPath, &cfg.Embedder.HelperPath} {
		if strings.TrimSpace(*p) != "" {
			if *p, err = absPath(*p); err != nil {
				return err
			}
		}
	}

	return nil
}

// EnsureLayout creates standard data-directory subdirectories.
func EnsureLayout(dataDir string) error {
	for _, sub := range []string{"", "saved", "browser"} {
		dir := dataDir
		if sub != "" {
			dir = filepath.Join(dataDir, sub)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func absPath(path string) (string, error) {
	path = expandHome(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if len(path) == 1 || path[1] == '/' || path[1] == filepath.Separator {
			return filepath.Join(home, path[1:])
		}
	}
	return os.ExpandEnv(path)
}
