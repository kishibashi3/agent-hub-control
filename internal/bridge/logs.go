// logs.go — bridge logs command
package bridge

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewLogsCmd() *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [--follow|-f] <handle>",
		Short: "Show logs of a bridge worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(args[0], follow)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output (like tail -f)")

	return cmd
}

func runLogs(handle string, follow bool) error {
	handle = strings.TrimPrefix(handle, "@")

	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	entry := st.Get(handle)
	if entry == nil {
		return fmt.Errorf("@%s not found", handle)
	}

	if entry.LogPath == "" {
		return fmt.Errorf("@%s has no log_path recorded", handle)
	}

	if _, err := os.Stat(entry.LogPath); err != nil {
		return fmt.Errorf("log file %q: %w", entry.LogPath, err)
	}

	var tailArgs []string
	if follow {
		tailArgs = []string{"-f", entry.LogPath}
	} else {
		tailArgs = []string{entry.LogPath}
	}

	proc := exec.Command("tail", tailArgs...)
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr

	if err := proc.Start(); err != nil {
		return fmt.Errorf("start tail: %w", err)
	}

	if follow {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sig
			proc.Process.Signal(syscall.SIGTERM)
		}()
	}

	if err := proc.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return nil
			}
		}
		return err
	}
	return nil
}
