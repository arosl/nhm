package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// killWorld is waitInstance's swarm (IcyKnoll in w1:p1, BlueLake in w1:p2,
// a shell in w1:p3) with nhm as the owner, more workspaces, pane labels,
// a fake Agent Mail and records of what closed.
type killWorld struct {
	mu        sync.Mutex
	wsClosed  []string
	paneClose []string
	released  []string
	asked     []string
}

type killWS struct{ id, label string }

func newKillWorld(t *testing.T, labels map[string]string, extra ...killWS) *killWorld {
	t.Helper()
	k := &killWorld{}
	s, inst, _ := waitInstance(t, func(pane string) map[string]any {
		if pane == "w1:p1" {
			return agentMap(pane, "claude", "working", 4, 0)
		}
		return agentMap(pane, "codex", "idle", 7, 7)
	})
	workspaces := []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
		"pane_count": 3, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}}
	for i, w := range extra {
		workspaces = append(workspaces, map[string]any{"workspace_id": w.id, "number": i + 2, "label": w.label, "focused": false,
			"pane_count": 1, "tab_count": 1, "active_tab_id": w.id + ":t1", "agent_status": "idle"})
	}
	pane := func(id, ws string) map[string]any {
		return map[string]any{"pane_id": id, "tab_id": ws + ":t1", "workspace_id": ws, "terminal_id": "t-" + id,
			"label": labels[id], "agent_status": "idle", "focused": false, "revision": 1}
	}
	panes := []any{pane("w1:p1", "w1"), pane("w1:p2", "w1"), pane("w1:p3", "w1")}
	for _, w := range extra {
		panes = append(panes, pane(w.id+":p1", w.id))
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": panes, "agents": []any{},
			"workspaces": workspaces}}, nil
	})
	record := func(list *[]string) func(r herdrtest.Request) (any, *herdrtest.Error) {
		return func(r herdrtest.Request) (any, *herdrtest.Error) {
			var p map[string]any
			_ = json.Unmarshal(r.Params, &p)
			k.mu.Lock()
			for _, key := range []string{"workspace_id", "pane_id", "target"} {
				if v, ok := p[key]; ok {
					*list = append(*list, fmt.Sprint(v))
				}
			}
			k.mu.Unlock()
			return map[string]any{"type": "ok"}, nil
		}
	}
	s.Handle("workspace.close", record(&k.wsClosed))
	s.Handle("pane.close", record(&k.paneClose))
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		if call.Params.Name == "release_file_reservations" {
			k.mu.Lock()
			k.released = append(k.released, fmt.Sprint(call.Params.Arguments["agent_name"]))
			k.mu.Unlock()
		}
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"isError": false, "content": []any{map[string]any{"type": "text", "text": `{"released":0}`}}}})
		_, _ = w.Write(res)
	}))
	t.Cleanup(mail.Close)
	t.Setenv("AGENT_MAIL_URL", mail.URL)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	t.Setenv("HOME", t.TempDir())
	oldReaper, oldConfirm := killReaper, confirmKill
	killReaper = func() lifecycle.Reaper { return nil }
	confirmKill = func(_ io.Writer, title, desc string) bool {
		t.Fatalf("asked %q %q", title, desc)
		return false
	}
	t.Cleanup(func() { killReaper, confirmKill = oldReaper, oldConfirm })
	ownNhm(t, inst)
	return k
}

// answer makes the confirm record its question and answer yes.
func (k *killWorld) answer(yes bool) {
	confirmKill = func(_ io.Writer, title, desc string) bool {
		k.mu.Lock()
		k.asked = append(k.asked, title+" | "+desc)
		k.mu.Unlock()
		return yes
	}
}

func (k *killWorld) closed() (ws, panes, released []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	r := append([]string(nil), k.released...)
	slices.Sort(r)
	return append([]string(nil), k.wsClosed...), append([]string(nil), k.paneClose...), r
}

