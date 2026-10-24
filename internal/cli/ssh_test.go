package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSSH puts an ssh on PATH that prints each argument on a line and
// exits with $FAKE_SSH_EXIT.
func fakeSSH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\nexit \"${FAKE_SSH_EXIT:-0}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_SSH_EXIT", "")
}

// The operator's ruling (2026-10-24): --ssh H runs the whole call on H,
// "ssh [-t] -- H nhm <args without --ssh>"; robot flags and help go too.
func TestSSHRunsTheWholeCallOnTheHost(t *testing.T) {
	fakeSSH(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--ssh", "user@h", "status", "--json"}, "nhm 'status' '--json'"},
		{[]string{"status", "--ssh=user@h", "--", "--ssh", "x"}, "nhm 'status' '--' '--ssh' 'x'"},
		{[]string{"--ssh", "user@h", "--robot-status"}, "nhm '--robot-status'"},
		{[]string{"--ssh", "user@h", "send", "s", "it's here"}, `nhm 'send' 's' 'it'\''s here'`},
		{[]string{"--ssh", "user@h", "version", "--help"}, "nhm 'version' '--help'"},
	} {
		code, out, errOut := run(c.args...)
		// stdout is no terminal here, so no -t.
		if code != 0 || out != "--\nuser@h\n"+c.want+"\n" || errOut != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
}

// ssh's exit is the call's: the remote nhm's code, or ssh's own 255.
func TestSSHExitCodePassesThrough(t *testing.T) {
	fakeSSH(t)
	t.Setenv("FAKE_SSH_EXIT", "3")
	if code, _, _ := run("--ssh", "h", "status"); code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
}

// An empty --ssh is none, as in ntm; a missing value is pflag's error.
func TestSSHEmptyOrMissing(t *testing.T) {
	fakeSSH(t)
	code, out, errOut := run("--ssh=", "version", "-s")
	if code != 0 || out != Version+"\n" || errOut != "" {
		t.Errorf("--ssh= : exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	code, out, errOut = run("version", "--ssh")
	if code != 1 || out != "" || errOut != "Error: flag needs an argument: --ssh\n" {
		t.Errorf("--ssh without a value: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

func TestSSHNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, out, errOut := run("--ssh", "h", "status")
	if code != 1 || out != "" || !strings.Contains(errOut, "Error: --ssh: ssh is not on PATH") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
