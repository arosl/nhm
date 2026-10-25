package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

// assignReadTools installs a fake bv and br for --robot-assign in a new
// project: two ready beads (nhm-1, a P1 bug; nhm-2, a P2 refactor), one
// blocked bead (nhm-3, blocked by nhm-1) and one stale bead in progress.
// label, if not "", is nhm-2's label. br logs its arguments to the
// returned file.
func assignReadTools(t *testing.T, label string) (project, brLog string) {
	t.Helper()
	labels := `[]`
	if label != "" {
		labels = `["` + label + `"]`
	}
	project = t.TempDir()
	writeAll(t, filepath.Join(project, ".beads", "issues.jsonl"),
		`{"id":"nhm-1","title":"Fix the crash","status":"open","issue_type":"bug","priority":1}`+"\n"+
			`{"id":"nhm-2","title":"Refactor the store","status":"open","issue_type":"task","priority":2}`+"\n"+
			`{"id":"nhm-3","title":"Wait for the fix","status":"open","issue_type":"task","priority":2}`+"\n")
	bin := t.TempDir()
	brLog = filepath.Join(bin, "br.log")
	bvScript := `#!/bin/sh
case "$*" in
  *--robot-plan*) printf '%s\n' '{"plan":{"tracks":[{"track_id":"one","items":[{"id":"nhm-1","title":"Fix the crash","status":"open","priority":1},{"id":"nhm-2","title":"Refactor the store","status":"open","priority":2}]}],"summary":{"total_actionable":2}}}' ;;
  *--robot-triage*) printf '%s\n' '{"triage":{"recommendations":[{"id":"nhm-1","title":"Fix the crash","type":"bug","status":"open","priority":1,"score":0.9,"unblocks_ids":["nhm-3"]},{"id":"nhm-2","title":"Refactor the store","type":"task","status":"open","priority":2,"score":0.5,"labels":` + labels + `}]}}' ;;
  *) exit 1 ;;
esac
`
	brScript := `#!/bin/sh
echo "$*" >> ` + brLog + `
case " $* " in
  *--status=in_progress*) printf '%s\n' '[{"id":"nhm-9","title":"Old work","assignee":"BlueLake","updated_at":"2026-10-01T00:00:00Z"}]' ;;
  *' blocked '*) printf '%s\n' '[{"id":"nhm-3","title":"Wait for the fix"}]' ;;
  *' show '*) printf '%s\n' '[{"id":"nhm-3","title":"Wait for the fix","issue_type":"task","status":"open","priority":2,"dependencies":[{"id":"nhm-1","title":"Fix the crash","status":"open","priority":1,"dependency_type":"blocks"}]}]' ;;
  *' ready '*|*' list '*) printf '%s\n' '[{"id":"nhm-1","title":"Fix the crash","status":"open","issue_type":"bug","priority":1,"labels":[]},{"id":"nhm-2","title":"Refactor the store","status":"open","issue_type":"task","priority":2,"labels":` + labels + `}]' ;;
  *) printf '%s\n' '{}' ;;
esac
`
	writeAll(t, filepath.Join(bin, "bv"), bvScript)
	writeAll(t, filepath.Join(bin, "br"), brScript)
	for _, name := range []string{"bv", "br"} {
		if err := os.Chmod(filepath.Join(bin, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return project, brLog
}

// assignSession serves workspace "swarm" (w1) with three agents of
// project: IcyKnoll (claude) idle, BlueLake (codex) working and RedFox
// (codex) idle, all bound. It returns their bindings by Mail name.
func assignSession(t *testing.T, project string) (*herdrtest.Server, store.Instance, map[string]bindings.Binding) {
	t.Helper()
	s, inst := cliInstance(t)
	agents := map[string]map[string]any{
		"w1:p1": agentMap("w1:p1", "claude", "idle", 4, 4),
		"w1:p2": agentMap("w1:p2", "codex", "working", 7, 0),
		"w1:p3": agentMap("w1:p3", "codex", "idle", 5, 5),
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{},
			"workspaces": []any{
				map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
					"pane_count": 3, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"},
				map[string]any{"workspace_id": "w2", "number": 2, "label": "empty", "focused": false,
					"pane_count": 1, "tab_count": 1, "active_tab_id": "w2:t1", "agent_status": "idle"},
			},
		}}, nil
	})
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		a, ok := agents[p.Target]
		if !ok {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "no agent"}
		}
		return map[string]any{"type": "agent_info", "agent": a}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string]bindings.Binding{}
	for _, n := range []struct{ name, tab, pane, kind, model string }{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude", "opus"}, {"BlueLake", "w1:t1", "w1:p2", "codex", ""},
		{"RedFox", "w1:t2", "w1:p3", "codex", ""},
	} {
		b, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: n.name, MailProjectKey: project,
			ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: n.tab,
				PaneID: n.pane, HerdrAlias: strings.ToLower(n.name), Kind: n.kind, Model: n.model}})
		if err != nil {
			t.Fatal(err)
		}
		out[n.name] = b
	}
	return s, inst, out
}

