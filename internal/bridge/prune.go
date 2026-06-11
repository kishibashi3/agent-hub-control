// prune.go — bridge prune command
package bridge

import (
	"fmt"
	"sort"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewPruneCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove dead entries from bridges.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(dryRun)
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be removed without making changes")
	return cmd
}

func runPrune(dryRun bool) error {
	st, unlock, err := state.LoadLocked()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	defer unlock()

	var dead []string
	for handle, entry := range st.Bridges {
		if !entry.IsRunning() {
			dead = append(dead, handle)
		}
	}

	if len(dead) == 0 {
		fmt.Println("no dead entries")
		return nil
	}

	sort.Strings(dead)
	for _, handle := range dead {
		pid := st.Bridges[handle].PID
		if dryRun {
			fmt.Printf("would remove @%s (pid=%d)\n", handle, pid)
		} else {
			fmt.Printf("removed @%s (pid=%d)\n", handle, pid)
			st.Delete(handle)
		}
	}

	if dryRun {
		return nil
	}
	return st.Save()
}
