package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// listWorld is a herdr server with four workspaces: swarm (focused, two
// tabs: IcyKnoll's Claude, tagged frontend, BlueLake's Codex and a
// shell), proj--api (a Claude that nhm has not bound), proj (a shell) and
// w4 (no label, no pane). With enrolled, nhm has enrolled the server and
// bound IcyKnoll and BlueLake, both of project /p.
func listWorld(t *testing.T, enrolled bool, workspaces ...map[string]any) *herdrtest.Server {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PANE_ID", "")
	s := herdrtest.New(t)
	ws := func(id, label string, focused bool, tabs int) map[string]any {
		return map[string]any{"workspace_id": id, "number": 1, "label": label, "focused": focused,
			"pane_count": 0, "tab_count": tabs, "active_tab_id": id + ":t1", "agent_status": "idle"}
	}
	pane := func(id, agent, label string) map[string]any {
		p := map[string]any{"pane_id": id, "tab_id": strings.Split(id, ":")[0] + ":t1", "workspace_id": strings.Split(id, ":")[0],
			"terminal_id": "t-" + id, "label": label, "agent_status": "idle", "focused": false, "revision": 1}
		if agent != "" {
			p["agent"] = agent
		}
		return p
	}
	if workspaces == nil {
		workspaces = []map[string]any{ws("w1", "swarm", true, 2), ws("w2", "proj--api", false, 1), ws("w3", "proj", false, 1), ws("w4", "", false, 1)}
	}
	panes := []any{pane("w1:p1", "claude", "IcyKnoll opus[frontend]"), pane("w1:p2", "codex", "BlueLake gpt"), pane("w1:p3", "", ""),
		pane("w2:p1", "claude", ""), pane("w3:p1", "", "")}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": panes, "agents": []any{},
			"workspaces": workspaces}}, nil
	})
	if !enrolled {
		return s
	}
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
	defer db.Close()
	for _, b := range []struct{ name, pane, kind string }{{"IcyKnoll", "w1:p1", "claude"}, {"BlueLake", "w1:p2", "codex"}} {
		if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: b.name, MailProjectKey: "/p",
			ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
				PaneID: b.pane, HerdrAlias: strings.ToLower(b.name), Kind: b.kind}}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// listJSON runs args with --json and decodes ntm's ListResponse.
func listJSON(t *testing.T, args ...string) (listResponse, map[string]any) {
	t.Helper()
	code, out, errOut := run(append(args, "--json")...)
	if code != 0 || errOut != "" {
		t.Fatalf("%v = %d\n%s%s", args, code, out, errOut)
	}
	var resp listResponse
	var keys map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	_ = json.Unmarshal([]byte(out), &keys)
	return resp, keys
}

func listNames(resp listResponse) []string {
	var names []string
	for _, s := range resp.Sessions {
		names = append(names, s.Name)
	}
	return names
}

// ntm's ListResponse: every workspace as a session, sorted by base
// project, then unlabeled first, then label; the windows are the tabs,
// attached the focus, the agents counted by type over every pane.
func TestListNTMSessionRows(t *testing.T) {
	s := listWorld(t, true)
	resp, keys := listJSON(t, "list", "--socket", s.SocketPath)
	if got := listNames(resp); !slices.Equal(got, []string{"proj", "proj--api", "swarm", "w4"}) {
		t.Fatalf("sessions = %v", got)
	}
	for _, k := range []string{"generated_at", "sessions", "count", "total_matches", "has_more"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("no %q in %v", k, keys)
		}
	}
	if resp.Count != 4 || resp.TotalMatches != 4 || resp.HasMore || resp.Pagination != nil || resp.AgentHints != nil || resp.GeneratedAt.IsZero() {
		t.Errorf("resp = %+v", resp)
	}
	swarm := resp.Sessions[2]
	want := agentCountsResponse{Claude: 1, Codex: 1, User: 1, Total: 3}
	if swarm.BaseProject != "swarm" || swarm.Label != "" || swarm.Windows != 2 || swarm.PaneCount != 3 || !swarm.Attached ||
		swarm.WorkingDirectory != "/p" || swarm.AgentCounts == nil || *swarm.AgentCounts != want {
		t.Errorf("swarm = %+v %+v", swarm, swarm.AgentCounts)
	}
	api := resp.Sessions[1]
	if api.BaseProject != "proj" || api.Label != "api" || api.Attached || api.WorkingDirectory != "" ||
		api.AgentCounts == nil || *api.AgentCounts != (agentCountsResponse{Claude: 1, Total: 1}) {
		t.Errorf("proj--api = %+v %+v", api, api.AgentCounts)
	}
	// A workspace without a pane has no agent counts, as a tmux session
	// without panes has none.
	if w4 := resp.Sessions[3]; w4.BaseProject != "w4" || w4.PaneCount != 0 || w4.AgentCounts != nil {
		t.Errorf("w4 = %+v", w4)
	}
	var raw struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal([]byte(mustRun(t, "list", "--socket", s.SocketPath, "--json")), &raw)
	if _, ok := raw.Sessions[3]["agents"]; ok {
		t.Errorf("w4 has agents: %v", raw.Sessions[3])
	}
	if _, ok := raw.Sessions[0]["label"]; ok {
		t.Errorf("proj has a label: %v", raw.Sessions[0])
	}
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := run(args...)
	if code != 0 {
		t.Fatalf("%v = %d\n%s%s", args, code, out, errOut)
	}
	return out
}

