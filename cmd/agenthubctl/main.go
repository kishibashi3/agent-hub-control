// agenthubctl — bridge lifecycle CLI
//
// Usage:
//
//	agenthubctl bridge spawn --participant <handle> --workdir <path> [--type <bridge-type>]
//	agenthubctl bridge stop <handle>
//	agenthubctl bridge restart <handle> | --all
//	agenthubctl bridge start <handle> | --all
//	agenthubctl bridge list
//	agenthubctl bridge status [handle]
//	agenthubctl bridge sync [--dry-run]
//	agenthubctl bridge logs [--follow|-f] <handle>
//	agenthubctl bridge prune [--dry-run]
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
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/kishibashi3/agent-hub-control/internal/bridge"
	"github.com/kishibashi3/agent-hub-control/internal/hub"
	"github.com/spf13/cobra"
)

// version is overridable via -ldflags "-X main.version=...". commit / build date は
// go の VCS stamping (debug.ReadBuildInfo) から取得するので Makefile への追加は不要。
var version = "dev"

// buildInfo は go build / go install が埋め込む VCS スタンプを返す。
func buildInfo() (commit, date string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.time":
			date = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return
}

// versionString は version / commit / build date / go version を含む複数行の
// バージョン文字列を返す。古い binary を即座に見分けられるようにするのが目的 (issue #38)。
func versionString() string {
	commit, date, modified := buildInfo()
	if commit == "" {
		commit = "unknown"
	} else if len(commit) > 12 {
		commit = commit[:12]
	}
	if modified {
		commit += "+dirty"
	}
	if date == "" {
		date = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "agenthubctl %s\n", version)
	fmt.Fprintf(&b, "  commit: %s\n", commit)
	fmt.Fprintf(&b, "  built:  %s\n", date)
	fmt.Fprintf(&b, "  go:     %s\n", runtime.Version())
	return b.String()
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, build commit, and build date",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Print(versionString())
		},
	}
}

func main() {
	root := &cobra.Command{
		Use:          "agenthubctl",
		Short:        "Manage agent-hub bridge workers",
		SilenceUsage: true,
		Version:      version,
	}
	// `--version` も `version` サブコマンドと同じ詳細表示にする。
	root.SetVersionTemplate(versionString())

	bridgeCmd := &cobra.Command{
		Use:   "bridge",
		Short: "Bridge worker lifecycle operations",
	}

	bridgeCmd.AddCommand(
		bridge.NewSpawnCmd(),
		bridge.NewStartCmd(),
		bridge.NewStopCmd(),
		bridge.NewRestartCmd(),
		bridge.NewListCmd(),
		bridge.NewStatusCmd(),
		bridge.NewSyncCmd(),
		bridge.NewLogsCmd(),
		bridge.NewPruneCmd(),
		bridge.NewConfigCmd(),
	)

	root.AddCommand(bridgeCmd)
	root.AddCommand(hub.NewSendCmd())
	root.AddCommand(hub.NewInboxCmd())
	root.AddCommand(hub.NewParticipantsCmd())
	root.AddCommand(newVersionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