// TestKillAsksOnATTY: without -f ntm asks first; no is "Aborted." and
// exit 0, and nothing closes; yes kills the session, a working agent
// too, and releases the agents' reservations.
func TestKillAsksOnATTY(t *testing.T) {
	k := newKillWorld(t, nil)
	k.answer(false)
	if code, out, errOut := runVerb(verbRun("kill"), "swarm"); code != exitOK || out != "Aborted.\n" {
		t.Fatalf("no = %d %q %q", code, out, errOut)
	}
	if ws, panes, rel := k.closed(); len(ws)+len(panes)+len(rel) != 0 {
		t.Fatalf("closed %v %v %v", ws, panes, rel)
	}
	k.answer(true)
	if code, out, errOut := runVerb(verbRun("kill"), "swarm"); code != exitOK || out != "Killed session 'swarm'\n" {
		t.Fatalf("yes = %d %q %q", code, out, errOut)
	}
	ws, _, rel := k.closed()
	if !slices.Equal(ws, []string{"w1"}) || !slices.Equal(rel, []string{"BlueLake", "IcyKnoll"}) {
		t.Fatalf("closed %v, released %v", ws, rel)
	}
	if want := "Kill session 'swarm'? | This will terminate 3 running agent(s)."; len(k.asked) != 2 || k.asked[0] != want {
		t.Fatalf("asked %q", k.asked)
	}
}

// TestKillReadsYesFromStdin: without a terminal, ntm's "[y/N]" prompt;
// only y or yes is yes.
func TestKillReadsYesFromStdin(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, " YES \n": true, "n\n": false, "": false, "yep\n": false} {
		var out strings.Builder
		if got := confirmSimple(&out, strings.NewReader(in), "Kill session 'swarm'?"); got != want || out.String() != "Kill session 'swarm'? [y/N]: " {
			t.Errorf("%q = %v %q, want %v", in, got, out.String(), want)
		}
	}
}

// TestKillForceAndJSONNeedNoConfirm: -f, --force and --json never ask.
// --json prints ntm's KillResponse.
func TestKillForceAndJSONNeedNoConfirm(t *testing.T) {
	for _, flag := range []string{"-f", "--force"} {
		_ = newKillWorld(t, nil)
		if code, out, errOut := runVerb(verbRun("kill"), "swarm", flag); code != exitOK || out != "Killed session 'swarm'\n" {
			t.Fatalf("%s = %d %q %q", flag, code, out, errOut)
		}
	}
	_ = newKillWorld(t, nil)
	code, out, _ := runVerb(verbRun("kill"), "swarm", "--json")
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil || code != exitOK {
		t.Fatalf("--json = %d %s %v", code, out, err)
	}
	if raw["session"] != "swarm" || raw["killed"] != true || raw["message"] != "Killed session 'swarm'" || raw["generated_at"] == nil {
		t.Fatalf("KillResponse = %v", raw)
	}
	if _, ok := raw["summary"]; ok {
		t.Fatalf("summary without --summarize: %v", raw)
	}
}

// TestKillTag: --tag closes only the panes whose label ends in a tag and
// keeps the session; no match is ntm's line, exit 0.
func TestKillTag(t *testing.T) {
	k := newKillWorld(t, map[string]string{"w1:p1": "IcyKnoll opus[ui]", "w1:p2": "BlueLake[api]"})
	k.answer(true)
	if code, out, errOut := runVerb(verbRun("kill"), "swarm", "--tag", "UI"); code != exitOK || out != "Killed 1 pane(s) matching tags\n" {
		t.Fatalf("--tag ui = %d %q %q", code, out, errOut)
	}
	ws, panes, rel := k.closed()
	if len(ws) != 0 || !slices.Equal(panes, []string{"w1:p1"}) || len(rel) != 0 {
		t.Fatalf("closed %v %v, released %v", ws, panes, rel)
	}
	if want := "Kill 1 pane(s)? | This will terminate panes matching tags [UI] in session 'swarm'."; len(k.asked) != 1 || k.asked[0] != want {
		t.Fatalf("asked %q", k.asked)
	}
	if code, out, _ := runVerb(verbRun("kill"), "swarm", "--tag", "nope"); code != exitOK || out != "No panes found matching tags.\n" {
		t.Fatalf("no match = %d %q", code, out)
	}
	code, out, _ := runVerb(verbRun("kill"), "swarm", "--tag", "nope", "--json")
	if !strings.Contains(out, `"killed": false`) || !strings.Contains(out, `"message": "No panes found matching tags"`) || code != exitOK {
		t.Fatalf("no match, --json = %d %s", code, out)
	}
}

