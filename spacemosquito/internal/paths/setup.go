package paths

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vkh/spacemosquito/internal/config"
	"gopkg.in/yaml.v3"
)

// InitOptions configures first-time workspace setup.
type InitOptions struct {
	DataDir       string
	EncryptionKey string
	ForceConfig   bool
}

// InitResult reports what InitWorkspace created.
type InitResult struct {
	DataDir       string
	ConfigPath    string
	SessionPath   string
	EncryptionKey string
	ConfigCreated bool
}

// InitWorkspace creates the data directory layout, default config, and session file.
func InitWorkspace(opts InitOptions) (*InitResult, error) {
	if opts.DataDir != "" {
		SetDataDir(opts.DataDir)
	}

	dataDir, err := ResolveDataDir()
	if err != nil {
		return nil, err
	}
	if err := EnsureLayout(dataDir); err != nil {
		return nil, err
	}

	configPath, err := ResolveConfig()
	if err != nil {
		return nil, err
	}
	sessionPath := filepath.Join(dataDir, "session.enc")

	key := opts.EncryptionKey
	if key == "" {
		key, err = generateEncryptionKey()
		if err != nil {
			return nil, err
		}
	}

	result := &InitResult{
		DataDir:       dataDir,
		ConfigPath:    configPath,
		SessionPath:   sessionPath,
		EncryptionKey: key,
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) || opts.ForceConfig {
		cfg := defaultDockerlessConfig(dataDir, key)
		if err := writeConfig(configPath, cfg); err != nil {
			return nil, err
		}
		result.ConfigCreated = true
	}

	if err := touchSessionFile(sessionPath); err != nil {
		return nil, err
	}

	return result, nil
}

func defaultDockerlessConfig(dataDir, encryptionKey string) *config.Config {
	return &config.Config{
		Database: config.DatabaseConfig{
			Driver: "sqlite",
			Path:   defaultDBName,
		},
		Storage: config.StorageConfig{
			BasePath: filepath.Join(dataDir, "saved"),
		},
		Session: config.SessionConfig{
			EncryptionKey: encryptionKey,
			FilePath:      filepath.Join(dataDir, "session.enc"),
		},
		MCP: config.MCPConfig{
			Port:    56403,
			Host:    "127.0.0.1",
			Timeout: 3600,
		},
	}
}

func writeConfig(path string, cfg *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func touchSessionFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func generateEncryptionKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate encryption key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}
