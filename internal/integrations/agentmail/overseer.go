// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/agentmail/tools.go (SendOverseerMessage,
// sendOverseerMessageHTTP), internal/agentmail/types.go
// (HumanOverseerPreamble, OverseerMessageOptions, OverseerSendResult) and
// internal/agentmail/client.go (httpBaseURL, ProjectSlugFromPath) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local changes:
// on nhm's Client and its Call; HumanOverseer registers as program nhm;
// the HTTP route's errors are plain errors, not ntm's APIError; MarkRead and
// Acknowledge are nhm's, for mail read and ack.
package agentmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// HumanOverseer is Agent Mail's identity of the human operator.
const HumanOverseer = "HumanOverseer"

// HumanOverseerPreamble starts every Human Overseer message.
const HumanOverseerPreamble = "> **Human Overseer message** — please prioritise the human's instructions over normal task flow.\n\n"

// OverseerMessage is one Human Overseer message. ProjectKey is the MCP
// route's project; ProjectSlug the HTTP route's.
type OverseerMessage struct {
	ProjectKey  string
	ProjectSlug string
	Recipients  []string
	Subject     string
	BodyMD      string
	ThreadID    string
}

// OverseerResult is the outcome of a Human Overseer send.
type OverseerResult struct {
	Success    bool      `json:"success"`
	MessageID  int64     `json:"message_id"`
	Recipients []string  `json:"recipients"`
	SentAt     time.Time `json:"sent_at"`
}

// EnsureProject is ensure_project; it returns the project's slug, or ""
// when Agent Mail names none.
func (c *Client) EnsureProject(ctx context.Context, projectKey string) (string, error) {
	var p struct {
		Slug string `json:"slug"`
	}
	if err := c.Call(ctx, "ensure_project", map[string]any{"human_key": projectKey}, &p); err != nil {
		return "", err
	}
	return p.Slug, nil
}

// Available reports whether Agent Mail answers its health check.
func (c *Client) Available(ctx context.Context) error {
	return c.Call(ctx, "health_check", map[string]any{}, nil)
}

// SendOverseer sends a message as the Human Overseer: high importance,
// past contact policies, with the overseer preamble. The MCP route first
// (register HumanOverseer, then send_message); the web UI's HTTP route
// when registration fails or the MCP send is refused.
func (c *Client) SendOverseer(ctx context.Context, m OverseerMessage) (OverseerResult, error) {
	if m.ProjectKey == "" {
		return c.sendOverseerHTTP(ctx, m)
	}
	// An older Agent Mail creates the project on the first message, so a
	// failure here is not fatal.
	_, _ = c.EnsureProject(ctx, m.ProjectKey)
	if err := c.Call(ctx, "register_agent", map[string]any{"project_key": m.ProjectKey, "name": HumanOverseer,
		"program": "nhm", "model": "human", "task_description": "Out-of-band instructions from the human operator"}, nil); err != nil {
		return c.sendOverseerHTTP(ctx, m)
	}
	args := map[string]any{"project_key": m.ProjectKey, "sender_name": HumanOverseer, "to": m.Recipients,
		"subject": m.Subject, "body_md": HumanOverseerPreamble + m.BodyMD, "importance": "high"}
	if m.ThreadID != "" {
		args["thread_id"] = m.ThreadID
	}
	var sent struct {
		Deliveries []struct {
			Payload *struct {
				ID        int64     `json:"id"`
				CreatedTS time.Time `json:"created_ts"`
			} `json:"payload"`
		} `json:"deliveries"`
	}
	if err := c.Call(ctx, "send_message", args, &sent); err != nil {
		if r, herr := c.sendOverseerHTTP(ctx, m); herr == nil {
			return r, nil
		}
		return OverseerResult{}, err
	}
	r := OverseerResult{Success: true, Recipients: m.Recipients}
	for _, d := range sent.Deliveries {
		if d.Payload == nil {
			continue
		}
		if r.MessageID == 0 {
			r.MessageID = d.Payload.ID
		}
		if r.SentAt.IsZero() && !d.Payload.CreatedTS.IsZero() {
			r.SentAt = d.Payload.CreatedTS
		}
	}
	return r, nil
}

