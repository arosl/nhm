package cli

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/robot/contract"
	"github.com/arosl/nhm/internal/store"
)

// downInstances enrolls two herdr servers that do not run, in a scratch
// state root: one whose socket is gone, with IcyKnoll bound in it, and one
// whose socket refuses (nhm-fyq).
func downInstances(t *testing.T) (gone, refused string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	t.Setenv("ACFS_HOME", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	// A socket path must fit sun_path, so not under a gate's long TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "nhm-fyq-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	gone, refused = filepath.Join(dir, "gone.sock"), filepath.Join(dir, "refused.sock")
	l, err := net.Listen("unix", refused)
	if err != nil {
		t.Fatal(err)
	}
	// Close the listener but keep its file: a socket that refuses.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, gone, "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Enroll(root, refused, "other", time.Now()); err != nil {
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
	return gone, refused
}

// fakeHerdrBin makes HERDR_BIN_PATH an executable: herdr is installed.
func fakeHerdrBin(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
}

// assertEmptyHerdrDegraded checks a success with no sessions that names
// herdr in degraded_sources and keeps herdr's error under _nhm.sources.
func assertEmptyHerdrDegraded(t *testing.T, what string, env *contract.Envelope) {
	t.Helper()
	contract.AssertCriticalArrayPresent(t, env, "sessions")
	if n := len(env.Raw["sessions"].([]any)); n != 0 {
		t.Errorf("%s: %d sessions, want none: %v", what, n, env.Raw["sessions"])
	}
	degraded, _ := env.Raw["degraded_sources"].([]any)
	if !slices.Contains(degraded, any("herdr")) {
		t.Errorf("%s: degraded_sources = %v, want herdr", what, env.Raw["degraded_sources"])
	}
	nhm, _ := env.Raw["_nhm"].(map[string]any)
	sources, _ := nhm["sources"].(map[string]any)
	h, _ := sources["herdr"].(map[string]any)
	if h["status"] != "unavailable" || h["error"] == "" || h["error"] == nil {
		t.Errorf("%s: _nhm.sources.herdr = %v", what, h)
	}
}

// TestRobotStatusAndSnapshotWithNoHerdrServer: as ntm with no tmux
// server, --robot-status and --robot-snapshot succeed with no sessions,
// even with an agent bound; herdr is in degraded_sources. A snapshot with
// herdr not installed answers DEPENDENCY_MISSING, as ntm's with tmux not
// installed, and a status still succeeds. The other reads still need
// herdr (nhm-fyq).
func TestRobotStatusAndSnapshotWithNoHerdrServer(t *testing.T) {
	gone, refused := downInstances(t)
	fakeHerdrBin(t)
	for _, sock := range []string{gone, refused} {
		for _, form := range []string{"--robot-status", "--robot-snapshot"} {
			env, code := robotRun(t, form, "--no-mail", "--socket", sock)
			if code != 0 {
				t.Fatalf("%s at %s = %d %+v", form, sock, code, env)
			}
			assertEmptyHerdrDegraded(t, form+" at "+sock, env)
		}
		for _, form := range []string{"--robot-activity=swarm", "--robot-tail=swarm", "--robot-health=swarm"} {
			if env, code := robotRun(t, form, "--socket", sock); code != 1 || env.ErrorCode != "DEPENDENCY_MISSING" {
				t.Errorf("%s at %s = %d %+v", form, sock, code, env)
			}
		}
	}

	t.Setenv("HERDR_BIN_PATH", filepath.Join(t.TempDir(), "herdr"))
	env, code := robotRun(t, "--robot-snapshot", "--no-mail", "--socket", gone)
	if code != 1 || env.ErrorCode != "DEPENDENCY_MISSING" || env.Error != "herdr is not installed" {
		t.Fatalf("snapshot, herdr not installed = %d %+v", code, env)
	}
	env, code = robotRun(t, "--robot-status", "--no-mail", "--socket", gone)
	if code != 0 {
		t.Fatalf("status, herdr not installed = %d %+v", code, env)
	}
	assertEmptyHerdrDegraded(t, "status, herdr not installed", env)
}

// TestRobotStatusAndSnapshotWhenHerdrAnswersBadly: a server that answers
// with an error is not down. --robot-status still succeeds, as ntm's
// always does; --robot-snapshot answers DEPENDENCY_MISSING, where ntm
// answers INTERNAL_ERROR (nhm-fyq).
func TestRobotStatusAndSnapshotWhenHerdrAnswersBadly(t *testing.T) {
	s := swarmInstance(t)
	fakeHerdrBin(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "internal_error", Message: "snapshot failed"}
	})
	env, code := robotRun(t, "--robot-status", "--no-mail", "--socket", s.SocketPath)
	if code != 0 {
		t.Fatalf("status = %d %+v", code, env)
	}
	assertEmptyHerdrDegraded(t, "status", env)
	env, code = robotRun(t, "--robot-snapshot", "--no-mail", "--socket", s.SocketPath)
	if code != 1 || env.ErrorCode != "DEPENDENCY_MISSING" || !strings.HasPrefix(env.Error, "herdr did not answer: ") {
		t.Fatalf("snapshot = %d %+v", code, env)
	}
}
