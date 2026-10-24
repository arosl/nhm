// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cm/client.go
// (Client, NewClient, NewPortClient, rpc, callTool, GetContext, Health,
// RecordOutcome, ContextResult, Rule, HistorySnippet, PIDFileInfo) and
// internal/cli/memory.go (findSessionID) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: the pid files are under
// <project>/.nhm/pids, not .ntm/pids (the operator's ruling, 2026-10-24:
// nhm keeps its own paths); the daemon's rule and snippet types are named
// DaemonRule and DaemonSnippet beside this package's CLI types; the HTTP
// client is a field, so tests can give their own.
package cm

import (
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

// PIDFileInfo is the pid file that `nhm memory serve` writes for its cm
// daemon: <project>/.nhm/pids/cm-<session>.pid.
type PIDFileInfo struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	OwnerID   string    `json:"owner_id"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
}

// PIDsDir and LogsDir are where `nhm memory serve` keeps its pid and log
// files, under the project directory.
func PIDsDir(projectDir string) string { return filepath.Join(projectDir, ".nhm", "pids") }
func LogsDir(projectDir string) string { return filepath.Join(projectDir, ".nhm", "logs") }

// FindSessionID is the session of the memory daemon that runs for the
// project directory: the first cm-<session>.pid in its pids directory, as
// ntm finds it.
func FindSessionID(projectDir string) (string, error) {
	entries, err := os.ReadDir(PIDsDir(projectDir))
	if err != nil {
		return "", fmt.Errorf("could not find .nhm/pids in current directory (run from project root): %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) > 3 && name[:3] == "cm-" && name[len(name)-4:] == ".pid" {
			return name[3 : len(name)-4], nil
		}
	}
	return "", errors.New("no running memory daemon found in current directory")
}

// Daemon talks to a `cm serve` daemon. The daemon speaks MCP JSON-RPC
// over HTTP at its root path: every call is a JSON-RPC method (tools/list)
// or a tools/call (cm_context, cm_outcome).
type Daemon struct {
	BaseURL   string
	SessionID string
	HTTP      *http.Client
}

// NewDaemon finds the daemon of a session by its pid file.
func NewDaemon(projectDir, sessionID string) (*Daemon, error) {
	data, err := os.ReadFile(filepath.Join(PIDsDir(projectDir), fmt.Sprintf("cm-%s.pid", sessionID)))
	if err != nil {
		return nil, fmt.Errorf("reading cm pid file: %w", err)
	}
	var info PIDFileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing cm pid file: %w", err)
	}
	return NewPortDaemon(info.Port, sessionID), nil
}

// NewPortDaemon is the client of a daemon on a localhost port. sessionID
// may be empty for a context query or a health probe; RecordOutcome needs
// it.
func NewPortDaemon(port int, sessionID string) *Daemon {
	return &Daemon{
		BaseURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		SessionID: sessionID,
		HTTP:      &http.Client{Timeout: 10 * time.Second},
	}
}

// ContextResult is ntm's answer of a context query: what `nhm memory
// context` prints. The cm_context tool gives more fields per rule and
// snippet; ntm keeps these.
type ContextResult struct {
	Task             string          `json:"task,omitempty"`
	RelevantBullets  []DaemonRule    `json:"relevantBullets"`
	AntiPatterns     []DaemonRule    `json:"antiPatterns"`
	HistorySnippets  []DaemonSnippet `json:"historySnippets"`
	SuggestedQueries []string        `json:"suggestedCassQueries"`
}

// DaemonRule is a playbook rule, as ntm reads it: its identity and text.
type DaemonRule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

// DaemonSnippet is a CASS hit that a context query found, in cm's wire
// schema.
type DaemonSnippet struct {
	SourcePath string  `json:"source_path"`
	LineNumber int     `json:"line_number"`
	Agent      string  `json:"agent"`
	Workspace  string  `json:"workspace,omitempty"`
	Title      string  `json:"title,omitempty"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score,omitempty"`
	CreatedAt  int64   `json:"created_at,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpc makes one MCP JSON-RPC call at the daemon's root. A JSON-RPC error
// is an error, also under HTTP 200, and so is an answer with no result.
func (d *Daemon) rpc(ctx context.Context, method string, params any, result any) error {
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal cm %s request: %w", method, err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := d.HTTP.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cm %s failed: %s", method, resp.Status)
	}
	var r rpcResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&r); err != nil {
		return fmt.Errorf("decode cm %s response: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("cm %s failed (%d): %s", method, r.Error.Code, r.Error.Message)
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return fmt.Errorf("cm %s returned no result", method)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(r.Result, result)
}

// toolEnvelope is MCP's tools/call result. Older cm releases answer the
// tool's payload as the result itself; callTool takes both.
type toolEnvelope struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callTool runs an MCP tool and decodes its JSON payload into result. The
// envelope is known by its members, not their values: an envelope with no
// content is an empty answer, never a payload.
func (d *Daemon) callTool(ctx context.Context, name string, arguments any, result any) error {
	var raw json.RawMessage
	if err := d.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &raw); err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	isEnvelope := false
	if json.Unmarshal(raw, &probe) == nil {
		_, hasContent := probe["content"]
		_, hasIsError := probe["isError"]
		isEnvelope = hasContent || hasIsError
	}
	var env toolEnvelope
	if isEnvelope && json.Unmarshal(raw, &env) == nil {
		var text string
		for _, c := range env.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				text = c.Text
				break
			}
		}
		if env.IsError {
			if text != "" {
				return fmt.Errorf("cm tool %s failed: %s", name, text)
			}
			return fmt.Errorf("cm tool %s failed", name)
		}
		if result == nil {
			return nil
		}
		if text == "" {
			return fmt.Errorf("cm tool %s returned no text payload", name)
		}
		if err := json.Unmarshal([]byte(text), result); err != nil {
			return fmt.Errorf("decode cm tool %s payload: %w", name, err)
		}
		return nil
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("decode cm tool %s result: %w", name, err)
	}
	return nil
}

// GetContext asks the daemon for a task's rules (cm_context). A workspace
// scopes them to one project, so projects with the same base name share
// no rules.
func (d *Daemon) GetContext(ctx context.Context, task, workspace string) (*ContextResult, error) {
	args := map[string]any{"task": task}
	if strings.TrimSpace(workspace) != "" {
		args["workspace"] = workspace
	}
	var result ContextResult
	if err := d.callTool(ctx, "cm_context", args, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Health checks that the daemon answers (tools/list, which every MCP cm
// release has).
func (d *Daemon) Health(ctx context.Context) error {
	return d.rpc(ctx, "tools/list", nil, nil)
}

// Outcome statuses, ntm's words.
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomePartial = "partial"
)

// RecordOutcome records a session's outcome for the rules it used
// (cm_outcome). The tool needs the session id; ntm's "partial" is cm's
// "mixed".
func (d *Daemon) RecordOutcome(ctx context.Context, status string, ruleIDs []string) error {
	if strings.TrimSpace(d.SessionID) == "" {
		return errors.New("cm outcome requires a session ID")
	}
	outcome := status
	if status == OutcomePartial {
		outcome = "mixed"
	}
	args := map[string]any{"sessionId": d.SessionID, "outcome": outcome}
	if len(ruleIDs) > 0 {
		args["rulesUsed"] = ruleIDs
	}
	return d.callTool(ctx, "cm_outcome", args, nil)
}