// recommendationsByBead maps each recommended bead to its recommendation.
func recommendationsByBead(t *testing.T, out map[string]any) map[string]map[string]any {
	t.Helper()
	list, ok := out["recommendations"].([]any)
	if !ok {
		t.Fatalf("recommendations = %v", out["recommendations"])
	}
	by := map[string]map[string]any{}
	for _, r := range list {
		m := r.(map[string]any)
		by[m["assign_bead"].(string)] = m
	}
	return by
}

// --robot-assign=S answers ntm's AssignOutput (nhm-y55): the session's
// idle agents (the working one is not), its ready beads paired by the
// strategy, the blocked bead with its blocker, ntm's summary and hints.
// The agent types are ntm's short ones (cc, cod), as ntm's observer gives
// them. "simple" pairs in order; "quality" pairs by the capability matrix;
// --beads narrows the beads. It claims nothing, runs no br write, and
// makes no assignment ledger. An agent with an active assignment in the
// ledger is not idle; with none idle, the lists are [], never null.
func TestRobotAssignContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	project, brLog := assignReadTools(t, "")
	s, inst, bound := assignSession(t, project)
	sock := "--socket=" + s.SocketPath
	icy, red := bound["IcyKnoll"], bound["RedFox"]

	// The name is trimmed, as ntm's resolveRobotLiveSession trims it.
	out, code := robotJSON(t, "--robot-assign= swarm ", "--robot-format=json", sock)
	if code != robotOK || out["success"] != true || out["session"] != "swarm" || out["strategy"] != "simple" {
		t.Fatalf("simple = %d %v", code, out)
	}
	idle, _ := json.Marshal(out["idle_agents"])
	if string(idle) != `["w1:p1","w1:p3"]` {
		t.Fatalf("idle_agents = %s", idle)
	}
	by := recommendationsByBead(t, out)
	first, second := by["nhm-1"], by["nhm-2"]
	if len(by) != 2 || first["pane_id"] != "w1:p1" || first["pane_target"] != icy.Slot || first["agent_type"] != "cc" ||
		first["model"] != "opus" || first["priority"] != "P1" || first["bead_title"] != "Fix the crash" ||
		!strings.Contains(first["reasoning"].(string), "cc excels at bug tasks; high priority; simple sequential pairing") ||
		second["pane_id"] != "w1:p3" || second["pane_target"] != red.Slot || second["agent_type"] != "cod" {
		t.Fatalf("simple recommendations = %v", out["recommendations"])
	}
	var typed robot.AssignOutput
	raw, _ := json.Marshal(out)
	if err := json.Unmarshal(raw, &typed); err != nil {
		t.Fatal(err)
	}
	if want := []robot.BlockedBead{{ID: "nhm-3", Title: "Wait for the fix", BlockedBy: []string{"nhm-1"}}}; !reflect.DeepEqual(typed.BlockedBeads, want) {
		t.Fatalf("blocked_beads = %+v", typed.BlockedBeads)
	}
	if want := (robot.AssignSummary{TotalAgents: 3, IdleAgents: 2, WorkingAgents: 1, ReadyBeads: 2, BlockedBeads: 1,
		Recommendations: 2}); typed.Summary != want {
		t.Fatalf("summary = %+v", typed.Summary)
	}
	hints := out["_agent_hints"].(map[string]any)
	if hints["summary"] != "2 assignments recommended for 2 idle agents" || hints["suggested_commands"] != nil ||
		!strings.Contains(strings.Join(anyStrings(hints["warnings"]), "\n"), "1 in-progress beads are stale") {
		t.Fatalf("hints = %v", hints)
	}

	// quality: the bug to the codex agent, the refactor to the claude one.
	out, code = robotJSON(t, "--robot-assign=swarm", "--strategy=quality", sock)
	by = recommendationsByBead(t, out)
	if code != robotOK || out["strategy"] != "quality" || by["nhm-1"]["pane_id"] != "w1:p3" || by["nhm-2"]["pane_id"] != "w1:p1" {
		t.Fatalf("quality = %d %v", code, out["recommendations"])
	}

	// --beads narrows the ready beads; the session's workspace id works too.
	out, code = robotJSON(t, "--robot-assign=w1", "--beads=nhm-2", sock)
	by = recommendationsByBead(t, out)
	if code != robotOK || len(by) != 1 || by["nhm-2"]["pane_id"] != "w1:p1" {
		t.Fatalf("--beads = %d %v", code, out["recommendations"])
	}

	log, _ := os.ReadFile(brLog)
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		for _, write := range []string{" update ", " close ", " create ", " sync "} {
			if strings.Contains(" "+line+" ", write) {
				t.Fatalf("--robot-assign ran a br write: %q", line)
			}
		}
	}
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sessions")); !os.IsNotExist(err) {
		t.Fatalf("--robot-assign made the assignment ledger's directory: %v", err)
	}

	// An agent whose binding holds an active assignment in the instance's
	// ledger is not idle: the second ready bead then has no agent.
	ledger := assignment.NewStore(inst.ID)
	ledger.Assignments["nhm-7"] = &assignment.Assignment{BeadID: "nhm-7", Status: assignment.StatusWorking, OccupancyKey: red.ID}
	if err := ledger.Save(); err != nil {
		t.Fatal(err)
	}
	out, code = robotJSON(t, "--robot-assign=swarm", sock)
	by = recommendationsByBead(t, out)
	unassignable, _ := json.Marshal(out["unassignable_beads"])
	if code != robotOK || len(by) != 1 || by["nhm-1"]["pane_id"] != "w1:p1" ||
		string(unassignable) != `[{"id":"nhm-2","reason":"no idle agent available","title":"Refactor the store"}]` {
		t.Fatalf("occupied = %d %v %s", code, out["recommendations"], unassignable)
	}

	// No idle agent: recommendations and idle_agents are [], as ntm's
	// encoder writes a nil list.
	ledger.Assignments["nhm-8"] = &assignment.Assignment{BeadID: "nhm-8", Status: assignment.StatusWorking, OccupancyKey: icy.ID}
	if err := ledger.Save(); err != nil {
		t.Fatal(err)
	}
	code, text, _ := run("--robot-assign=swarm", sock)
	if code != robotOK || !strings.Contains(text, `"recommendations": []`) || !strings.Contains(text, `"idle_agents": []`) ||
		strings.Contains(text, "null") {
		t.Fatalf("none idle = %d %s", code, text)
	}
}

