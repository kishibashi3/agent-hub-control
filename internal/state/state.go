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

// Entry は 1 つの bridge プロセスの状態を表す。
type Entry struct {
	Handle    string `json:"handle"`
	PID       int    `json:"pid"`
	Workdir   string `json:"workdir"`
	Tenant    string `json:"tenant,omitempty"`
	LogPath   string `json:"log_path"`
	StartedAt string `json:"started_at"`
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

func statePath() (string, error) {
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(dir, ".local", "share", "agenthubctl", "bridges.json"), nil
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
func (s *State) Set(handle string, pid int, workdir, tenant, logPath string) {
	s.Bridges[handle] = &Entry{
		Handle:    handle,
		PID:       pid,
		Workdir:   workdir,
		Tenant:    tenant,
		LogPath:   logPath,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
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
