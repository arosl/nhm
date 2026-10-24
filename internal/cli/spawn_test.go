package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// spawnAgentMail answers Agent Mail's JSON-RPC tools/call for a spawn.
func spawnAgentMail(t *testing.T, names ...string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		result := map[string]any{}
		mu.Lock()
		if req.Params.Name == "create_agent_identity" && len(names) > 0 {
			result = map[string]any{"name": names[0], "registration_token": "tok-" + names[0]}
			names = names[1:]
		}
		mu.Unlock()
		text, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENT_MAIL_URL", srv.URL+"/mcp/")
	return srv
}

// spawnHerdr adds what a spawn calls to the send instance's stub.
func spawnHerdr(t *testing.T, s *herdrtest.Server, h *sendHerdr) {
	t.Helper()
	var mu sync.Mutex
	n := 10
	ok := func(herdrtest.Request) (any, *herdrtest.Error) { return map[string]any{"type": "ok"}, nil }
	s.Handle("tab.create", func(herdrtest.Request) (any, *herdrtest.Error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		pane, tab := "w1:p"+strconv.Itoa(n), "w1:t"+strconv.Itoa(n)
		return map[string]any{"type": "tab_created",
			"tab":       map[string]any{"tab_id": tab, "workspace_id": "w1", "number": n, "label": "x", "focused": false, "pane_count": 1, "agent_status": "unknown"},
			"root_pane": map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": "w1", "terminal_id": "t-" + pane, "agent_status": "unknown", "focused": false, "revision": 1}}, nil
	})
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID,
			"shell_pid": 100, "foreground_process_group_id": 100}}, nil
	})
	// The agents spawn starts join the stub's agent.list and agent.get,
	// with herdr's state from h, so the kickoff send finds them.
	started := map[string][2]string{"w1:p1": {"claude", "icyknoll"}, "w1:p2": {"codex", "bluelake"}}
	agent := func(pane string) map[string]any {
		kn := started[pane]
		completion := 0
		if h.status[pane] == "idle" || h.status[pane] == "done" {
			completion = h.seq[pane]
		}
		a := agentMap(pane, kn[0], h.status[pane], h.seq[pane], completion)
		a["name"] = kn[1]
		return a
	}
	s.Handle("agent.start", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.status[p.PaneID], h.seq[p.PaneID] = "idle", 1
		started[p.PaneID] = [2]string{p.Kind, p.Name}
		return map[string]any{"type": "agent_started", "argv": []string{p.Kind}, "agent": agent(p.PaneID)}, nil
	})
	s.Handle("agent.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		list := []any{}
		for pane := range started {
			list = append(list, agent(pane))
		}
		return map[string]any{"type": "agent_list", "agents": list}, nil
	})
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := started[p.Target]; !ok {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "no agent"}
		}
		return map[string]any{"type": "agent_info", "agent": agent(p.Target)}, nil
	})
	s.Handle("agent.wait", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "agent_info", "agent": agentMap(p.Target, "claude", "idle", 1, 1)}, nil
	})
	s.Handle("pane.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": "w1:p11", "tab_id": "w1:t11",
			"workspace_id": "w1", "terminal_id": "t", "agent_status": "idle", "focused": false, "revision": 1}}, nil
	})
	s.Handle("tab.rename", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "tab_info", "tab": map[string]any{"tab_id": "w1:t11", "workspace_id": "w1",
			"number": 3, "label": "x", "focused": false, "pane_count": 1, "agent_status": "idle"}}, nil
	})
	s.Handle("pane.report_metadata", ok)
}

