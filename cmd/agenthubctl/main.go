// agenthubctl — bridge lifecycle CLI
//
// Usage:
//
//	agenthubctl bridge spawn --user <handle> --workdir <path>
//	agenthubctl bridge stop <handle>
//	agenthubctl bridge list
//	agenthubctl bridge status <handle>
//	agenthubctl bridge logs [--follow|-f] <handle>
//
// 環境変数:
//
//	AGENT_HUB_URL                   required  agent-hub MCP エンドポイント
//	AGENT_HUB_TENANT                optional  テナント ID
//	GITHUB_PAT                      required  GitHub Personal Access Token
//	AGENT_HUB_BRIDGE_CLAUDE2_BIN    optional  bridge-claude2 バイナリパス
package main

import (
	"fmt"
	"os"

	"github.com/kishibashi3/agent-hub-control/internal/bridge"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:          "agenthubctl",
		Short:        "Manage agent-hub bridge workers",
		SilenceUsage: true,
		Version:      version,
	}

	bridgeCmd := &cobra.Command{
		Use:   "bridge",
		Short: "Bridge worker lifecycle operations",
	}

	bridgeCmd.AddCommand(
		bridge.NewSpawnCmd(),
		bridge.NewStopCmd(),
		bridge.NewListCmd(),
		bridge.NewStatusCmd(),
		bridge.NewLogsCmd(),
	)

	root.AddCommand(bridgeCmd)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