// TestKillProject: -p kills every session whose base name is P, after one
// confirm; none is ntm's error, and so is a session with -p.
func TestKillProject(t *testing.T) {
	k := newKillWorld(t, nil, killWS{"w2", "proj"}, killWS{"w3", "proj--ui"}, killWS{"w4", "other"})
	k.answer(true)
	code, out, errOut := runVerb(verbRun("kill"), "-p", "proj")
	if code != exitOK || out != "Killed session 'proj'\nKilled session 'proj--ui'\n" {
		t.Fatalf("-p proj = %d %q %q", code, out, errOut)
	}
	if ws, _, _ := k.closed(); !slices.Equal(ws, []string{"w2", "w3"}) {
		t.Fatalf("closed %v", ws)
	}
	if want := "Kill 2 session(s)? | Sessions: proj, proj--ui"; len(k.asked) != 1 || k.asked[0] != want {
		t.Fatalf("asked %q", k.asked)
	}
	if code, _, errOut := runVerb(verbRun("kill"), "--project", "none", "-f"); code != exitError || errOut != "Error: no sessions found for project \"none\"\n" {
		t.Fatalf("none = %d %q", code, errOut)
	}
	if code, _, errOut := runVerb(verbRun("kill"), "swarm", "-p", "proj"); code != exitError || errOut != "Error: cannot use --project with a specific session name\n" {
		t.Fatalf("both = %d %q", code, errOut)
	}
}

// TestKillPaneAndSummarizeWait: ntm's argument checks, then --pane and
// --summarize refuse until their engines exist; nothing closes.
func TestKillPaneAndSummarizeWait(t *testing.T) {
	k := newKillWorld(t, nil)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "--pane", "1"}, "Error: --pane is not in this nhm yet (deferred: nhm-2p6.11)\n"},
		{[]string{"swarm", "--pane", "1", "--tag", "ui"}, "Error: --pane cannot be combined with --project, --tag, or --summarize\n"},
		{[]string{"--pane", "1"}, "Error: session name required with --pane\n"},
		{[]string{"swarm", "--summarize"}, "Error: --summarize is not in this nhm yet (deferred: nhm-2p6.8)\n"},
		{nil, "Error: session name or --project required\n"},
	} {
		if code, _, errOut := runVerb(verbRun("kill"), c.args...); code != exitError || errOut != c.want {
			t.Errorf("%v = %d %q, want %q", c.args, code, errOut, c.want)
		}
	}
	if code, out, _ := runVerb(verbRun("kill"), "swarm", "--summarize", "--json"); code != robotNotImplemented || !strings.Contains(out, `"error_code": "NOT_IMPLEMENTED"`) {
		t.Fatalf("--summarize --json = %d %s", code, out)
	}
	if ws, panes, _ := k.closed(); len(ws)+len(panes) != 0 {
		t.Fatalf("closed %v %v", ws, panes)
	}
}

// TestKillErrorsAreNTMs: a session that does not resolve is ntm's error,
// or under --json its failure envelope on one line.
func TestKillErrorsAreNTMs(t *testing.T) {
	_ = newKillWorld(t, nil)
	if code, _, errOut := runVerb(verbRun("kill"), "nosuch", "-f"); code != exitError || !strings.HasPrefix(errOut, "Error: ") {
		t.Fatalf("no such session = %d %q", code, errOut)
	}
	code, out, errOut := runVerb(verbRun("kill"), "nosuch", "--json")
	if code != exitError || errOut != "" || !strings.HasPrefix(out, `{"success":false,"error":"`) || strings.Count(out, "\n") != 1 {
		t.Fatalf("--json = %d %q %q", code, out, errOut)
	}
}
