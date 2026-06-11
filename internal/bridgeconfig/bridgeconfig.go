// bridgeconfig.go — per-bridge config (~/.config/agenthubctl/config.json)
package bridgeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BridgeEntry holds spawn defaults for one bridge handle.
type BridgeEntry struct {
	Workdir     string `json:"workdir,omitempty"`
	Tenant      string `json:"tenant,omitempty"`
	BridgeType  string `json:"bridge_type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// Config holds per-bridge settings keyed by handle.
type Config struct {
	Bridges map[string]*BridgeEntry `json:"bridges"`
	path    string
}

func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		base = filepath.Join(dir, ".config")
	}
	return filepath.Join(base, "agenthubctl", "config.json"), nil
}

// Load reads bridge config. Returns an empty Config if the file does not exist.
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}

	c := &Config{Bridges: make(map[string]*BridgeEntry), path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bridge config: %w", err)
	}

	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parse bridge config %s: %w", path, err)
	}
	if c.Bridges == nil {
		c.Bridges = make(map[string]*BridgeEntry)
	}
	c.path = path
	return c, nil
}

// Save writes bridge config atomically (tempfile + rename).
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bridge config: %w", err)
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write bridge config tmp: %w", err)
	}

	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename bridge config: %w", err)
	}
	return nil
}

// Set adds or replaces the entry for handle.
func (c *Config) Set(handle string, entry *BridgeEntry) {
	c.Bridges[handle] = entry
}

// Get returns the entry for handle, or nil if not found.
func (c *Config) Get(handle string) *BridgeEntry {
	return c.Bridges[handle]
}