// Without nhm's enrolment, list shows herdr's view: herdr's agents, and no
// folder.
func TestListOnAServerNotEnrolled(t *testing.T) {
	s := listWorld(t, false)
	resp, _ := listJSON(t, "list", "--socket", s.SocketPath)
	swarm := resp.Sessions[2]
	if swarm.Name != "swarm" || swarm.WorkingDirectory != "" || swarm.AgentCounts == nil ||
		*swarm.AgentCounts != (agentCountsResponse{Claude: 1, Codex: 1, User: 1, Total: 3}) {
		t.Errorf("swarm = %+v %+v", swarm, swarm.AgentCounts)
	}
}

// The table on a terminal of 100 columns or more, with PROJECT and LABEL
// when a session has a label; one line per session on a narrower one or a
// pipe.
func TestListTextWideAndPiped(t *testing.T) {
	s := listWorld(t, true)
	width := 0
	old := listWidth
	listWidth = func(io.Writer) int { return width }
	t.Cleanup(func() { listWidth = old })

	width = 120
	row := func(cells ...string) string {
		return fmt.Sprintf("%-12s%-10s%-8s%-10s%-11s%s\n", cells[0], cells[1], cells[2], cells[3], cells[4], cells[5])
	}
	want := row("SESSION", "PROJECT", "LABEL", "WINDOWS", "STATE", "AGENTS") +
		row("proj", "proj", "-", "1", "detached", "1 Usr") +
		row("proj--api", "proj", "api", "1", "detached", "1 CC") +
		row("swarm", "swarm", "-", "2", "attached", "1 CC, 1 COD, 1 Usr") +
		row("w4", "w4", "-", "1", "detached", "-")
	if out := mustRun(t, "list", "--socket", s.SocketPath); out != want {
		t.Errorf("wide =\n%s\nwant\n%s", out, want)
	}
	// No label: no PROJECT and LABEL columns.
	want = fmt.Sprintf("%-10s%-10s%-11s%s\n", "SESSION", "WINDOWS", "STATE", "AGENTS") +
		fmt.Sprintf("%-10s%-10s%-11s%s\n", "swarm", "2", "attached", "1 CC, 1 COD, 1 Usr")
	if out := mustRun(t, "list", "-p", "swarm", "--socket", s.SocketPath); out != want {
		t.Errorf("wide, no labels =\n%s\nwant\n%s", out, want)
	}

	width = 99
	want = "  proj: 1 windows\n  proj--api: 1 windows\n  swarm: 2 windows (attached)\n  w4: 1 windows\n"
	if out := mustRun(t, "list", "--socket", s.SocketPath); out != want {
		t.Errorf("narrow = %q, want %q", out, want)
	}
}

func TestListNoSessions(t *testing.T) {
	s := listWorld(t, true, []map[string]any{}...)
	if out := mustRun(t, "list", "--socket", s.SocketPath); out != "No herdr sessions running\n" {
		t.Errorf("text = %q", out)
	}
	resp, keys := listJSON(t, "list", "--socket", s.SocketPath)
	if resp.Count != 0 || resp.TotalMatches != 0 || keys["sessions"] == nil {
		t.Errorf("json = %+v %v", resp, keys)
	}
}

