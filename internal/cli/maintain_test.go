package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// ACFS's timer runs maintain --once every 15 s whether or not nhm serves
// the herdr server (acfs-9hkh.78): not enrolled, no server and not the
// owner all exit 0 with the reason on one stdout line and nothing on
// stderr.
func TestMaintainOnceIsQuietWhenThereIsNothingToServe(t *testing.T) {
	quiet := func(t *testing.T, want string, args ...string) {
		t.Helper()
		code, out, errOut := runVerb(runMaintain, append([]string{"--once"}, args...)...)
		if code != exitOK || errOut != "" || !strings.HasPrefix(out, "nothing to do: "+want) || strings.Count(out, "\n") != 1 {
			t.Fatalf("maintain --once %v = %d %q %q, want %q", args, code, out, errOut, want)
		}
		code, out, errOut = runVerb(runMaintain, append([]string{"--once", "--json"}, args...)...)
		res, _ := decodeEnvelope(t, out)["result"].(map[string]any)
		if code != exitOK || errOut != "" || !strings.HasPrefix(res["skipped"].(string), want) {
			t.Fatalf("maintain --once --json %v = %d %s %q", args, code, out, errOut)
		}
	}

	t.Run("not enrolled", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("HERDR_SOCKET_PATH", "")
		quiet(t, "this herdr server is not enrolled", "--socket", filepath.Join(t.TempDir(), "herdr.sock"))
		// --loop keeps failing loudly: it is no timer's pass.
		code, _, errOut := runVerb(runMaintain, "--loop", "--socket", filepath.Join(t.TempDir(), "herdr.sock"))
		if code != exitError || !strings.Contains(errOut, "not enrolled") {
			t.Fatalf("--loop not enrolled = %d %q", code, errOut)
		}
	})

	t.Run("no server", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("HERDR_SOCKET_PATH", "")
		root, err := store.StateRoot()
		if err != nil {
			t.Fatal(err)
		}
		// Socket paths stay under the kernel's limit (107 bytes) whatever
		// TMPDIR is: a long one gives EINVAL, which is no "no server".
		missing := filepath.Join(shortDir(t), "herdr.sock")
		if _, _, err := store.Enroll(root, missing, "default", time.Now()); err != nil {
			t.Fatal(err)
		}
		quiet(t, "no herdr server runs on "+missing, "--socket", missing)

		// A socket file a stopped server left behind refuses connections.
		stale := filepath.Join(shortDir(t), "stale.sock")
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		l.SetUnlinkOnClose(false)
		_ = l.Close()
		if _, _, err := store.Enroll(root, stale, "default", time.Now()); err != nil {
			t.Fatal(err)
		}
		quiet(t, "no herdr server runs on "+stale, "--socket", stale)
	})

	t.Run("not the owner", func(t *testing.T) {
		s, _ := cliInstance(t) // ACFS's Bash owns a fresh enrolment
		quiet(t, "", "--socket", s.SocketPath)
	})
}

// A socket path the kernel will not take cannot tell whether a server
// runs: the pass fails, it is never a quiet skip.
func TestMaintainOnceFailsWhenItCannotTell(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", "")
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(shortDir(t), strings.Repeat("d", 120), "herdr.sock")
	if _, _, err := store.Enroll(root, long, "default", time.Now()); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runVerb(runMaintain, "--once", "--socket", long)
	if code != exitError || strings.Contains(out, "nothing to do") || !strings.Contains(errOut, "cannot tell whether a herdr server runs") {
		t.Fatalf("maintain --once on a %d-byte socket path = %d %q %q", len(long), code, out, errOut)
	}
}

// shortDir is a fresh directory under /tmp, so a socket path in it fits
// the kernel's sockaddr_un limit however long TMPDIR is.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "nhm-s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
