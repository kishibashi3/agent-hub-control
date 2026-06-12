// restart.go — bridge restart command (stop → spawn with same workdir/tenant)
package bridge

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

const (
	restartStopTimeoutS  = 15
	restartStopPollMs    = 200
)

func NewRestartCmd() *cobra.Command {
	var (
		displayName string
		timeout     int
		all         bool
	)

	cmd := &cobra.Command{
		Use:   "restart <handle> | --all",
		Short: "Restart a bridge worker (stop then spawn with same workdir/tenant)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) > 0 {
					return fmt.Errorf("--all and <handle> are mutually exclusive")
				}
				return runRestartAll(displayName, timeout)
			}
			if len(args) == 0 {
				return fmt.Errorf("either <handle> or --all is required")
			}
			return runRestart(args[0], displayName, timeout)
		},
	}

	cmd.Flags().StringVar(&displayName, "display-name", "", "display name passed to the bridge on re-spawn (optional)")
	cmd.Flags().IntVar(&timeout, "timeout", defaultSpawnTimeoutS, "seconds to wait for ready signal after re-spawn")
	cmd.Flags().BoolVar(&all, "all", false, "Restart all known bridge workers sequentially")

	return cmd
}

func runRestart(handle, displayName string, spawnTimeoutS int) error {
	// ── 1. state を読み、エントリを取得 ──────────────────────────────────
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	entry := st.Get(handle)
	if entry == nil {
		unlock()
		return fmt.Errorf("@%s not found in state. Use `bridge list` to see running bridges.", handle)
	}

	// エントリから spawn に必要な情報を保存
	savedBridgeType := entry.BridgeType
	if savedBridgeType == "" {
		savedBridgeType = defaultBridgeType
	}
	savedWorkdir := entry.Workdir
	savedTenant := entry.Tenant
	savedPID := entry.PID
	isRunning := entry.IsRunning()

	// ── 2. stop ──────────────────────────────────────────────────────────
	if isRunning {
		proc, findErr := os.FindProcess(savedPID)
		if findErr != nil {
			unlock()
			return fmt.Errorf("find process pid=%d: %w", savedPID, findErr)
		}

		if sigErr := proc.Signal(syscall.SIGTERM); sigErr != nil {
			unlock()
			return fmt.Errorf("SIGTERM pid=%d: %w", savedPID, sigErr)
		}

		fmt.Fprintf(os.Stderr, "stopping @%s (pid=%d)...\n", handle, savedPID)

		// state からエントリを削除して保存
		st.Delete(handle)
		if saveErr := st.Save(); saveErr != nil {
			unlock()
			return fmt.Errorf("save state: %w", saveErr)
		}
		unlock()

		// プロセスが終了するまで待機
		if waitErr := waitForExit(savedPID); waitErr != nil {
			return fmt.Errorf("wait for @%s to stop: %w", handle, waitErr)
		}
		fmt.Fprintf(os.Stderr, "stopped @%s (pid=%d)\n", handle, savedPID)
	} else {
		fmt.Fprintf(os.Stderr, "warning: @%s (pid=%d) was not running. Cleaning up state and re-spawning.\n", handle, savedPID)
		st.Delete(handle)
		if saveErr := st.Save(); saveErr != nil {
			unlock()
			return fmt.Errorf("save state: %w", saveErr)
		}
		unlock()
	}

	// ── 3. spawn ─────────────────────────────────────────────────────────
	fmt.Fprintf(os.Stderr, "re-spawning @%s (type=%s, workdir=%s)...\n", handle, savedBridgeType, savedWorkdir)
	return runSpawn(handle, savedBridgeType, savedWorkdir, savedTenant, displayName, spawnTimeoutS)
}

func runRestartAll(displayName string, spawnTimeoutS int) error {
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if len(st.Bridges) == 0 {
		fmt.Println("no bridges to restart")
		return nil
	}

	var handles []string
	for h := range st.Bridges {
		handles = append(handles, h)
	}

	var failed []string
	for _, h := range handles {
		fmt.Fprintf(os.Stderr, "=== restarting @%s ===\n", h)
		if err := runRestart(h, displayName, spawnTimeoutS); err != nil {
			fmt.Fprintf(os.Stderr, "error: @%s: %v\n", h, err)
			failed = append(failed, h)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("restart failed for: %v", failed)
	}
	fmt.Printf("restarted %d bridge(s)\n", len(handles))
	return nil
}

// waitForExit は pid のプロセスが終了するまでポーリングで待機する。
func waitForExit(pid int) error {
	deadline := time.Now().Add(restartStopTimeoutS * time.Second)
	for time.Now().Before(deadline) {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return nil // プロセスが消えた
		}
		if proc.Signal(syscall.Signal(0)) != nil {
			return nil // シグナル送信失敗 = プロセスが存在しない
		}
		time.Sleep(restartStopPollMs * time.Millisecond)
	}
	return fmt.Errorf("pid=%d did not exit within %ds", pid, restartStopTimeoutS)
}