// --tag keeps a session where a pane's label ends in one of the tags,
// case-insensitively; --project (-p) keeps one base project's sessions.
func TestListTagAndProjectFilters(t *testing.T) {
	s := listWorld(t, true)
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"--tag", "frontend"}, []string{"swarm"}},
		{[]string{"--tag", "FRONTEND"}, []string{"swarm"}},
		{[]string{"--tag", "api,backend"}, nil},
		{[]string{"-p", "proj"}, []string{"proj", "proj--api"}},
		{[]string{"--project", "proj--api"}, nil},
		{[]string{"--project=swarm", "--tag", "frontend"}, []string{"swarm"}},
	} {
		resp, _ := listJSON(t, append([]string{"list", "--socket", s.SocketPath}, c.args...)...)
		if got := listNames(resp); !slices.Equal(got, c.want) || resp.Count != len(c.want) {
			t.Errorf("%v = %v (count %d), want %v", c.args, got, resp.Count, c.want)
		}
	}
}

// --limit and --offset page the list, with ntm's pagination and its hint.
func TestListPagination(t *testing.T) {
	s := listWorld(t, true)
	resp, keys := listJSON(t, "list", "--socket", s.SocketPath, "--limit", "2", "--offset", "1")
	if got := listNames(resp); !slices.Equal(got, []string{"proj--api", "swarm"}) {
		t.Fatalf("page = %v", got)
	}
	p := resp.Pagination
	if resp.Count != 2 || resp.TotalMatches != 4 || !resp.HasMore || p == nil || p.Limit != 2 || p.Offset != 1 || p.Total != 4 ||
		p.NextCursor == nil || *p.NextCursor != 3 || resp.AgentHints == nil || resp.AgentHints.NextOffset == nil || *resp.AgentHints.NextOffset != 3 {
		t.Errorf("resp = %+v %+v %+v", resp, p, resp.AgentHints)
	}
	if _, ok := keys["_agent_hints"]; !ok {
		t.Errorf("no _agent_hints: %v", keys)
	}
	resp, _ = listJSON(t, "list", "--socket", s.SocketPath, "--offset", "3")
	if got := listNames(resp); !slices.Equal(got, []string{"w4"}) || resp.HasMore || resp.AgentHints != nil || resp.TotalMatches != 4 {
		t.Errorf("last page = %v %+v", got, resp)
	}
}

// ls and l are list, and ntm's list ignores extra arguments.
func TestListAliases(t *testing.T) {
	s := listWorld(t, true)
	for _, args := range [][]string{{"ls"}, {"l"}, {"list", "extra", "words"}} {
		resp, _ := listJSON(t, append(args, "--socket", s.SocketPath)...)
		if got := listNames(resp); len(got) != 4 {
			t.Errorf("%v = %v", args, got)
		}
	}
}

// An error is "Error: <msg>", exit 1; under --json ntm's error envelope on
// one line, exit 1.
func TestListErrors(t *testing.T) {
	listWorld(t, true)
	const msg = "no herdr socket: give --socket or run inside herdr (HERDR_SOCKET_PATH)"
	if code, out, errOut := run("list"); code != 1 || out != "" || errOut != "Error: "+msg+"\n" {
		t.Errorf("text = %d %q %q", code, out, errOut)
	}
	if code, out, errOut := run("list", "--json"); code != 1 || errOut != "" || out != `{"success":false,"error":"`+msg+`"}`+"\n" {
		t.Errorf("json = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := run("list", "--limit", "x"); code != 1 || !strings.HasPrefix(errOut, `Error: invalid argument "x" for "--limit" flag`) {
		t.Errorf("bad flag = %d %q", code, errOut)
	}
}

// attach without a session runs ntm's list.
func TestAttachWithoutASessionRunsList(t *testing.T) {
	s := listWorld(t, true)
	resp, _ := listJSON(t, "attach", "--socket", s.SocketPath)
	if got := listNames(resp); len(got) != 4 {
		t.Errorf("attach = %v", got)
	}
}
