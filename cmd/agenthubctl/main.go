// agenthubctl — bridge lifecycle CLI
//
// Usage:
//
//	agenthubctl bridge spawn --user <handle> --workdir <path> [--type <bridge-type>]
//	agenthubctl bridge stop <handle>
//	agenthubctl bridge list
//	agenthubctl bridge status <handle>
//	agenthubctl bridge logs [--follow|-f] <handle>
//	agenthubctl send <@handle> <message>
//	agenthubctl inbox [--mark-read]
//	agenthubctl participants [--online-only]
//
// 環境変数:
//
//	AGENT_HUB_URL                   required  agent-hub MCP エンドポイント
//	AGENT_HUB_TENANT                optional  テナント ID
//	GITHUB_PAT                      required  GitHub Personal Access Token
//	AGENT_HUB_USER                  optional  handle override (pat モード) / handle (trust モード)
//	AGENT_HUB_BRIDGE_CLAUDE2_BIN    optional  bridge-claude2 バイナリパス
//	AGENT_HUB_{TYPE}_BIN            optional  任意 bridge type のバイナリパス (例: AGENT_HUB_BRIDGE_CODEX2_BIN)
package main

import (
	"fmt"
	"os"

	"github.com/kishibashi3/agent-hub-control/internal/bridge"
	"github.com/kishibashi3/agent-hub-control/internal/hub"
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
	root.AddCommand(hub.NewSendCmd())
	root.AddCommand(hub.NewInboxCmd())
	root.AddCommand(hub.NewParticipantsCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
