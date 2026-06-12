// status.go — bridge status command
package bridge

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [handle]",
		Short: "Show status of bridge worker(s). Omit handle to list all.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runStatusAll()
			}
			return runStatus(args[0])
		},
	}
}

func runStatusAll() error {
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if len(st.Bridges) == 0 {
		fmt.Println("no bridges")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tSTATUS\tPID\tTYPE\tTENANT\tSTARTED")
	for _, e := range st.Bridges {
		label := "DEAD"
		if e.IsRunning() {
			label = "LIVE"
		}
		tenant := e.Tenant
		if tenant == "" {
			tenant = "(default)"
		}
		bridgeType := e.BridgeType
		if bridgeType == "" {
			bridgeType = "bridge-claude2"
		}
		fmt.Fprintf(w, "@%s\t%s\t%d\t%s\t%s\t%s\n",
			e.Handle, label, e.PID, bridgeType, tenant, e.StartedAt)
	}
	return w.Flush()
}

func runStatus(user string) error {
	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	entry := st.Get(user)
	if entry == nil {
		return fmt.Errorf("@%s not found", user)
	}

	status := "stopped"
	if entry.IsRunning() {
		status = "running"
	}

	tenant := entry.Tenant
	if tenant == "" {
		tenant = "(default)"
	}

	bridgeType := entry.BridgeType
	if bridgeType == "" {
		bridgeType = "bridge-claude2"
	}

	fmt.Printf("handle:   @%s\n", entry.Handle)
	fmt.Printf("status:   %s\n", status)
	fmt.Printf("pid:      %d\n", entry.PID)
	fmt.Printf("type:     %s\n", bridgeType)
	fmt.Printf("tenant:   %s\n", tenant)
	fmt.Printf("workdir:  %s\n", entry.Workdir)
	fmt.Printf("log:      %s\n", entry.LogPath)
	fmt.Printf("started:  %s\n", entry.StartedAt)

	// 最後の数行のログを表示
	if entry.LogPath != "" {
		if tail, err := tailLog(entry.LogPath, 5); err == nil && len(tail) > 0 {
			fmt.Println("\n--- recent log ---")
			for _, line := range tail {
				fmt.Println(line)
			}
		}
	}

	return nil
}

// tailLog はファイルの末尾 n 行を返す。
func tailLog(path string, n int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := splitLines(string(data))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func splitLines(s string) []string {
	if len(s) == 0 {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
