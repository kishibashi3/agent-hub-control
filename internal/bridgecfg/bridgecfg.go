// bridgecfg — per-bridge configuration (~/.config/agenthubctl/bridges/<handle>.json)
package bridgecfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BridgeConfig holds saved defaults for a bridge handle.
type BridgeConfig struct {
	Handle      string `json:"handle"`
	Workdir     string `json:"workdir,omitempty"`
	Tenant      string `json:"tenant,omitempty"`
	BridgeType  string `json:"type,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// Model は bridge worker に渡す LLM model id (issue #46)。空 = bridge 内蔵 default。
	// 検証はしない (bridge-claude2 も claude CLI にそのまま渡すだけで、id 一覧を持つと陳腐化する)。
	Model string `json:"model,omitempty"`
	// StoppedAt は `bridge stop` で「止めてある」と記録した時刻 (RFC 3339 UTC、issue #98)。
	// 空 = 動かす (fleet の `bridge start --all` が起こす)。ある = 止めてある (fleet は起こさない)。
	// bool でなく時刻なのは「いつから止めてあるか」を残し、「忘れられている」と区別できるようにするため。
	// 項目の無い既存 config は空として読めるので書き換え不要。
	StoppedAt string `json:"stopped_at,omitempty"`
}

// Stopped は config が「止めてある」(stopped_at あり) かを返す。
func (c *BridgeConfig) Stopped() bool {
	return c != nil && c.StoppedAt != ""
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

// stoppedAtFormat は stopped_at の書式 (RFC 3339 UTC、ミリ秒、Z suffix)。
const stoppedAtFormat = "2006-01-02T15:04:05.000Z07:00"

// MarkStopped は handle の config に stopped_at = now を記録する (issue #98)。config が無ければ
// 何もせず (作らない) false を返す。既に止めてある場合は元の時刻を残す (「いつから止めてあるか」を保つ)。
// 他の項目は書き換えない。
func MarkStopped(handle string, now time.Time) (bool, error) {
	cfg, err := Load(handle)
	if err != nil || cfg == nil {
		return false, err
	}
	if cfg.StoppedAt != "" {
		return true, nil
	}
	cfg.StoppedAt = now.UTC().Format(stoppedAtFormat)
	if err := Save(cfg); err != nil {
		return false, err
	}
	return true, nil
}

// ClearStopped は handle の config から stopped_at を消す (明示的に起こした = 動かす、issue #98)。
// config が無い / stopped_at が無い場合はファイルに触らない (既存 config を書き換えない)。
// 消した場合は true を返す。
func ClearStopped(handle string) (bool, error) {
	cfg, err := Load(handle)
	if err != nil || cfg == nil || cfg.StoppedAt == "" {
		return false, err
	}
	cfg.StoppedAt = ""
	if err := Save(cfg); err != nil {
		return false, err
	}
	return true, nil
}
