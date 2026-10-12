package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/robot/contract"
	"github.com/arosl/nhm/internal/store"
)

// swarmInstance serves a session with workspace "swarm" (w1: IcyKnoll
// idle and bound, a blocked unbound codex) and "other" (w2: empty), and
// binds IcyKnoll in an enrolled instance.
func swarmInstance(t *testing.T) *herdrtest.Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	t.Setenv("ACFS_HOME", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	s := herdrtest.New(t)
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, s.SocketPath, "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p",
		ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude", Model: "opus"}})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	ws := func(id, label string, n int) map[string]any {
		return map[string]any{"workspace_id": id, "number": n, "label": label, "focused": n == 1, "pane_count": 2,
			"tab_count": 2, "active_tab_id": id + ":t1", "agent_status": "idle"}
	}
	agent := func(pane, kind, name, status string) map[string]any {
		a := map[string]any{"pane_id": pane, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t-" + pane,
			"agent_status": status, "focused": false, "revision": 1, "agent": kind, "state_change_seq": 2}
		if name != "" {
			a["name"] = name
		}
		return a
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{},
			"workspaces": []any{ws("w1", "swarm", 1), ws("w2", "other", 2)},
			"agents":     []any{agent("w1:p1", "claude", "icyknoll", "idle"), agent("w1:p2", "codex", "", "blocked")},
		}}, nil
	})
	read := func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID + p.Target, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3,
			"text": "\x1b[1mDone.\x1b[0m\n❯ \n", "truncated": false}}, nil
	}
	s.Handle("pane.read", read)
	s.Handle("agent.read", read)
	return s
}

func envelopeOf(t *testing.T, out string) map[string]any {
	t.Helper()
	env := decodeEnvelope(t, out)
	if env["error"] != nil {
		t.Fatalf("error = %v", env["error"])
	}
	return env
}

func TestStatusVerb(t *testing.T) {
	s := swarmInstance(t)
	code, out, errOut := run("status", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "IcyKnoll") || !strings.Contains(out, "0.1") || !strings.Contains(out, "blocked") {
		t.Fatalf("status = %d\n%s%s", code, out, errOut)
	}
	// Agent Mail is down: the mail column says "?", and a warning says why.
	if !strings.Contains(out, "?") || !strings.Contains(errOut, "agent_mail is unavailable") {
		t.Fatalf("mail down: %s / %s", out, errOut)
	}
	code, out, _ = run("status", "swarm", "--no-mail", "--json", "--socket", s.SocketPath)
	env := envelopeOf(t, out)
	agents := env["result"].(map[string]any)["agents"].([]any)
	if code != 0 || len(agents) != 2 {
		t.Fatalf("json = %d %v", code, agents)
	}
	if code, out, _ := run("status", "nosuch", "--no-mail", "--socket", s.SocketPath); code != 1 {
		t.Fatalf("unknown session = %d %s", code, out)
	}
	if code, out, _ := run("status", "other", "--no-mail", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "no agents") {
		t.Fatalf("empty session = %d %s", code, out)
	}
}

func TestStatusWithMail(t *testing.T) {
	s := swarmInstance(t)
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text := `[{"id":5,"subject":"x","from":"BlueLake","created_ts":"2026-10-12T04:00:00Z","importance":"urgent","ack_required":true}]`
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		_, _ = w.Write(res)
	}))
	defer mail.Close()
	t.Setenv("AGENT_MAIL_URL", mail.URL)
	code, out, errOut := run("status", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "1/1 ack") || errOut != "" {
		t.Fatalf("status with mail = %d\n%s%s", code, out, errOut)
	}
}

func TestStatusWatchCount(t *testing.T) {
	s := swarmInstance(t)
	code, out, errOut := run("status", "--watch", "--count", "2", "--interval", "50ms", "--no-mail", "--socket", s.SocketPath)
	if code != 0 || strings.Count(out, "--- ") != 2 {
		t.Fatalf("watch = %d\n%s%s", code, out, errOut)
	}
}

func TestListVerb(t *testing.T) {
	s := swarmInstance(t)
	code, out, _ := run("list", "--json", "--socket", s.SocketPath)
	agents := envelopeOf(t, out)["result"].(map[string]any)["agents"].([]any)
	if code != 0 || len(agents) != 1 || agents[0].(map[string]any)["binding"].(map[string]any)["mail_name"] != "IcyKnoll" {
		t.Fatalf("list = %d %v", code, agents)
	}
}

