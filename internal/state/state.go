// state.go — bridge state persistence (~/.local/share/agenthubctl/bridges.json)
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func lockFilePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bridges.lock"), nil
}

// LoadLocked はファイルロック（LOCK_EX）を取得してから State を読み込む。
// 返された unlock を必ず呼び出すこと（defer 推奨）。
// ロックは bridges.lock ファイルへの syscall.Flock で実装される。
// Save() 完了後に unlock を呼ぶことで read-modify-write がアトミックになる。
func LoadLocked() (*State, func(), error) {
	lp, err := lockFilePath()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return nil, nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	lf, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		lf.Close()
		return nil, nil, fmt.Errorf("flock acquire: %w", err)
	}
	st, err := Load()
	if err != nil {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
		return nil, nil, err
	}
	unlock := func() {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
	}
	return st, unlock, nil
}

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
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "agenthubctl"), nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(dir, ".local", "share", "agenthubctl"), nil
}

func statePath() (string, error) {
	d, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "bridges.json"), nil
}

// Load はディスクから状態を読み込む。ファイルが存在しない場合は空の State を返す。
func Load() (*State, error) {
	path, err := statePath()
	if err != nil {
		return nil, err
	}

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

// Save は状態をディスクに書き込む。
func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
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
