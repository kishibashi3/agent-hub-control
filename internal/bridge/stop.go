// stop.go — bridge stop command
package bridge

import (
	"fmt"
	"os"
	"syscall"

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
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	entry := st.Get(user)
	if entry == nil {
		// state にない場合は pgrep で探す
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

	fmt.Printf("stopped @%s (pid=%d)\n", user, entry.PID)

	st.Delete(user)
	if err := st.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return nil
}
