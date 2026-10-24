package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// As ntm's: bare memory, a word that is no sub-verb, and bare memory
// privacy print the help and exit 0 (the operator's ruling, 2026-10-24:
// nhm's memory <query> goes).
func TestMemoryBarePrintsHelp(t *testing.T) {
	for _, args := range [][]string{{"memory"}, {"memory", "fix", "the", "login"}, {"memory", "--help"}} {
		if code, out, errOut := run(args...); code != 0 || out != memoryUsage || errOut != "" {
			t.Fatalf("%v = %d %q %q", args, code, out, errOut)
		}
	}
	code, out, _ := run("memory", "privacy")
	if code != 0 || !strings.Contains(out, "Manage privacy controls for cross-agent enrichment") || !strings.Contains(out, "deny        Remove an agent") {
		t.Fatalf("memory privacy = %d %q", code, out)
	}
	// A sub-verb that shares a verb's name gets its own help.
	code, out, _ = run("memory", "privacy", "status", "--help")
	if code != 0 || !strings.Contains(out, "Show cross-agent privacy settings") || strings.Contains(out, statusUsage) {
		t.Fatalf("memory privacy status --help = %d %q", code, out)
	}
	if code, _, errOut := run("memory", "--no-such-flag"); code != exitError || !strings.Contains(errOut, "unknown flag: --no-such-flag") {
		t.Fatalf("bad flag = %d %q", code, errOut)
	}
}

// context and outcome need a memory serve that runs in the cwd, and give
// ntm's errors without one.
func TestMemoryNeedsADaemon(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"memory", "context", "fix auth"}, "Error: could not find .nhm/pids in current directory (run from project root): "},
		{[]string{"memory", "outcome", "success"}, "Error: could not find .nhm/pids in current directory (run from project root): "},
		{[]string{"memory", "outcome", "maybe"}, "Error: invalid status: maybe\n"},
		{[]string{"memory", "context"}, "Error: accepts 1 arg(s), received 0\n"},
		{[]string{"memory", "context", "a", "b"}, "Error: accepts 1 arg(s), received 2\n"},
	} {
		if code, out, errOut := run(c.args...); code != exitError || out != "" || !strings.HasPrefix(errOut, c.want) {
			t.Errorf("%v = %d %q %q, want %q", c.args, code, out, errOut, c.want)
		}
	}
	if err := os.MkdirAll(".nhm/pids", 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run("memory", "context", "x"); code != exitError || errOut != "Error: no running memory daemon found in current directory\n" {
		t.Fatalf("empty pids = %d %q", code, errOut)
	}
}

// fakeMemoryDaemon is a cm daemon on a local port for the cwd's session
// s1: it records each tools/call and answers cm_context with ctxPayload.
type fakeMemoryDaemon struct {
	mu    sync.Mutex
	calls []map[string]any
}

const ctxPayload = `{"task":"fix auth","relevantBullets":[{"id":"b-1","content":"Check the token","category":"auth","finalScore":6.3}],` +
	`"antiPatterns":[],"historySnippets":[],"suggestedCassQueries":["auth"]}`

func newFakeMemoryDaemon(t *testing.T) *fakeMemoryDaemon {
	t.Helper()
	f := &fakeMemoryDaemon{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.calls = append(f.calls, req.Params)
		f.mu.Unlock()
		text, _ := json.Marshal(ctxPayload)
		if req.Params["name"] == "cm_outcome" {
			text, _ = json.Marshal(`{"recorded":true}`)
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%s}],"isError":false}}`, text)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	if err := os.MkdirAll(".nhm/pids", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".nhm/pids/cm-s1.pid", []byte(`{"pid":1,"port":`+u.Port()+`,"owner_id":"s1","command":"cm"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// context prints ntm's ContextResult: its fields of each rule, as JSON,
// for the task scoped to the cwd.
func TestMemoryContextPrintsJSON(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	f := newFakeMemoryDaemon(t)
	code, out, errOut := run("memory", "context", "fix auth")
	want := `{
  "task": "fix auth",
  "relevantBullets": [
    {
      "id": "b-1",
      "content": "Check the token",
      "category": "auth"
    }
  ],
  "antiPatterns": [],
  "historySnippets": [],
  "suggestedCassQueries": [
    "auth"
  ]
}
`
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("memory context = %d %q %q", code, out, errOut)
	}
	args := f.calls[0]["arguments"].(map[string]any)
	cwd, _ := os.Getwd()
	if f.calls[0]["name"] != "cm_context" || args["task"] != "fix auth" || args["workspace"] != cwd {
		t.Fatalf("call = %v", f.calls[0])
	}
}

// outcome sends cm_outcome for the daemon's session, cm's word for partial
// and the rules of every --rules, and prints nothing.
func TestMemoryOutcome(t *testing.T) {
	t.Chdir(t.TempDir())
	f := newFakeMemoryDaemon(t)
	code, out, errOut := run("memory", "outcome", "partial", "--rules", "b-1,b-2", "--rules=b-3")
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("memory outcome = %d %q %q", code, out, errOut)
	}
	args := f.calls[0]["arguments"].(map[string]any)
	if f.calls[0]["name"] != "cm_outcome" || args["sessionId"] != "s1" || args["outcome"] != "mixed" ||
		!reflect.DeepEqual(args["rulesUsed"], []any{"b-1", "b-2", "b-3"}) {
		t.Fatalf("call = %v", f.calls[0])
	}
}

