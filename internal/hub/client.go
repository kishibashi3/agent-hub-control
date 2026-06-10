// client.go — MCP クライアント（agent-hub tools/call ラッパー）
package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Client は agent-hub MCP セッションを保持する。
type Client struct {
	baseURL    string
	httpClient *http.Client
	authHeader authHeader
	sessionID  string
}

type authHeader struct {
	key   string
	value string
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
	ID      *int   `json:"id,omitempty"` // nil = notification (no response), non-nil = request
}

func reqID(n int) *int { return &n }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	ID      int             `json:"id,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// NewClient は環境変数から認証情報を読み取り、MCP セッションを初期化する。
func NewClient() (*Client, error) {
	baseURL := os.Getenv("AGENT_HUB_URL")
	if baseURL == "" {
		return nil, fmt.Errorf("AGENT_HUB_URL is not set")
	}

	c := &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{},
	}

	pat := os.Getenv("GITHUB_PAT")
	if pat != "" {
		c.authHeader = authHeader{key: "Authorization", value: "Bearer " + pat}
		// warn once if deprecated AGENT_HUB_USER override is still in use
		if os.Getenv("AGENT_HUB_PARTICIPANT") == "" && os.Getenv("AGENT_HUB_USER") != "" {
			fmt.Fprintln(os.Stderr, "warning: AGENT_HUB_USER is deprecated; use AGENT_HUB_PARTICIPANT instead")
		}
	} else if participant := os.Getenv("AGENT_HUB_PARTICIPANT"); participant != "" {
		c.authHeader = authHeader{key: "X-Participant-Id", value: participant}
	} else if user := os.Getenv("AGENT_HUB_USER"); user != "" {
		fmt.Fprintln(os.Stderr, "warning: AGENT_HUB_USER is deprecated; use AGENT_HUB_PARTICIPANT instead")
		c.authHeader = authHeader{key: "X-Participant-Id", value: user}
	} else {
		return nil, fmt.Errorf("set GITHUB_PAT (pat mode) or AGENT_HUB_PARTICIPANT (trust mode)")
	}

	if err := c.initialize(); err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return c, nil
}

func (c *Client) initialize() error {
	resp, err := c.post(rpcRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "agenthubctl", "version": "1.0"},
		},
		ID: reqID(0),
	}, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) //nolint:errcheck

	sid := resp.Header.Get("mcp-session-id")
	if sid == "" {
		return fmt.Errorf("no mcp-session-id in response (HTTP %d)", resp.StatusCode)
	}
	c.sessionID = sid

	// MCP プロトコル必須 handshake
	if err := c.notify("notifications/initialized"); err != nil {
		return fmt.Errorf("notifications/initialized: %w", err)
	}
	return nil
}

func (c *Client) notify(method string) error {
	resp, err := c.post(rpcRequest{JSONRPC: "2.0", Method: method}, c.sessionID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) //nolint:errcheck
	return nil
}

// CallTool は MCP tools/call を呼び、content[0].text を返す。
// サーバーは SSE 形式 (text/event-stream) でレスポンスを返す。
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	resp, err := c.post(rpcRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  map[string]any{"name": name, "arguments": args},
		ID:      reqID(1),
	}, c.sessionID)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := extractSSEData(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var rpc rpcResponse
	if err := json.Unmarshal([]byte(data), &rpc); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if rpc.Error != nil {
		return "", fmt.Errorf("rpc error %d: %s", rpc.Error.Code, rpc.Error.Message)
	}

	var result toolCallResult
	if err := json.Unmarshal(rpc.Result, &result); err != nil {
		return "", fmt.Errorf("decode result: %w", err)
	}
	if result.IsError {
		if len(result.Content) > 0 {
			return "", fmt.Errorf("tool error: %s", result.Content[0].Text)
		}
		return "", fmt.Errorf("tool returned error")
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	return result.Content[0].Text, nil
}

// extractSSEData は SSE レスポンスボディから最初の data: 行を返す。
// サーバーが plain JSON で返す場合もそのまま返す。
func extractSSEData(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: "), nil
		}
		// plain JSON fallback (Content-Type: application/json の場合)
		if strings.HasPrefix(line, "{") {
			return line, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no data in response")
}

func (c *Client) post(req rpcRequest, sessionID string) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set(c.authHeader.key, c.authHeader.value)

	if tenant := os.Getenv("AGENT_HUB_TENANT"); tenant != "" {
		httpReq.Header.Set("X-Tenant-Id", tenant)
	}
	if override := os.Getenv("AGENT_HUB_PARTICIPANT"); override != "" && c.authHeader.key == "Authorization" {
		httpReq.Header.Set("X-Participant-Id", override)
	} else if override := os.Getenv("AGENT_HUB_USER"); override != "" && c.authHeader.key == "Authorization" {
		httpReq.Header.Set("X-Participant-Id", override)
	}
	if sessionID != "" {
		httpReq.Header.Set("mcp-session-id", sessionID)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", c.baseURL, err)
	}
	return resp, nil
}
