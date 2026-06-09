// config.go — central config file (~/.local/share/agenthubctl/config.json)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds agenthubctl-wide settings.
// Fields are optional; zero value means "use default".
type Config struct {
	// SubprocessTimeoutS is passed as --subprocess-timeout to bridge-claude2 (0 = use bridge default).
	SubprocessTimeoutS int `json:"subprocess_timeout_s,omitempty"`
}

func configPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		base = filepath.Join(dir, ".local", "share")
	}
	return filepath.Join(base, "agenthubctl", "config.json"), nil
}

// Load reads config.json. Returns an empty Config if the file does not exist.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}