// The project's [assign] policy reaches the classifier: a bead with a
// label the project's .nhm/config.toml gates is unassignable, with ntm's
// reason; without that file it is ready. ntm's .ntm/config.toml plays no
// part (nhm-2p6.28.2).
func TestRobotAssignHonoursTheProjectsOperatorGates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	project, _ := assignReadTools(t, "release-approval")
	s, _, _ := assignSession(t, project)
	sock := "--socket=" + s.SocketPath
	gate := "[assign]\noperator_gated_labels = [\"release-approval\"]\n"
	writeAll(t, filepath.Join(project, ".ntm", "config.toml"), gate)
	out, code := robotJSON(t, "--robot-assign=swarm", sock)
	if by := recommendationsByBead(t, out); code != robotOK || len(by) != 2 {
		t.Fatalf("no gate = %d %v", code, out["recommendations"])
	}
	writeAll(t, filepath.Join(project, ".nhm", "config.toml"), gate)
	out, code = robotJSON(t, "--robot-assign=swarm", sock)
	unassignable, _ := json.Marshal(out["unassignable_beads"])
	if by := recommendationsByBead(t, out); code != robotOK || len(by) != 1 || by["nhm-1"] == nil ||
		string(unassignable) != `[{"id":"nhm-2","reason":"operator-gated label: release-approval","title":"Refactor the store"}]` {
		t.Fatalf("gated = %d %v %s", code, out["recommendations"], unassignable)
	}
	// A policy file that does not parse is refused before any read.
	writeAll(t, filepath.Join(project, ".nhm", "config.toml"), "[assign]\nunknown_key = 1\n")
	out, code = robotJSON(t, "--robot-assign=swarm", sock)
	if code != robotError || out["error_code"] != robot.ErrCodeInvalidFlag ||
		out["hint"] != "Fix [assign] in nhm's config and the project's .nhm/config.toml" {
		t.Fatalf("bad policy = %d %v", code, out)
	}
}

