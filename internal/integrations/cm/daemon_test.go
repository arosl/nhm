package cm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The session of a project's memory daemon is the first cm-<session>.pid
// in its .nhm/pids, as ntm finds it; ntm's errors otherwise.
func TestFindSessionID(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindSessionID(dir); err == nil || !strings.Contains(err.Error(), "could not find .nhm/pids in current directory (run from project root)") {
		t.Fatalf("no pids directory: %v", err)
	}
	if err := os.MkdirAll(PIDsDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bd-x.pid", "cm-x.log", ".daemon-1.lock"} {
		if err := os.WriteFile(filepath.Join(PIDsDir(dir), name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FindSessionID(dir); err == nil || err.Error() != "no running memory daemon found in current directory" {
		t.Fatalf("no cm pid file: %v", err)
	}
	for _, name := range []string{"cm-memory-serve-9.pid", "cm-memory-serve-12.pid"} {
		if err := os.WriteFile(filepath.Join(PIDsDir(dir), name), []byte(`{"port":8211}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id, err := FindSessionID(dir)
	if err != nil || id != "memory-serve-12" {
		t.Fatalf("session = %q, %v; want the first by name", id, err)
	}
	d, err := NewDaemon(dir, id)
	if err != nil || d.BaseURL != "http://127.0.0.1:8211" || d.SessionID != id {
		t.Fatalf("NewDaemon = %+v, %v", d, err)
	}
	if _, err := NewDaemon(dir, "nosuch"); err == nil || !strings.HasPrefix(err.Error(), "reading cm pid file: ") {
		t.Fatalf("missing pid file: %v", err)
	}
}

// mcpFake answers tools/call with answer(name, arguments) as the JSON-RPC
// result, and records each call.
type mcpFake struct {
	mu    sync.Mutex
	calls []map[string]any
}

func newMCPFake(t *testing.T, answer func(name string, args map[string]any) string) (*Daemon, *mcpFake) {
	t.Helper()
	f := &mcpFake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.calls = append(f.calls, map[string]any{"method": req.Method, "name": req.Params.Name, "arguments": req.Params.Arguments})
		f.mu.Unlock()
		result := `{"tools":[]}`
		if req.Method == "tools/call" {
			result = answer(req.Params.Name, req.Params.Arguments)
		}
		if strings.HasPrefix(result, "ERROR ") {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"`+strings.TrimPrefix(result, "ERROR ")+`"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":`+result+`}`)
	}))
	t.Cleanup(srv.Close)
	return &Daemon{BaseURL: srv.URL, SessionID: "memory-serve-7", HTTP: srv.Client()}, f
}

func textEnvelope(t *testing.T, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}, "isError": false})
	return string(env)
}

// cm_context answers in MCP's envelope (cm 0.5) or as the result itself
// (older cm); ntm keeps only its fields of each rule and snippet.
func TestDaemonGetContext(t *testing.T) {
	payload := map[string]any{
		"task":                 "fix auth",
		"relevantBullets":      []any{map[string]any{"id": "b-1", "content": "Check the token", "category": "auth", "finalScore": 6.3, "maturity": "candidate"}},
		"antiPatterns":         []any{},
		"historySnippets":      []any{map[string]any{"source_path": "/s.jsonl", "line_number": 4, "agent": "claude", "snippet": "x", "extra": true}},
		"suggestedCassQueries": []any{"auth timeout"},
	}
	want := &ContextResult{Task: "fix auth",
		RelevantBullets:  []DaemonRule{{ID: "b-1", Content: "Check the token", Category: "auth"}},
		AntiPatterns:     []DaemonRule{},
		HistorySnippets:  []DaemonSnippet{{SourcePath: "/s.jsonl", LineNumber: 4, Agent: "claude", Snippet: "x"}},
		SuggestedQueries: []string{"auth timeout"}}
	direct, _ := json.Marshal(payload)
	for name, answer := range map[string]string{"envelope": textEnvelope(t, payload), "direct": string(direct)} {
		d, f := newMCPFake(t, func(string, map[string]any) string { return answer })
		got, err := d.GetContext(context.Background(), "fix auth", "/data/p")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: context = %+v, %v", name, got, err)
		}
		args := f.calls[0]["arguments"].(map[string]any)
		if f.calls[0]["name"] != "cm_context" || args["task"] != "fix auth" || args["workspace"] != "/data/p" {
			t.Fatalf("%s: call = %v", name, f.calls[0])
		}
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(got)
		if strings.Contains(out.String(), "finalScore") || strings.Contains(out.String(), "extra") {
			t.Fatalf("%s: ntm's fields only: %s", name, out.String())
		}
	}
	d, f := newMCPFake(t, func(string, map[string]any) string { return textEnvelope(t, payload) })
	if _, err := d.GetContext(context.Background(), "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.calls[0]["arguments"].(map[string]any)["workspace"]; ok {
		t.Fatalf("an empty workspace is sent: %v", f.calls[0])
	}
}