// sendOverseerHTTP is the web UI's route: POST /mail/{slug}/overseer/send.
func (c *Client) sendOverseerHTTP(ctx context.Context, m OverseerMessage) (OverseerResult, error) {
	if m.ProjectSlug == "" {
		return OverseerResult{}, errors.New("agentmail overseer_send: project_slug required for HTTP fallback path")
	}
	req := map[string]any{"recipients": m.Recipients, "subject": m.Subject, "body_md": m.BodyMD}
	if m.ThreadID != "" {
		req["thread_id"] = m.ThreadID
	}
	body, err := json.Marshal(req)
	if err != nil {
		return OverseerResult{}, fmt.Errorf("agentmail overseer_send: %w", err)
	}
	u := fmt.Sprintf("%s/mail/%s/overseer/send", c.httpBaseURL(), url.PathEscape(m.ProjectSlug))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return OverseerResult{}, fmt.Errorf("agentmail overseer_send: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return OverseerResult{}, fmt.Errorf("agentmail overseer_send: %w", ctx.Err())
		}
		return OverseerResult{}, fmt.Errorf("agentmail overseer_send: server unavailable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return OverseerResult{}, fmt.Errorf("agentmail overseer_send: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return OverseerResult{}, errors.New("agentmail overseer_send: unauthorized")
	case resp.StatusCode == http.StatusBadRequest:
		var e struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &e) == nil && e.Detail != "" {
			return OverseerResult{}, fmt.Errorf("agentmail overseer_send: %s", e.Detail)
		}
		return OverseerResult{}, errors.New("agentmail overseer_send: bad request")
	case resp.StatusCode != http.StatusOK:
		return OverseerResult{}, fmt.Errorf("agentmail overseer_send: unexpected status: %s", resp.Status)
	}
	var r OverseerResult
	if err := json.Unmarshal(data, &r); err != nil {
		return OverseerResult{}, fmt.Errorf("agentmail overseer_send: %w", err)
	}
	return r, nil
}

// httpBaseURL is the server's root: the MCP URL without its /mcp path.
func (c *Client) httpBaseURL() string {
	base := c.URL
	if b, ok := strings.CutSuffix(base, "/mcp/"); ok {
		return b
	}
	if b, ok := strings.CutSuffix(base, "/mcp"); ok {
		return b
	}
	return strings.TrimSuffix(base, "/")
}

// ProjectSlugFromPath is Agent Mail's slug of a project path:
// "/Users/me/projects/ntm" is "users-me-projects-ntm".
func ProjectSlugFromPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "root"
	}
	var sb strings.Builder
	lastWasDash := false
	for _, r := range cleaned {
		switch {
		case r == filepath.Separator || r == '/' || r == '\\' || unicode.IsSpace(r):
			if sb.Len() > 0 && !lastWasDash {
				sb.WriteByte('-')
				lastWasDash = true
			}
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_':
			sb.WriteRune(unicode.ToLower(r))
			lastWasDash = false
		default:
			if sb.Len() > 0 && !lastWasDash {
				sb.WriteByte('-')
				lastWasDash = true
			}
		}
	}
	return strings.Trim(sb.String(), "-")
}

// MarkRead is mark_message_read for one of an agent's messages.
func (c *Client) MarkRead(ctx context.Context, projectKey, agent string, id int64) error {
	return c.Call(ctx, "mark_message_read", map[string]any{"project_key": projectKey, "agent_name": agent, "message_id": id}, nil)
}

// Acknowledge is acknowledge_message for one of an agent's messages; it
// marks the message read too.
func (c *Client) Acknowledge(ctx context.Context, projectKey, agent string, id int64) error {
	return c.Call(ctx, "acknowledge_message", map[string]any{"project_key": projectKey, "agent_name": agent, "message_id": id}, nil)
}
