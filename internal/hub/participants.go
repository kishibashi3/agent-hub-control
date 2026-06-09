// participants.go — participants サブコマンド (issue #7)
package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type participantEntry struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	DisplayName  *string `json:"display_name"`
	Mode         *string `json:"mode"`
	IsOnline     bool    `json:"is_online"`
	LastActiveAt *string `json:"last_active_at"`
	QueueDepth   int     `json:"queue_depth"`
}

// NewParticipantsCmd は `agenthubctl participants [--online-only]` コマンドを返す。
func NewParticipantsCmd() *cobra.Command {
	var onlineOnly bool

	cmd := &cobra.Command{
		Use:   "participants",
		Short: "List agent-hub participants",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runParticipants(onlineOnly)
		},
	}

	cmd.Flags().BoolVar(&onlineOnly, "online-only", false, "show only online participants")
	return cmd
}

func runParticipants(onlineOnly bool) error {
	client, err := NewClient()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	text, err := client.CallTool("get_participants", map[string]any{})
	if err != nil {
		return err
	}

	// discriminated union: type="person" | "team"
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		fmt.Println(text)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tONLINE\tQUEUE\tLAST_ACTIVE")

	count := 0
	for _, r := range raw {
		var p participantEntry
		if err := json.Unmarshal(r, &p); err != nil {
			continue
		}
		if p.Type != "person" {
			continue
		}
		if onlineOnly && !p.IsOnline {
			continue
		}

		online := "false"
		if p.IsOnline {
			online = "true"
		}
		lastActive := "-"
		if p.LastActiveAt != nil {
			lastActive = *p.LastActiveAt
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", p.Name, online, p.QueueDepth, lastActive)
		count++
	}

	if count == 0 {
		if onlineOnly {
			fmt.Fprintln(os.Stdout, "no online participants")
		} else {
			fmt.Fprintln(os.Stdout, "no participants")
		}
		return nil
	}

	return w.Flush()
}
