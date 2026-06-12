// sync.go — bridge sync command
//
// 双方向同期: bridges.json と実プロセスを突き合わせて整合させる
//   - dead entry の削除（prune と同様）
//   - bridges.json に存在しない実行中の bridge-claude2 プロセスを検出して entry を追加
package bridge

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewSyncCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync bridges.json with actual processes (prune dead + register orphans)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSync(dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without modifying bridges.json")
	return cmd
}

// orphanEntry holds info parsed from a running process not in bridges.json.
type orphanEntry struct {
	pid         int
	handle      string
	workdir     string
	tenant      string
	bridgeType  string
}

func runSync(dryRun bool) error {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	defer unlock()

	// ── 1. dead entry を収集 ────────────────────────────────────────────
	var dead []string
	for handle, entry := range st.Bridges {
		if !entry.IsRunning() {
			dead = append(dead, handle)
		}
	}

	// ── 2. orphan プロセスを検出 ─────────────────────────────────────────
	// bridges.json に記録のない bridge-claude2 プロセスを pgrep + /proc で特定する
	orphans, err := findOrphanBridges(st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: orphan detection failed: %v\n", err)
		// 検出に失敗してもdead entryの削除は続行する
	}

	// ── 3. 変更内容を表示 ─────────────────────────────────────────────
	changed := len(dead) > 0 || len(orphans) > 0

	if !changed {
		fmt.Println("bridges.json is in sync with running processes")
		return nil
	}

	if len(dead) > 0 {
		for _, h := range dead {
			pid := st.Bridges[h].PID
			if dryRun {
				fmt.Printf("would remove dead @%s (pid=%d)\n", h, pid)
			} else {
				fmt.Printf("removing dead @%s (pid=%d)\n", h, pid)
				st.Delete(h)
			}
		}
	}

	if len(orphans) > 0 {
		for _, o := range orphans {
			if dryRun {
				fmt.Printf("would add orphan @%s (pid=%d, workdir=%s)\n", o.handle, o.pid, o.workdir)
			} else {
				fmt.Printf("adding orphan @%s (pid=%d, workdir=%s)\n", o.handle, o.pid, o.workdir)
				logPath := fmt.Sprintf("/tmp/bridge-%s.log", o.handle)
				st.Bridges[o.handle] = &state.Entry{
					Handle:     o.handle,
					PID:        o.pid,
					BridgeType: o.bridgeType,
					Workdir:    o.workdir,
					Tenant:     o.tenant,
					LogPath:    logPath,
					StartedAt:  time.Now().UTC().Format(time.RFC3339),
				}
			}
		}
	}

	if dryRun {
		return nil
	}
	return st.Save()
}

// findOrphanBridges は bridges.json に存在しない bridge-claude2 プロセスを返す。
func findOrphanBridges(st *state.State) ([]orphanEntry, error) {
	// pgrep で bridge-claude2 の全 PID を取得
	pids, err := pgrepBridgeProcesses()
	if err != nil {
		return nil, err
	}

	selfPID := os.Getpid()
	var orphans []orphanEntry

	for _, pid := range pids {
		if pid == selfPID {
			continue
		}

		// /proc/<pid>/cmdline を読んでフラグを解析
		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue // プロセスが終了した可能性
		}

		args := strings.Split(string(cmdlineBytes), "\x00")
		handle, workdir, tenant, bridgeType := parseBridgeCmdline(args)
		if handle == "" {
			continue // --participant が見つからない
		}

		// すでに bridges.json に記録されているか確認
		if entry := st.Get(handle); entry != nil {
			continue
		}

		orphans = append(orphans, orphanEntry{
			pid:        pid,
			handle:     handle,
			workdir:    workdir,
			tenant:     tenant,
			bridgeType: bridgeType,
		})
	}

	return orphans, nil
}

// pgrepBridgeProcesses は bridge-claude2 または bridge-* プロセスの PID 一覧を返す。
func pgrepBridgeProcesses() ([]int, error) {
	// /proc を直接スキャンして bridge- で始まるプロセスを検出する
	// pgrep への依存を避け、bridge-claude2 以外の bridge type も対象にする
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("readdir /proc: %w", err)
	}

	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // 数字でないエントリはスキップ
		}

		commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))
		if strings.HasPrefix(comm, "bridge-") {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// parseBridgeCmdline は bridge プロセスの cmdline 引数から各フラグを抽出する。
// args は /proc/<pid>/cmdline を "\x00" で split したもの。
func parseBridgeCmdline(args []string) (handle, workdir, tenant, bridgeType string) {
	if len(args) > 0 {
		comm := args[0]
		if idx := strings.LastIndex(comm, "/"); idx >= 0 {
			comm = comm[idx+1:]
		}
		if strings.HasPrefix(comm, "bridge-") {
			bridgeType = comm
		}
	}

	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--participant":
			handle = args[i+1]
		case "--workdir":
			workdir = args[i+1]
		case "--tenant":
			tenant = args[i+1]
		}
	}
	return
}
