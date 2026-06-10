// state.go — bridge state persistence (~/.agent-hub/state/bridges.json)
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Entry は 1 つの bridge プロセスの状態を表す。
type Entry struct {
	Handle     string `json:"handle"`
	PID        int    `json:"pid"`
	BridgeType string `json:"bridge_type,omitempty"`
	Workdir    string `json:"workdir"`
	Tenant     string `json:"tenant,omitempty"`
	LogPath    string `json:"log_path"`
	StartedAt  string `json:"started_at"`
}

// IsRunning はプロセスが実行中かどうかを返す。
func (e *Entry) IsRunning() bool {
	if e.PID <= 0 {
		return false
	}
	proc, err := os.FindProcess(e.PID)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// State は全 bridge エントリのコレクション。
type State struct {
	Bridges map[string]*Entry `json:"bridges"`
	path    string
}

func stateDir() (string, error) {
	if base := os.Getenv("AGENT_HUB_HOME"); base != "" {
		return filepath.Join(base, "state"), nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(dir, ".agent-hub", "state"), nil
}

func statePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bridges.json"), nil
}

// oldStatePath は旧パス (~/.local/share/agenthubctl/bridges.json) を返す。マイグレーション用。
func oldStatePath() (string, error) {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "agenthubctl", "bridges.json"), nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(dir, ".local", "share", "agenthubctl", "bridges.json"), nil
}

// migrateIfNeeded は新パスが存在しない場合に旧パスから自動コピーする。
func migrateIfNeeded(newPath string) error {
	if _, err := os.Stat(newPath); err == nil {
		return nil // 新パスが既にある場合はスキップ
	}
	oldPath, err := oldStatePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(oldPath)
	if os.IsNotExist(err) {
		return nil // 旧ファイルも存在しない — 初回起動
	}
	if err != nil {
		return fmt.Errorf("read old state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(newPath, data, 0o600); err != nil {
		return fmt.Errorf("write new state: %w", err)
	}
	fmt.Fprintf(os.Stderr, "info: migrated state %s → %s\n", oldPath, newPath)
	return nil
}

// Load はディスクから状態を読み込む（読み取り専用操作用）。ファイルが存在しない場合は空の State を返す。
func Load() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}
	if err := migrateIfNeeded(path); err != nil {
		fmt.Fprintf(os.Stderr, "warning: state migration failed: %v\n", err)
	}
	return loadFromPath(path)
}

// LoadLocked はディスクから状態を排他ロック付きで読み込む。
// 返された unlock 関数は必ず呼ぶこと（defer 推奨）。Save() を伴う操作はこちらを使う。
func LoadLocked() (*State, func(), error) {
	path, err := statePath()
	if err != nil {
		return nil, nil, err
	}

	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("mkdir state dir: %w", err)
	}

	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open lockfile: %w", err)
	}

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		_ = lf.Close()
		return nil, nil, fmt.Errorf("flock: %w", err)
	}

	unlock := func() {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		_ = lf.Close()
	}

	s, err := loadFromPath(path)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return s, unlock, nil
}

func loadFromPath(path string) (*State, error) {
	s := &State{Bridges: make(map[string]*Entry), path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}

	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	s.path = path
	return s, nil
}

// Save は状態をディスクに atomic に書き込む（tempfile + rename）。
func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write state tmp: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}

// Set は handle のエントリを追加/更新する。
func (s *State) Set(handle string, pid int, bridgeType, workdir, tenant, logPath string) {
	s.Bridges[handle] = &Entry{
		Handle:     handle,
		PID:        pid,
		BridgeType: bridgeType,
		Workdir:    workdir,
		Tenant:     tenant,
		LogPath:    logPath,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	}
}

// Delete は handle のエントリを削除する。
func (s *State) Delete(handle string) {
	delete(s.Bridges, handle)
}

// Get は handle のエントリを返す。存在しない場合は nil。
func (s *State) Get(handle string) *Entry {
	return s.Bridges[handle]
}
