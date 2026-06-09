// inbox.go — inbox サブコマンド (issue #9)
package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type inboxMessage struct {
	ID        string  `json:"id"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Message   string  `json:"message"`
	CausedBy  *string `json:"caused_by"`
	Timestamp string  `json:"timestamp"`
}

// NewInboxCmd は `agenthubctl inbox [--mark-read]` コマンドを返す。
func NewInboxCmd() *cobra.Command {
	var markRead bool

	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Show unread messages from agent-hub",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInbox(markRead)
		},
	}

	cmd.Flags().BoolVar(&markRead, "mark-read", false, "mark displayed messages as read")
	return cmd
}

func runInbox(markRead bool) error {
	client, err := NewClient()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	text, err := client.CallTool("get_messages", map[string]any{})
	if err != nil {
		return err
	}

	var messages []inboxMessage
	if err := json.Unmarshal([]byte(text), &messages); err != nil {
		fmt.Println(text)
		return nil
	}

	if len(messages) == 0 {
		fmt.Println("no unread messages")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FROM\tMESSAGE\tTIMESTAMP")
	for _, m := range messages {
		msg := m.Message
		if len([]rune(msg)) > 60 {
			runes := []rune(msg)
			msg = string(runes[:57]) + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.From, msg, m.Timestamp)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if !markRead {
		return nil
	}

	for _, m := range messages {
		if _, err := client.CallTool("mark_as_read", map[string]any{
			"message_id": m.ID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warn: mark_as_read %s: %v\n", m.ID, err)
		}
	}
	return nil
}
