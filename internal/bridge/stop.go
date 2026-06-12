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
		Short: "Stop a running bridge-claude2 worker",
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
	if entry == nil {
		return fmt.Errorf("@%s not found in state. Use `bridge list` to see running bridges.", user)
	}

	if !entry.IsRunning() {
		fmt.Fprintf(os.Stderr, "warning: @%s (pid=%d) is not running. Cleaning up state.\n", user, entry.PID)
		st.Delete(user)
		return st.Save()
	}

	proc, err := os.FindProcess(entry.PID)
	if err != nil {
		return fmt.Errorf("find process pid=%d: %w", entry.PID, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM pid=%d: %w", entry.PID, err)
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
		fmt.Fprintf(os.Stderr, "warning: pid=%d did not exit after SIGTERM, sending SIGKILL\n", entry.PID)
		_ = proc.Kill()
	}

	fmt.Printf("stopped @%s (pid=%d)\n", user, entry.PID)

	st.Delete(user)
	if err := st.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return nil
}
