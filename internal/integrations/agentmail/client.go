package agentmail

// Agent Mail's HTTP transport, as ACFS's Bash uses it: one JSON-RPC
// "tools/call" per request to $AGENT_MAIL_URL (default
// http://127.0.0.1:${ACFS_AGENT_MAIL_PORT:-8765}/mcp/), with the bearer
// token from HTTP_BEARER_TOKEN in Agent Mail's config.env. The answer is
// JSON or one SSE "data:" line; the tool's own result is JSON text in
// result.content[0].text.
//
// nhm reads mail without consuming it: FetchInbox always sends
// mark_read=false (plan-v8 2.5).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client calls Agent Mail's tools over HTTP.
type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

// DefaultURL is Agent Mail's URL from the environment, as ACFS sets it.
func DefaultURL() string {
	if u := os.Getenv("AGENT_MAIL_URL"); u != "" {
		return u
	}
	port := os.Getenv("ACFS_AGENT_MAIL_PORT")
	if port == "" {
		port = "8765"
	}
	return "http://127.0.0.1:" + port + "/mcp/"
}

// NewClient returns a client for DefaultURL with the token from Agent
// Mail's config.env under configHome ("" means ConfigHome()). A missing
// token is not an error: a server without auth needs none.
func NewClient(configHome string) (*Client, error) {
	if configHome == "" {
		var err error
		if configHome, err = ConfigHome(); err != nil {
			return nil, err
		}
	}
	c := &Client{URL: DefaultURL(), HTTP: &http.Client{Timeout: 15 * time.Second}}
	for _, name := range []string{"config.env", ".env"} {
		data, err := os.ReadFile(filepath.Join(configHome, "mcp-agent-mail", name))
		if err != nil {
			continue
		}
		c.Token = bearerToken(data)
		break
	}
	return c, nil
}

// bearerToken is the last HTTP_BEARER_TOKEN= line's value, unquoted.
func bearerToken(env []byte) string {
	token := ""
	sc := bufio.NewScanner(bytes.NewReader(env))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "HTTP_BEARER_TOKEN="); ok {
			token = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return token
}

// ToolError is a refusal from Agent Mail: a JSON-RPC error or a tool
// result with isError.
type ToolError struct {
	Tool    string
	Message string
}

func (e *ToolError) Error() string { return fmt.Sprintf("agentmail %s: %s", e.Tool, e.Message) }

// Call runs one tool and decodes its JSON result into out.
func (c *Client) Call(ctx context.Context, tool string, args, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		return fmt.Errorf("agentmail %s: %w", tool, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agentmail %s: %w", tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("agentmail %s: %w", tool, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("agentmail %s: %w", tool, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("agentmail %s: HTTP %d: %.200s", tool, resp.StatusCode, data)
	}
	var rpc struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(lastJSONLine(data), &rpc); err != nil {
		return fmt.Errorf("agentmail %s: decode: %w", tool, err)
	}
	if rpc.Error != nil {
		return &ToolError{Tool: tool, Message: rpc.Error.Message}
	}
	if rpc.Result == nil {
		return fmt.Errorf("agentmail %s: no result", tool)
	}
	text := ""
	if len(rpc.Result.Content) > 0 {
		text = rpc.Result.Content[0].Text
	}
	if rpc.Result.IsError {
		return &ToolError{Tool: tool, Message: text}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("agentmail %s: decode result: %w", tool, err)
	}
	return nil
}

// lastJSONLine picks the JSON body out of a plain or SSE answer: the last
// "data: " payload, or the last line that starts with "{".
func lastJSONLine(data []byte) []byte {
	var last []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if p, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
			last = p
		} else if bytes.HasPrefix(line, []byte("{")) {
			last = line
		}
	}
	if last == nil {
		return data
	}
	return last
}

// Message is one inbox entry, as fetch_inbox returns it.
type Message struct {
	ID          int64  `json:"id"`
	Subject     string `json:"subject"`
	From        string `json:"from"`
	ThreadID    string `json:"thread_id,omitempty"`
	CreatedTS   string `json:"created_ts"`
	Importance  string `json:"importance"`
	AckRequired bool   `json:"ack_required"`
	AckTS       string `json:"ack_ts,omitempty"`
	ReadTS      string `json:"read_ts,omitempty"`
}

// ErrNotRegistered means Agent Mail does not know the agent in that
// project.
var ErrNotRegistered = errors.New("agentmail: agent not registered")

// FetchInbox peeks at an agent's unread mail without marking it read.
func (c *Client) FetchInbox(ctx context.Context, projectKey, agent string, limit int) ([]Message, error) {
	var msgs []Message
	err := c.Call(ctx, "fetch_inbox", map[string]any{"project_key": projectKey, "agent_name": agent,
		"unread_only": true, "mark_read": false, "limit": limit, "include_bodies": false}, &msgs)
	var te *ToolError
	if errors.As(err, &te) && strings.Contains(strings.ToLower(te.Message), "not found") {
		return nil, fmt.Errorf("%w: %s in %s", ErrNotRegistered, agent, projectKey)
	}
	return msgs, err
}

// Unread sums up an inbox: unread messages, urgent ones, and acks that
// are still owed.
type Unread struct {
	Unread      int `json:"unread"`
	Urgent      int `json:"urgent"`
	PendingAcks int `json:"pending_acks"`
}

// Summarize counts an inbox peek.
func Summarize(msgs []Message) Unread {
	var u Unread
	for _, m := range msgs {
		if m.ReadTS == "" {
			u.Unread++
		}
		if m.Importance == "urgent" || m.Importance == "high" {
			u.Urgent++
		}
		if m.AckRequired && m.AckTS == "" {
			u.PendingAcks++
		}
	}
	return u
}
