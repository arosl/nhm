package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
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

func TestEnrollIsIdempotent(t *testing.T) {
	enrollEnv(t)
	s := herdrtest.New(t)
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
