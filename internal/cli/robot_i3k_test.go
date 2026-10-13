package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

// ocPiInstance serves workspace "swarm" (w1) with an opencode agent in
// w1:p1 and a pi agent in w1:p2, both idle and bound (nhm-i3k).
func ocPiInstance(t *testing.T) *herdrtest.Server {
	t.Helper()
	s, inst := cliInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	t.Setenv("ACFS_HOME", "")
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	kinds := map[string]string{"w1:p1": "opencode", "w1:p2": "pi"}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 2, "tab_count": 1, "active_tab_id": "w1:t1", "agent_status": "idle"}},
			"agents": []any{agentMap("w1:p1", "opencode", "idle", 4, 4), agentMap("w1:p2", "pi", "idle", 4, 4)},
		}}, nil
	})
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "agent_info", "agent": agentMap(p.Target, kinds[p.Target], "idle", 4, 4)}, nil
	})
	s.Handle("events.subscribe", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "subscription_started"}, nil
	})
	read := func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID + p.Target, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3, "text": "Done.\n❯ \n", "truncated": false}}, nil
	}
	s.Handle("pane.read", read)
	s.Handle("agent.read", read)
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, n := range []struct{ name, pane string }{{"IcyKnoll", "w1:p1"}, {"BlueLake", "w1:p2"}} {
		_, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: n.name, MailProjectKey: "/p",
			ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
				PaneID: n.pane, HerdrAlias: strings.ToLower(n.name), Kind: kinds[n.pane]}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestRobotReadsTypeOpencodeAsOcAndListPi: every robot read types
// opencode as ntm's oc (agentTypeString), and lists pi under its own
// name, as ntm lists a registered plugin agent type (nhm-i3k).
func TestRobotReadsTypeOpencodeAsOcAndListPi(t *testing.T) {
	s := ocPiInstance(t)
	sock := s.SocketPath
	want := []string{"oc", "pi"}
	check := func(read string, got []string) {
		t.Helper()
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s types = %v, want %v", read, got, want)
		}
	}
	field := func(list any, key string) []string {
		var out []string
		for _, a := range list.([]any) {
			out = append(out, a.(map[string]any)[key].(string))
		}
		return out
	}

	env, _ := robotRun(t, "--robot-status", "--no-mail", "--socket", sock)
	summary := env.Raw["summary"].(map[string]any)
	var byType []string
	for k := range summary["agents_by_type"].(map[string]any) {
		byType = append(byType, k)
	}
	check("--robot-status", byType)
	if summary["opencode_count"] != 1.0 {
		t.Errorf("--robot-status opencode_count = %v", summary["opencode_count"])
	}

	env, _ = robotRun(t, "--robot-snapshot", "--no-mail", "--socket", sock)
	check("--robot-snapshot", field(env.Raw["sessions"].([]any)[0].(map[string]any)["agents"], "type"))

	env, _ = robotRun(t, "--robot-activity=swarm", "--socket", sock)
	check("--robot-activity", field(env.Raw["agents"], "agent_type"))

	env, _ = robotRun(t, "--robot-tail=swarm", "--lines=5", "--socket", sock)
	var tail []string
	for _, p := range env.Raw["panes"].(map[string]any) {
		tail = append(tail, p.(map[string]any)["type"].(string))
	}
	check("--robot-tail", tail)

	env, _ = robotRun(t, "--robot-health=swarm", "--socket", sock)
	check("--robot-health", field(env.Raw["agents"], "agent_type"))

	code, v := robotWaitRun(t, "--robot-wait=swarm", "--condition=idle", "--timeout=5s", "--poll=5ms", "--socket", sock)
	if code != 0 {
		t.Fatalf("--robot-wait = %d %v", code, v)
	}
	check("--robot-wait", field(v["agents"], "agent_type"))
}

// TestAssignAgentOcPicksAnOpencodeAgent: spawn --assign's --assign-agent
// filter compares ntm's type, which a spawned agent takes from its kind's
// NTMType. LegacyAgentType canonicalises both "oc" and "opencode", so the
// rename to oc keeps --assign-agent=oc and =opencode picking an opencode
// agent, as in ntm (AmberLotus's check, nhm-i3k).
func TestAssignAgentOcPicksAnOpencodeAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no bv: no cycles
	triage := &bv.TriageResponse{Triage: bv.TriageData{Recommendations: []bv.TriageRecommendation{
		{ID: "nhm-1", Title: "Fix the thing", Type: "task", Status: "open", Priority: 1, Score: 0.9}}}}
	agents := []robot.SpawnedAgent{{Pane: "2.1", PaneID: "w1:p11", Type: kinds.Lookup("claude").NTMType},
		{Pane: "2.2", PaneID: "w1:p12", Type: kinds.Lookup("opencode").NTMType}}
	ie := &instanceEnv{inst: store.Instance{ID: "inst-oc"}}
	for _, flag := range []string{"oc", "opencode"} {
		a := &humanAssign{strategy: "balanced", agentType: spawnassign.ResolveSpawnAssignAgentType(flag, false, false, false, false),
			timeout: time.Second}
		var sel spawnassign.HumanSelection
		_, items := a.selectWork(context.Background(), ie, "swarm", t.TempDir(), &sel)(triage, agents)
		if len(items) != 1 {
			t.Fatalf("--assign-agent=%s: items %+v", flag, items)
		}
	}
}

// TestRobotDependencyMissingExitsOne: with no herdr, each robot form
// answers DEPENDENCY_MISSING with exit 1, as ntm's ExitCodeForResponse
// does; exit 2 stays NOT_IMPLEMENTED's (nhm-i3k).
func TestRobotDependencyMissingExitsOne(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PANE_ID", "")
	for _, args := range [][]string{
		{"--robot-status"}, {"--robot-snapshot"}, {"--robot-activity=swarm"}, {"--robot-tail=swarm"},
		{"--robot-health=swarm"}, {"--robot-wait=swarm"}, {"--robot-send=swarm", "--panes=0.1", "--msg=hi"},
		{"--robot-interrupt=swarm"}, {"--robot-spawn=swarm", "--spawn-dir=" + t.TempDir(), "--spawn-cc=1", "--spawn-no-user"},
	} {
		env, code := robotRun(t, args...)
		if code != 1 || env.ErrorCode != "DEPENDENCY_MISSING" {
			t.Errorf("%v = %d %+v", args, code, env)
		}
	}
	if env, code := robotRun(t, "--robot-health"); code != 2 || env.ErrorCode != "NOT_IMPLEMENTED" {
		t.Errorf("--robot-health = %d %+v", code, env)
	}
}