func TestSnapshotAndAttention(t *testing.T) {
	s := swarmInstance(t)
	code, out, _ := run("snapshot", "--no-mail", "--socket", s.SocketPath)
	res := envelopeOf(t, out)["result"].(map[string]any)
	if code != 0 || res["model"] == nil || len(res["attention"].([]any)) == 0 {
		t.Fatalf("snapshot = %d %v", code, res)
	}
	code, out, _ = run("attention", "--jsonl", "--no-mail", "--socket", s.SocketPath)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var first map[string]any
	if code != 0 || json.Unmarshal([]byte(lines[0]), &first) != nil || first["kind"] != "agent_blocked" {
		t.Fatalf("attention = %d %s", code, out)
	}
}

func TestActivityAndHealth(t *testing.T) {
	s := swarmInstance(t)
	code, out, _ := run("activity", "swarm", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "WAITING") || !strings.Contains(out, "STALLED") {
		t.Fatalf("activity = %d %s", code, out)
	}
	if code, _, _ := run("activity", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("activity without a session = %d", code)
	}
	code, out, _ = run("health", "--no-mail", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, "overall: critical") {
		t.Fatalf("health with a blocked agent = %d %s", code, out)
	}
}

func TestReadVerb(t *testing.T) {
	s := swarmInstance(t)
	raw := filepath.Join(t.TempDir(), "raw.txt")
	code, out, errOut := run("read", "IcyKnoll", "--raw-to", raw, "--socket", s.SocketPath)
	if code != 0 || out != "Done.\n❯ \n" {
		t.Fatalf("read = %d %q %s", code, out, errOut)
	}
	if data, _ := os.ReadFile(raw); !strings.Contains(string(data), "\x1b[1m") {
		t.Fatalf("raw = %q", data)
	}
	if got := s.Requests(); got[len(got)-1].Method != "agent.read" || !strings.Contains(string(got[len(got)-1].Params), `"w1:p1"`) {
		t.Fatalf("last request = %+v", got[len(got)-1])
	}
	code, _, _ = run("read", "w1:p2", "--source", "recent", "--socket", s.SocketPath)
	if got := s.Requests(); code != 0 || got[len(got)-1].Method != "pane.read" {
		t.Fatalf("recent read = %d %+v", code, got[len(got)-1])
	}
	if code, _, _ := run("read", "--source", "bogus", "x"); code != 2 {
		t.Fatalf("bad source = %d", code)
	}
}

func TestWatchVerb(t *testing.T) {
	s := swarmInstance(t)
	code, out, errOut := run("watch", "swarm", "IcyKnoll", "--count", "2", "--interval", "10ms", "--socket", s.SocketPath)
	// The second round sees the same text: nothing new is printed.
	if code != 0 || strings.Count(out, "IcyKnoll | Done.") != 1 {
		t.Fatalf("watch = %d\n%s%s", code, out, errOut)
	}
}

func TestNewLines(t *testing.T) {
	for _, c := range []struct{ prev, cur, want string }{
		{"", "a,b", "a,b"},
		{"a,b", "a,b", ""},
		{"a,b", "b,c", "c"},
		{"a,b,c", "b,c,d,e", "d,e"},
		{"x", "a,b", "a,b"},
	} {
		split := func(s string) []string {
			if s == "" {
				return nil
			}
			return strings.Split(s, ",")
		}
		if got := strings.Join(newLines(split(c.prev), split(c.cur)), ","); got != c.want {
			t.Errorf("newLines(%s, %s) = %s, want %s", c.prev, c.cur, got, c.want)
		}
	}
}

func TestStripControl(t *testing.T) {
	if got := stripControl("\x1b[31mred\x1b[0m\x1b]0;title\x07 ok\r\n\ttab\x00"); got != "red ok\n\ttab" {
		t.Fatalf("stripControl = %q", got)
	}
}

func TestDoctor(t *testing.T) {
	s := swarmInstance(t)
	code, out, _ := run("doctor", "--no-mail", "--json", "--socket", s.SocketPath)
	checks := envelopeOf(t, out)["result"].(map[string]any)["checks"].([]any)
	results := map[string]string{}
	for _, c := range checks {
		m := c.(map[string]any)
		results[m["name"].(string)] = m["result"].(string)
	}
	if code != 0 || results["herdr"] != "ok" || results["enrollment"] != "ok" || results["database"] != "ok" || results["owner"] != "warn" {
		t.Fatalf("doctor = %d %v", code, results)
	}
	code, out, _ = run("doctor", "--no-mail", "--socket", filepath.Join(t.TempDir(), "none.sock"))
	if code != 1 || !strings.Contains(out, "fail herdr") {
		t.Fatalf("doctor without herdr = %d %s", code, out)
	}
}

