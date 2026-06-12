// bridgecfg — per-bridge configuration (~/.config/agenthubctl/bridges/<handle>.json)
package bridgecfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BridgeConfig holds saved defaults for a bridge handle.
type BridgeConfig struct {
	Handle      string `json:"handle"`
	Workdir     string `json:"workdir,omitempty"`
	Tenant      string `json:"tenant,omitempty"`
	BridgeType  string `json:"type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

func configDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("home dir: %w", err)
		}
		base = filepath.Join(dir, ".config")
	}
	return filepath.Join(base, "agenthubctl", "bridges"), nil
}

func configPath(handle string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, handle+".json"), nil
}

// Load reads config for a handle. Returns nil (not an error) if the file does not exist.
func Load(handle string) (*BridgeConfig, error) {
	path, err := configPath(handle)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read bridge config: %w", err)
	}
	var cfg BridgeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse bridge config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes config for a handle atomically.
func Save(cfg *BridgeConfig) error {
	path, err := configPath(cfg.Handle)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// ListAll returns all saved bridge configs sorted by filename.
func ListAll() ([]*BridgeConfig, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config dir: %w", err)
	}
	var cfgs []*BridgeConfig
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		handle := e.Name()[:len(e.Name())-len(".json")]
		cfg, err := Load(handle)
		if err != nil {
			return nil, err
		}
		if cfg != nil {
			cfgs = append(cfgs, cfg)
		}
	}
	return cfgs, nil
}

// Delete removes the config file for a handle.
func Delete(handle string) error {
	path, err := configPath(handle)
	if err != nil {
		return err
	}
	if err := os.Remove(path); os.IsNotExist(err) {
		return fmt.Errorf("no config for @%s", handle)
	}
	return err
}
