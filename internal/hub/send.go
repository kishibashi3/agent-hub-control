// send.go — send サブコマンド (issue #3)
package hub

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

type sentMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// NewSendCmd は `agenthubctl send @handle "message"` コマンドを返す。
func NewSendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "send <@handle> <message>",
		Short: "Send a DM via agent-hub",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSend(args[0], args[1])
		},
	}
}

func runSend(to, message string) error {
	if !strings.HasPrefix(to, "@") {
		to = "@" + to
	}

	client, err := NewClient()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	text, err := client.CallTool("send_message", map[string]any{
		"to":      to,
		"message": message,
	})
	if err != nil {
		return err
	}

	var msg sentMessage
	if err := json.Unmarshal([]byte(text), &msg); err != nil {
		fmt.Println(text)
		return nil
	}

	fmt.Printf("sent id=%s from=%s to=%s\n", msg.ID, msg.From, msg.To)
	return nil
}