// Every way a tool can fail is an error: a JSON-RPC error under HTTP 200,
// an isError envelope, an envelope with no text, a null result.
func TestDaemonToolErrors(t *testing.T) {
	for answer, want := range map[string]string{
		"ERROR bad session": "cm tools/call failed (-32000): bad session",
		`{"content":[{"type":"text","text":"boom"}],"isError":true}`: "cm tool cm_context failed: boom",
		`{"content":[],"isError":false}`:                             "cm tool cm_context returned no text payload",
		`null`:                                                       "cm tools/call returned no result",
		`{"content":[{"type":"text","text":"not json"}]}`:            "decode cm tool cm_context payload",
	} {
		d, _ := newMCPFake(t, func(string, map[string]any) string { return answer })
		if _, err := d.GetContext(context.Background(), "t", ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("answer %s: err = %v, want %q", answer, err, want)
		}
	}
	d, _ := newMCPFake(t, nil)
	if err := d.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
}

// cm_outcome gets the session, cm's word for ntm's partial (mixed), and the
// rules when there are any.
func TestDaemonRecordOutcome(t *testing.T) {
	d, f := newMCPFake(t, func(string, map[string]any) string { return textEnvelope(t, map[string]any{"ok": true}) })
	if err := d.RecordOutcome(context.Background(), OutcomePartial, []string{"b-1", "b-2"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordOutcome(context.Background(), OutcomeSuccess, nil); err != nil {
		t.Fatal(err)
	}
	first, second := f.calls[0]["arguments"].(map[string]any), f.calls[1]["arguments"].(map[string]any)
	if f.calls[0]["name"] != "cm_outcome" || first["sessionId"] != "memory-serve-7" || first["outcome"] != "mixed" ||
		!reflect.DeepEqual(first["rulesUsed"], []any{"b-1", "b-2"}) {
		t.Fatalf("partial = %v", first)
	}
	if _, ok := second["rulesUsed"]; ok || second["outcome"] != "success" {
		t.Fatalf("success = %v", second)
	}
	d.SessionID = ""
	if err := d.RecordOutcome(context.Background(), OutcomeSuccess, nil); err == nil || err.Error() != "cm outcome requires a session ID" {
		t.Fatalf("no session: %v", err)
	}
}

// `cm privacy` runs in the directory with the caller's words, and its
// output passes through.
func TestPrivacyPassesThrough(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\"\npwd\necho note >&2\n[ \"$2\" = deny ] && exit 4\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if err := Privacy(context.Background(), bin, dir, &out, &errOut, "enable", "--json", "GreenLake"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "privacy enable --json GreenLake\n"+dir+"\n" || errOut.String() != "note\n" {
		t.Fatalf("out %q, stderr %q", out.String(), errOut.String())
	}
	if err := Privacy(context.Background(), bin, dir, io.Discard, io.Discard, "deny", "BlueCat", "--json"); err == nil || err.Error() != "exit status 4" {
		t.Fatalf("cm's exit: %v", err)
	}
}
