// status.go — bridge status command
package bridge

import (
	"fmt"
	"os"

	"github.com/kishibashi3/agent-hub-control/internal/state"
	"github.com/spf13/cobra"
)

func NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <handle>",
		Short: "Show status of a bridge worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(args[0])
		},
	}
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
