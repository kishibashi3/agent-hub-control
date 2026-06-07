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
		Short: "List all known bridge workers",
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

	if len(st.Bridges) == 0 {
		fmt.Println("no bridges")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tSTATUS\tPID\tTENANT\tWORKDIR\tSTARTED")

	for _, e := range st.Bridges {
		status := "stopped"
		if e.IsRunning() {
			status = "running"
		}
		tenant := e.Tenant
		if tenant == "" {
			tenant = "(default)"
		}
		fmt.Fprintf(w, "@%s\t%s\t%d\t%s\t%s\t%s\n",
			e.Handle, status, e.PID, tenant, e.Workdir, e.StartedAt)
	}

	return w.Flush()
}
