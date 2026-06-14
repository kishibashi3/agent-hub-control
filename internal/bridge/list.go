// list.go — bridge list command
package bridge

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all known bridge workers (including untracked running processes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList()
		},
	}
}

func runList() error {
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// 実プロセスを正本にするため、state に無い稼働中 bridge を pgrep + /proc で発見する (issue #38)。
	orphans, oerr := findOrphanBridges(st)
	if oerr != nil {
		fmt.Fprintf(os.Stderr, "warning: orphan detection failed: %v\n", oerr)
	}

	if len(st.Bridges) == 0 && len(orphans) == 0 {
		fmt.Println("no bridges")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tSTATUS\tPID\tTYPE\tTENANT\tWORKDIR\tSTARTED")

	deadCount := 0
	for _, e := range st.Bridges {
		status := "running"
		if !e.IsRunning() {
			status = "dead"
			deadCount++
		}
		fmt.Fprintf(w, "@%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			e.Handle, status, e.PID, bridgeTypeOrDefault(e.BridgeType), tenantOrDefault(e.Tenant), e.Workdir, e.StartedAt)
	}

	// state に無い稼働中プロセスを untracked として併記する。
	for _, o := range orphans {
		fmt.Fprintf(w, "@%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			o.handle, "untracked", o.pid, bridgeTypeOrDefault(o.bridgeType), tenantOrDefault(o.tenant), o.workdir, "(running)")
	}

	if err := w.Flush(); err != nil {
		return err
	}

	// drift があれば reconcile/prune をサジェストする (issue #38 discoverability)。
	if len(orphans) > 0 {
		fmt.Fprintf(os.Stderr,
			"\nwarning: %d untracked bridge process(es) running but not in state. Run `agenthubctl bridge sync` to adopt them.\n",
			len(orphans))
	}
	if deadCount > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %d dead entr(ies) in state. Run `agenthubctl bridge prune` to remove them (or `bridge sync` for both).\n",
			deadCount)
	}

	return nil
}

func tenantOrDefault(t string) string {
	if t == "" {
		return "(default)"
	}
	return t
}

func bridgeTypeOrDefault(t string) string {
	if t == "" {
		return defaultBridgeType
	}
	return t
}