// ntm's refusals, in ntm's order: the session, its project, then the
// strategy; a missing bv is DEPENDENCY_MISSING. Each exits 1.
func TestRobotAssignRefusals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	project, _ := assignReadTools(t, "")
	s, _, _ := assignSession(t, project)
	sock := "--socket=" + s.SocketPath
	for _, tc := range []struct {
		args             []string
		code, says, hint string
	}{
		{[]string{"--robot-assign="}, robot.ErrCodeInvalidFlag, "session name is required", "Provide session name: nhm --robot-assign=myproject"},
		{[]string{"--robot-assign=  "}, robot.ErrCodeSessionNotFound, "session is required", "Use 'nhm list' to see available sessions"},
		{[]string{"--robot-assign=nowhere"}, robot.ErrCodeSessionNotFound, "nowhere", "Use 'nhm list' to see available sessions"},
		{[]string{"--robot-assign=empty"}, robot.ErrCodeInternalError, "getting project root failed",
			"Ensure the target session has a resolvable project directory"},
		{[]string{"--robot-assign=swarm", "--strategy=fastest"}, robot.ErrCodeInvalidFlag, "invalid strategy 'fastest'",
			"Valid strategies: simple, balanced, speed, quality, dependency"},
		{[]string{"--robot-assign=swarm", "--robot-format=toon"}, robot.ErrCodeInvalidFlag, "json only", "use --robot-format=json"},
	} {
		out, code := robotJSON(t, append(tc.args, sock)...)
		if code != robotError || out["success"] != false || out["error_code"] != tc.code || !strings.Contains(out["error"].(string), tc.says) ||
			out["hint"] != tc.hint {
			t.Fatalf("%v = %d %v", tc.args, code, out)
		}
	}

	// No bv: the actionable work cannot be read.
	t.Setenv("PATH", t.TempDir())
	out, code := robotJSON(t, "--robot-assign=swarm", sock)
	if code != robotError || out["error_code"] != robot.ErrCodeDependencyMissing ||
		!strings.Contains(out["error"].(string), "read actionable Beads work") {
		t.Fatalf("no bv = %d %v", code, out)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}