// privacy runs `cm privacy <verb> … --json` in the cwd and passes cm's
// output and failure through.
func TestMemoryPrivacyPassesThrough(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	bin := filepath.Join(t.TempDir(), "cm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\"\n[ \"$2\" = deny ] && { echo denied >&2; exit 4; }\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := memoryCMCommand
	memoryCMCommand = []string{bin}
	t.Cleanup(func() { memoryCMCommand = old })
	for args, want := range map[string]string{
		"status":                 "privacy status --json\n",
		"enable":                 "privacy enable --json\n",
		"enable GreenLake Other": "privacy enable --json GreenLake Other\n",
		"disable":                "privacy disable --json\n",
		"allow GreenLake":        "privacy allow GreenLake --json\n",
	} {
		if code, out, errOut := run(append([]string{"memory", "privacy"}, strings.Fields(args)...)...); code != 0 || out != want || errOut != "" {
			t.Errorf("privacy %s = %d %q %q, want %q", args, code, out, errOut, want)
		}
	}
	if code, out, errOut := run("memory", "privacy", "deny", "BlueCat"); code != exitError || out != "privacy deny BlueCat --json\n" ||
		errOut != "denied\nError: exit status 4\n" {
		t.Fatalf("deny = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := run("memory", "privacy", "allow"); code != exitError || errOut != "Error: accepts 1 arg(s), received 0\n" {
		t.Fatalf("allow with no agent = %d %q", code, errOut)
	}
}

// fakeCMServeEnv makes the test binary a fake `cm serve` (TestHelperFakeCMServe):
// it answers MCP initialize and cm_context on --port.
const fakeCMServeEnv = "NHM_CLI_FAKE_CM_SERVE"

func TestHelperFakeCMServe(t *testing.T) {
	if os.Getenv(fakeCMServeEnv) == "" {
		t.Skip("the fake cm serve of TestMemoryServe")
	}
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(4)
	}
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"initialize"`)) {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"cass-memory"}}}`)
			return
		}
		text, _ := json.Marshal(ctxPayload)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%s}]}}`, text)
	}))
	os.Exit(5)
}

// memory serve supervises cm serve in the foreground: ntm's lines, a pid
// file that context finds, and a clean stop when its context ends.
func TestMemoryServe(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(fakeCMServeEnv, "1")
	oldCmd, oldCtx, oldPoll := memoryCMCommand, memoryServeContext, memoryServePoll
	t.Cleanup(func() { memoryCMCommand, memoryServeContext, memoryServePoll = oldCmd, oldCtx, oldPoll })
	memoryCMCommand = []string{os.Args[0], "-test.run=^TestHelperFakeCMServe$", "--"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	memoryServeContext = func() context.Context { return ctx }
	memoryServePoll = 20 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	type result struct {
		code        int
		out, errOut string
	}
	done := make(chan result, 1)
	go func() {
		code, out, errOut := run("memory", "serve", "--port", fmt.Sprint(port))
		done <- result{code, out, errOut}
	}()
	// The pid file comes at launch; the fake listens a moment later.
	pidFile := fmt.Sprintf(".nhm/pids/cm-memory-serve-%d.pid", os.Getpid())
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(pidFile); err == nil && bytes.Contains(data, []byte(fmt.Sprintf(`"port": %d`, port))) {
			if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
				_ = c.Close()
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			r := <-done
			t.Fatalf("no pid file: %d %q %q", r.code, r.out, r.errOut)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// context finds the daemon by its pid file and asks it. The serve
	// ends before any Fatalf, whose cleanups reset the hooks it reads.
	code, out, errOut := run("memory", "context", "fix auth")
	cancel()
	r := <-done
	if code != 0 || !strings.Contains(out, `"id": "b-1"`) {
		t.Fatalf("context against serve = %d %q %q", code, out, errOut)
	}
	want := fmt.Sprintf("Supervising cm daemon (pid=%%d port=%d state=starting)\nLogs: %s\nPress Ctrl-C to stop.\n",
		port, filepath.Join(dir, ".nhm", "logs", fmt.Sprintf("cm-memory-serve-%d.log", os.Getpid())))
	var pid int
	if r.code != 0 || r.errOut != "" || !strings.HasSuffix(r.out, "Shutting down cm daemon...\ncm daemon stopped.\n") {
		t.Fatalf("serve = %d %q %q", r.code, r.out, r.errOut)
	}
	if _, err := fmt.Sscanf(r.out, "Supervising cm daemon (pid=%d", &pid); err != nil || !strings.HasPrefix(r.out, fmt.Sprintf(want, pid)) {
		t.Fatalf("serve's first lines: %q (want %q)", r.out, fmt.Sprintf(want, pid))
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("pid file left: %v", err)
	}

	// A busy port refuses with ntm's words; so does a cm that is not there.
	ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	code, _, errOut = run("memory", "serve", "--port", fmt.Sprint(port))
	if code != exitError || errOut != fmt.Sprintf("Error: start cm daemon: daemon cm default port %d is already in use; not starting a second instance on a random port\n", port) {
		t.Fatalf("busy port = %d %q", code, errOut)
	}
	memoryCMCommand = []string{"nhm-test-no-such-cm"}
	code, _, errOut = run("memory", "serve")
	if code != exitError || !strings.HasPrefix(errOut, "Error: cm binary not found in PATH: ") ||
		!strings.Contains(errOut, "then re-run 'nhm memory serve'") {
		t.Fatalf("no cm = %d %q", code, errOut)
	}
}
