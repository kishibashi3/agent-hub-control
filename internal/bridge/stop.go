// stop.go — bridge stop command
package bridge

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <handle>",
		Short: "Stop a running bridge worker (finds the real process even if state is stale)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(args[0])
		},
	}
	return cmd
}

func runStop(user string) error {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	defer unlock()

	entry := st.Get(user)

	// 実プロセスを正本にする (issue #38)。state が stale でも / そもそも無くても、
	// 実際に動いている bridge プロセスを発見して止める (reconcile してから stop)。
	livePID := 0
	switch {
	case entry != nil && entry.IsRunning():
		livePID = entry.PID
	default:
		// state に無い、または state の PID が既に死んでいる場合は実プロセスを探す。
		pid, perr := pgrepHandle(user)
		if perr != nil {
			return fmt.Errorf("search running process for @%s: %w", user, perr)
		}
		livePID = pid
		if pid != 0 && entry == nil {
			fmt.Fprintf(os.Stderr, "note: @%s is not in state but found running (pid=%d) — stopping untracked process.\n", user, pid)
		} else if pid != 0 && entry != nil {
			fmt.Fprintf(os.Stderr, "note: state for @%s was stale (recorded pid=%d dead); stopping live process (pid=%d).\n", user, entry.PID, pid)
		}
	}

	if livePID == 0 {
		if entry != nil {
			fmt.Fprintf(os.Stderr, "warning: @%s (pid=%d) is not running. Cleaning up stale state.\n", user, entry.PID)
			st.Delete(user)
			return st.Save()
		}
		return fmt.Errorf("@%s not found in state or running processes. Use `bridge list` to see what is running.", user)
	}

	if err := stopPID(livePID); err != nil {
		return err
	}

	fmt.Printf("stopped @%s (pid=%d)\n", user, livePID)

	st.Delete(user)
	if err := st.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return nil
}

// stopPID は pid に SIGTERM を送り、最大 5 秒待ってから（まだ生きていれば）SIGKILL する。
func stopPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process pid=%d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM pid=%d: %w", pid, err)
	}

	// プロセスが実際に死ぬのを最大 5 秒待つ
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if proc.Signal(syscall.Signal(0)) != nil {
			break
		}
	}
	// まだ生きていれば SIGKILL
	if proc.Signal(syscall.Signal(0)) == nil {
		fmt.Fprintf(os.Stderr, "warning: pid=%d did not exit after SIGTERM, sending SIGKILL\n", pid)
		_ = proc.Kill()
	}

	return nil
}