func TestCapabilitiesAndCompatibility(t *testing.T) {
	code, out, _ := run("capabilities", "--json")
	forms := envelopeOf(t, out)["result"].(map[string]any)["forms"].([]any)
	if code != 0 || len(forms) < 20 {
		t.Fatalf("capabilities = %d %d", code, len(forms))
	}
	code, out, _ = run("compatibility", "status")
	if code != 0 || !strings.Contains(out, "implemented") || !strings.Contains(out, "deferred") {
		t.Fatalf("compatibility = %d %s", code, out)
	}
	if code, _, _ := run("compatibility"); code != 2 {
		t.Fatalf("compatibility without status = %d", code)
	}
}

// robotRun runs a robot flag and checks ntm's envelope contract.
func robotRun(t *testing.T, args ...string) (*contract.Envelope, int) {
	t.Helper()
	code, out, errOut := run(args...)
	o := &contract.Outcome{Command: "nhm " + strings.Join(args, " "), Stdout: []byte(out), Stderr: []byte(errOut), ExitCode: code}
	if code == 0 {
		return contract.AssertSuccessEnvelope(t, o), code
	}
	env, err := contract.ParseEnvelope(o)
	if err != nil {
		t.Fatal(err)
	}
	return env, code
}

func TestRobotStatus(t *testing.T) {
	s := swarmInstance(t)
	env, _ := robotRun(t, "--robot-status", "--no-mail", "--socket", s.SocketPath)
	contract.AssertCriticalArrayPresent(t, env, "sessions")
	sessions := env.Raw["sessions"].([]any)
	first := sessions[0].(map[string]any)
	if env.Raw["schema_id"] != "ntm:robot:status:v1" || first["name"] != "swarm" || first["agent_count"].(float64) != 2 {
		t.Fatalf("status = %v", env.Raw)
	}
}

func TestRobotSnapshot(t *testing.T) {
	s := swarmInstance(t)
	env, _ := robotRun(t, "--robot-snapshot", "--no-mail", "--socket", s.SocketPath)
	for _, f := range []string{"sessions", "active_incidents", "alerts"} {
		contract.AssertCriticalArrayPresent(t, env, f)
	}
	agents := env.Raw["sessions"].([]any)[0].(map[string]any)["agents"].([]any)
	if agents[0].(map[string]any)["pane"] != "0.1" {
		t.Fatalf("snapshot agents = %v", agents)
	}
}

func TestRobotActivity(t *testing.T) {
	s := swarmInstance(t)
	env, _ := robotRun(t, "--robot-activity=swarm", "--socket", s.SocketPath)
	contract.AssertCriticalArrayPresent(t, env, "agents")
	if env.Raw["session"] != "swarm" {
		t.Fatalf("activity = %v", env.Raw)
	}
	env, code := robotRun(t, "--robot-activity=ghost", "--socket", s.SocketPath)
	if code != 1 || env.ErrorCode != "SESSION_NOT_FOUND" {
		t.Fatalf("ghost = %d %+v", code, env)
	}
}

func TestRobotTail(t *testing.T) {
	s := swarmInstance(t)
	env, _ := robotRun(t, "--robot-tail=swarm", "--lines=5", "--socket", s.SocketPath)
	panes := env.Raw["panes"].(map[string]any)
	lines := panes["0.1"].(map[string]any)["lines"].([]any)
	if len(lines) != 2 || lines[0] != "Done." {
		t.Fatalf("tail = %v", panes)
	}
}

func TestRobotCapabilities(t *testing.T) {
	env, _ := robotRun(t, "--robot-capabilities")
	contract.AssertCriticalArrayPresent(t, env, "commands")
	unavailable := map[string]bool{}
	for _, c := range env.Raw["commands"].([]any) {
		m := c.(map[string]any)
		if m["unavailable"] == true {
			if m["unavailable_reason"] == "" {
				t.Errorf("%v has no reason", m["flag"])
			}
			unavailable[m["flag"].(string)] = true
		}
	}
	if !unavailable["--robot-send"] || unavailable["--robot-status"] {
		t.Fatalf("unavailable = %v", unavailable)
	}
}

func TestRobotFormat(t *testing.T) {
	env, code := robotRun(t, "--robot-capabilities", "--robot-format=toon")
	if code != 1 || env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("toon = %d %+v", code, env)
	}
	env, code = robotRun(t, "--robot-send=swarm", "--msg=hi")
	if code != 2 || env.ErrorCode != "NOT_IMPLEMENTED" || !strings.Contains(env.Hint, "nhm.send") {
		t.Fatalf("send = %d %+v", code, env)
	}
	env, code = robotRun(t, "--robot-status", "--robot-snapshot")
	if code != 1 || env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("two flags = %d %+v", code, env)
	}
	t.Setenv("HERDR_SOCKET_PATH", "")
	env, code = robotRun(t, "--robot-status")
	if code != 2 || env.ErrorCode != "DEPENDENCY_MISSING" {
		t.Fatalf("no herdr = %d %+v", code, env)
	}
}
