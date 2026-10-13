package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// After a takeover with no herdr server (a fresh ACFS install), ACFS's
// install and update run restore again before herdr starts: with no pane
// to verify, it exits 0 and says so (nhm-brp). A herdr that answers but
// fails still fails.
func TestRestoreWithNoServer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PANE_ID", "")
	// Under /tmp: a socket path past sun_path's limit fails with EINVAL,
	// which is not a server that is down.
	dir, err := os.MkdirTemp("/tmp", "nhm-restore-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "herdr.sock")
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, sock, "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ownNhm(t, inst)

	code, out, errOut := run("admin", "restore", "--socket", sock)
	if code != 0 || !strings.Contains(out, "herdr is not running; nothing to restore") || errOut != "" {
		t.Fatalf("restore = %d %q %q", code, out, errOut)
	}
	code, out, _ = run("admin", "restore", "--socket", sock, "--json")
	env := decodeEnvelope(t, out)
	if code != 0 || env["error"] != nil || len(env["result"].(map[string]any)["bindings"].([]any)) != 0 {
		t.Fatalf("restore --json = %d %s", code, out)
	}

	// A server that answers, but not with a snapshot, is not down.
	s := herdrtest.New(t)
	inst2, _, err := store.Enroll(root, s.SocketPath, "other", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ownNhm(t, inst2)
	if code, out, errOut = run("admin", "restore", "--socket", s.SocketPath); code != 1 {
		t.Fatalf("restore with a failing herdr = %d %q %q", code, out, errOut)
	}
}
