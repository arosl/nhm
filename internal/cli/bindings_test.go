package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// cliInstance enrolls a stub herdr in a scratch state root and serves a
// snapshot with one claude agent in w1:p1 and one in w1:p2.
func cliInstance(t *testing.T) (*herdrtest.Server, store.Instance) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PANE_ID", "")
	s := herdrtest.New(t)
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, s.SocketPath, "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pane := func(id, tab string) map[string]any {
		return map[string]any{"pane_id": id, "tab_id": tab, "workspace_id": "w1", "terminal_id": "t-" + id,
			"agent_status": "idle", "focused": false, "revision": 1, "agent": "claude"}
	}
	agent := func(id, tab, name string) map[string]any {
		a := pane(id, tab)
		if name != "" {
			a["name"] = name
		}
		return a
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "workspaces": []any{}, "layouts": []any{},
			"tabs":   []any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "number": 1, "label": "1", "focused": true, "pane_count": 1, "agent_status": "idle"}},
			"panes":  []any{pane("w1:p1", "w1:t1"), pane("w1:p2", "w1:t2")},
			"agents": []any{agent("w1:p1", "w1:t1", "icyknoll"), agent("w1:p2", "w1:t2", "")},
		}}, nil
	})
	s.Handle("pane.process_info", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w1:p1"}}, nil
	})
	s.Handle("agent.rename", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "agent_info", "agent": agent("w1:p2", "w1:t2", "bluelake")}, nil
	})
	return s, inst
}

func decodeEnvelope(t *testing.T, out string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if env["schema"] != "nhm.v1" {
		t.Fatalf("schema = %v", env["schema"])
	}
	return env
}

func TestResolveVerb(t *testing.T) {
	s, inst := cliInstance(t)
	project := t.TempDir()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: project,
		ProjectID: "proj-x", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude"}})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run("resolve", "IcyKnoll", "--socket", s.SocketPath, "--json")
	if code != 0 {
		t.Fatalf("resolve = %d\n%s%s", code, out, errOut)
	}
	env := decodeEnvelope(t, out)
	res := env["result"].(map[string]any)
	if res["binding"].(map[string]any)["slot"] != "0.1" || res["verification"].(map[string]any)["verdict"] != "verified" {
		t.Fatalf("result = %v", res)
	}
	code, out, _ = run("resolve", "w1:p1", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "IcyKnoll (claude)") || !strings.Contains(out, "verification: verified") {
		t.Fatalf("text = %d %s", code, out)
	}
	if code, _, errOut := run("resolve", "Nobody", "--socket", s.SocketPath); code != 1 || !strings.Contains(errOut, "no such binding") {
		t.Fatalf("unknown = %d %s", code, errOut)
	}
}

func TestResolveUsageAndNoSocket(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	for _, args := range [][]string{{"resolve"}, {"resolve", "a", "b"}, {"resolve", "a", "--current"}, {"resolve", "--bogus"}} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
	code, out, _ := run("resolve", "IcyKnoll", "--json")
	if code != 1 || decodeEnvelope(t, out)["error"] == nil {
		t.Fatalf("no socket = %d %s", code, out)
	}
}

func TestAdoptNeedsNhmAsOwner(t *testing.T) {
	s, inst := cliInstance(t)
	project := t.TempDir()
	code, out, _ := run("adopt", "BlueLake", "--pane", "w1:p2", "--project", project, "--socket", s.SocketPath, "--json")
	env := decodeEnvelope(t, out)
	if code != 2 || !strings.Contains(env["error"].(string), "owned by another") {
		t.Fatalf("adopt under the Bash = %d %v", code, env["error"])
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run("adopt", "--pane", "w1:p2", "BlueLake", "--project", project, "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "adopted BlueLake (claude) in w1:p2") {
		t.Fatalf("adopt = %d %s %s", code, out, errOut)
	}
	if code, _, _ := run("adopt", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("adopt without a name = %d", code)
	}
}

func TestRecoverReconcileImportOnly(t *testing.T) {
	s, _ := cliInstance(t)
	if code, _, _ := run("admin", "recover", "reconcile", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("without --import-only = %d", code)
	}
	if code, _, _ := run("admin", "takeover"); code != 2 {
		t.Fatalf("unknown admin verb = %d", code)
	}
	code, out, errOut := run("admin", "recover", "reconcile", "--import-only", "--dry-run", "--socket", s.SocketPath, "--json")
	if code != 0 {
		t.Fatalf("import = %d %s %s", code, out, errOut)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	if res["dry_run"] != true || res["imported"].(float64) != 0 {
		t.Fatalf("result = %v", res)
	}
}
