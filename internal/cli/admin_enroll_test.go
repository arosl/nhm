package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

func enrollEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HERDR_SOCKET_PATH", "")
	return cfg
}

// emptySession answers session.snapshot with no agents: enroll's import
// then has nothing to do.
func emptySession(s *herdrtest.Server) {
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{"protocol": 22, "version": "0.9.3",
			"workspaces": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{}}}, nil
	})
}

func TestEnrollIsIdempotent(t *testing.T) {
	enrollEnv(t)
	s := herdrtest.New(t)
	emptySession(s)
	code, out, errOut := run("admin", "instance", "enroll", "--socket", s.SocketPath, "--json")
	if code != 0 {
		t.Fatalf("enroll = %d %s", code, errOut)
	}
	var env struct {
		Result   EnrollResult
		Warnings []string
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	r := env.Result
	if !r.Created || r.Session != "default" || r.SocketPath != s.SocketPath || r.HerdrProtocol != 22 || len(env.Warnings) != 0 {
		t.Fatalf("first enroll = %+v %v", r, env.Warnings)
	}
	root, _ := store.StateRoot()
	inst, err := store.Lookup(root, s.SocketPath)
	if err != nil || inst.ID != r.InstanceID {
		t.Fatalf("lookup = %+v %v", inst, err)
	}
	code, out, _ = run("admin", "instance", "enroll", "--socket", s.SocketPath)
	if code != 0 || !strings.HasPrefix(out, "already enrolled "+r.InstanceID) {
		t.Fatalf("second enroll = %d %q", code, out)
	}
}

// Enroll imports the agents ACFS's Bash spawned, so nhm sees them from
// the start (acfs-9hkh.41).
func TestEnrollImportsBashSpawnedAgents(t *testing.T) {
	cfg := enrollEnv(t)
	t.Setenv("ACFS_HOME", t.TempDir())
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo, _ = filepath.EvalSymlinks(repo)
	if _, err := agentmail.WriteIdentity(cfg, repo, "w1:p1", "IcyKnoll", time.Now()); err != nil {
		t.Fatal(err)
	}
	s := herdrtest.New(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		agent := map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t1",
			"agent": "claude", "agent_status": "idle", "focused": false, "revision": 1, "cwd": repo}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{"protocol": 22, "version": "0.9.3",
			"workspaces": []any{}, "tabs": []any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "number": 1,
				"label": "1", "focused": false, "pane_count": 1, "agent_status": "idle"}},
			"panes": []any{agent}, "agents": []any{agent}}}, nil
	})
	s.Handle("pane.process_info", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w1:p1"}}, nil
	})
	code, out, errOut := run("admin", "instance", "enroll", "--socket", s.SocketPath, "--json")
	if code != 0 {
		t.Fatalf("enroll = %d %s", code, errOut)
	}
	var env struct {
		Result   EnrollResult
		Warnings []string
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Result.Import == nil || env.Result.Import.Imported != 1 || len(env.Warnings) != 0 {
		t.Fatalf("import = %+v, warnings %v", env.Result.Import, env.Warnings)
	}
	code, out, _ = run("admin", "instance", "enroll", "--socket", s.SocketPath)
	if code != 0 || strings.Contains(out, "imported") {
		t.Fatalf("second enroll imported again: %d %q", code, out)
	}
}

func TestEnrollFindsHerdrsDefaultSocket(t *testing.T) {
	cfg := enrollEnv(t)
	// No server: the install may run before herdr starts.
	code, out, errOut := run("admin", "instance", "enroll", "--json")
	if code != 0 {
		t.Fatalf("enroll = %d %s", code, errOut)
	}
	var env struct {
		Result   EnrollResult
		Warnings []string
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Result.SocketPath != filepath.Join(cfg, "herdr", "herdr.sock") || len(env.Warnings) != 1 ||
		!strings.Contains(env.Warnings[0], "does not answer") {
		t.Fatalf("default session = %+v %v", env.Result, env.Warnings)
	}
	code, out, _ = run("admin", "instance", "enroll", "--session", "exp4", "--json")
	if code != 0 || !strings.Contains(out, filepath.Join(cfg, "herdr", "sessions", "exp4", "herdr.sock")) {
		t.Fatalf("named session = %d %s", code, out)
	}
	if code, _, _ := run("admin", "instance", "enroll", "--session", "../x"); code == 0 {
		t.Fatal("a session name with a slash was accepted")
	}
}

// Inside a pane of the default server, --session names another server;
// the pane's socket must not be recorded under that session.
func TestExplicitSessionWinsOverThePaneSocket(t *testing.T) {
	cfg := enrollEnv(t)
	t.Setenv("HERDR_SOCKET_PATH", "/run/pane/herdr.sock")
	t.Setenv("HERDR_SESSION", "")
	code, out, errOut := run("admin", "instance", "enroll", "--session", "exp4", "--json")
	if code != 0 || !strings.Contains(out, filepath.Join(cfg, "herdr", "sessions", "exp4", "herdr.sock")) || strings.Contains(out, "/run/pane/") {
		t.Fatalf("explicit session = %d %s %s", code, out, errOut)
	}
	// Without --session the pane's socket counts, under the pane's session.
	t.Setenv("HERDR_SESSION", "work")
	code, out, _ = run("admin", "instance", "enroll", "--json")
	if code != 0 || !strings.Contains(out, `"socket_path": "/run/pane/herdr.sock"`) || !strings.Contains(out, `"session": "work"`) {
		t.Fatalf("pane socket = %d %s", code, out)
	}
}

func TestAdminUsageNamesEnroll(t *testing.T) {
	code, _, errOut := run("admin")
	if code != 2 || !strings.Contains(errOut, "nhm admin instance enroll") {
		t.Fatalf("admin = %d %q", code, errOut)
	}
}