func TestSpawnVerb(t *testing.T) {
	s, inst, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	spawnAgentMail(t, "GreenPike")
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAll(t, filepath.Join(home, ".acfs/onboard/docs/ntm/command_palette.md"),
		"## A\n### default_new_agent | Default\nRead AGENTS.md.\n")
	project := t.TempDir()
	sock := s.SocketPath
	t.Setenv("PATH", t.TempDir()) // no dcg: the argv cannot be screened

	// A dry run under the Bash: the plan and the admission, nothing created.
	code, out, errOut := run("spawn", "swarm", "--cc=1", "--project", project, "--allow-unguarded", "--dry-run", "--socket", sock)
	if code != 0 || !strings.Contains(out, "would start 1 agents in swarm (w1)") || !strings.Contains(out, "claude agent (default model) slot 2.1 planned") {
		t.Fatalf("dry run = %d %s %s", code, out, errOut)
	}
	// Without --allow-unguarded, the missing guards refuse; every error is
	// ntm's "Error: …" with exit 1 (nhm-2p6.4.5).
	if code, _, errOut = run("spawn", "swarm", "--cc=1", "--project", project, "--dry-run", "--socket", sock); code != exitError ||
		!strings.Contains(errOut, "Error: admission refused the spawn: claude guard dcg is missing") {
		t.Fatalf("unguarded = %d %s", code, errOut)
	}
	// A real spawn needs nhm as the owner.
	if code, _, _ = run("spawn", "swarm", "--cc=1", "--project", project, "--allow-unguarded", "--socket", sock); code != exitError {
		t.Fatalf("under the Bash = %d", code)
	}
	ownNhm(t, inst)
	code, out, errOut = run("spawn", "swarm", "--cc=1", "--project", project, "--allow-unguarded", "--op-id", "sp-1", "--socket", sock, "--json")
	if code != 0 {
		t.Fatalf("spawn = %d %s %s", code, out, errOut)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	agent := res["agents"].([]any)[0].(map[string]any)
	if agent["mail_name"] != "GreenPike" || agent["slot"] != "2.1" || agent["status"] != "kickoff_submitted" || agent["herdr_name"] != "greenpike" {
		t.Fatalf("agent = %v", agent)
	}
	if got := h.sent(); len(got) != 1 || !strings.Contains(got[0], "name GreenPike, project key "+project) || !strings.HasSuffix(got[0], "Read AGENTS.md.") {
		t.Fatalf("kickoff = %q", got)
	}
	// Agent Mail's pane identity file names the agent.
	rec, found, err := agentmail.ReadIdentity("", project, agent["pane_id"].(string))
	if err != nil || !found || rec.Name != "GreenPike" {
		t.Fatalf("identity file = %+v %v %v", rec, found, err)
	}
	for _, args := range [][]string{
		{"spawn", "swarm", "--socket", sock},
		{"spawn", "swarm", "--cc=x", "--socket", sock},
		{"spawn", "swarm", "--agy=1:some-model", "--project", project, "--socket", sock},
		{"spawn", "swarm", "--cc=1", "--with-cass", "--no-cass-context", "--socket", sock},
		{"spawn"},
		{"add", "nowhere", "--cc=1", "--project", project, "--allow-unguarded", "--socket", sock},
		{"spawn", "my--proj", "--cc=1", "--socket", sock},
		{"spawn", "swarm", "-l", "bad--label", "--cc=1", "--socket", sock},
	} {
		if code, _, errOut := run(args...); code != exitError || !strings.HasPrefix(errOut, "Error: ") {
			t.Errorf("%v = %d %q, want 1 and ntm's Error:", args, code, errOut)
		}
	}
}

// ntm's -l/--label makes the session <session>--<label> in the same
// project, for spawn and add; nhm's tab label is --tab-label
// (nhm-2p6.4.5).
func TestSpawnLabelMakesSessionSuffix(t *testing.T) {
	req, err := parseSpawnArgs(t, "proj", "-l", "frontend", "--tab-label", "Front", "--cc=1")
	if err != nil || req.Session != "proj--frontend" || req.Label != "Front" {
		t.Fatalf("label = %q %q %v", req.Session, req.Label, err)
	}
	if _, err := parseSpawnArgs(t, "my--proj", "--cc=1"); err == nil ||
		err.Error() != `project name "my--proj" contains '--' which is reserved as the session-label separator` {
		t.Fatalf("a session with -- = %v", err)
	}
	if _, err := parseSpawnArgs(t, "proj", "--label=-bad", "--cc=1"); err == nil ||
		err.Error() != "invalid label: label must start with alphanumeric and contain only alphanumeric, dash, underscore" {
		t.Fatalf("a bad label = %v", err)
	}
}

func TestRobotSpawnRefusesWithoutObservations(t *testing.T) {
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	// A new session: --spawn-safety refuses the existing swarm (nhm-lrx).
	out, code := robotJSON(t, "--robot-spawn=fresh", "--spawn-dir="+project, "--spawn-cc=1", "--spawn-no-user",
		"--spawn-safety", "--spawn-wait", "--timeout=60s", "--robot-format=json", "--dry-run", "--socket="+s.SocketPath)
	ad, _ := out["admission"].(map[string]any)
	if code != 1 || out["success"] != false || out["error_code"] != "ADMISSION_REFUSED" || ad["decision"] != "refuse" ||
		!strings.Contains(out["error"].(string), "needs an observation") {
		t.Fatalf("robot spawn = %d %v", code, out)
	}
	if len(out["would_create"].([]any)) != 1 || out["working_dir"] != project {
		t.Fatalf("plan = %v", out)
	}
}

func TestNewVerb(t *testing.T) {
	s, inst, _ := newSendInstance(t)
	var created []string
	s.Handle("workspace.create", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Label string `json:"label"`
			Cwd   string `json:"cwd"`
		}
		_ = json.Unmarshal(r.Params, &p)
		created = append(created, p.Label+"@"+p.Cwd)
		return map[string]any{"type": "workspace_created",
			"workspace": map[string]any{"workspace_id": "w7", "number": 7, "label": p.Label, "focused": false, "pane_count": 1, "tab_count": 1, "active_tab_id": "w7:t1", "agent_status": "unknown"},
			"tab":       map[string]any{"tab_id": "w7:t1", "workspace_id": "w7", "number": 1, "label": "1", "focused": true, "pane_count": 1, "agent_status": "unknown"},
			"root_pane": map[string]any{"pane_id": "w7:p1", "tab_id": "w7:t1", "workspace_id": "w7", "terminal_id": "t", "agent_status": "unknown", "focused": true, "revision": 1}}, nil
	})
	project := t.TempDir()
	if code, _, _ := run("new", "swarm", "--project", project, "--socket", s.SocketPath); code != 2 {
		t.Fatalf("existing label = %d", code)
	}
	if code, _, _ := run("new", "fresh", "--project", project, "--socket", s.SocketPath); code != 2 {
		t.Fatalf("under the Bash = %d", code)
	}
	ownNhm(t, inst)
	code, out, errOut := run("new", "fresh", "--project", project, "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "created workspace fresh (w7)") || len(created) != 1 {
		t.Fatalf("new = %d %s %s %v", code, out, errOut, created)
	}
}

func writeAll(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
