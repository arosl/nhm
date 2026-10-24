package cli

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
		{[]string{"--robot-status", "--ssh=user@h"}, "nhm '--robot-status'"},
		{[]string{"--ssh", "user@h", "send", "s", "it's here"}, `nhm 'send' 's' 'it'\''s here'`},
		{[]string{"version", "-s", "--ssh", "user@h"}, "nhm 'version' '-s'"},
	} {
		code, out, errOut := run(c.args...)
		// stdout is no terminal here, so no -t.
		if code != 0 || out != "--\nuser@h\n"+c.want+"\n" || errOut != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}
	// Help is local, as cobra answers --help before any hook.
	if code, out, _ := run("--ssh", "user@h", "version", "--help"); code != 0 || out != versionUsage {
		t.Errorf("--ssh … version --help: exit %d, stdout %q", code, out)
	}
}

// The hand-off comes after cobra's parse (MistyDesert's nit): a "--ssh"
// that is another flag's value stays that value, and the call runs here.
func TestSSHAfterTheParse(t *testing.T) {
	fakeSSH(t)
	var msg string
	ran := false
	probe := func(*rootOptions, io.Writer, io.Writer) *cobra.Command {
		cmd := &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error {
			ran = true
			return nil
		}}
		cmd.Flags().StringVar(&msg, "msg", "", "")
		return cmd
	}
	code, out, errOut := runWith(probe, "probe", "--msg", "--ssh")
	if code != 0 || !ran || msg != "--ssh" || out != "" || errOut != "" {
		t.Errorf("--msg --ssh: exit %d, ran %v, msg %q, stdout %q, stderr %q", code, ran, msg, out, errOut)
	}
	ran = false
	code, out, _ = runWith(probe, "probe", "--msg", "--ssh", "--ssh", "h")
	if code != 0 || ran || out != "--\nh\nnhm 'probe' '--msg' '--ssh'\n" {
		t.Errorf("--msg --ssh --ssh h: exit %d, ran %v, stdout %q", code, ran, out)
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

// stripSSH reads the words as pflag does: a "--ssh" that is another
// flag's value stays (MistyDesert's case, send S --msg --ssh).
func TestStripSSHKeepsAFlagValue(t *testing.T) {
	fs := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	fs.String("msg", "", "")
	fs.StringP("context", "C", "", "")
	fs.BoolP("invert", "v", false, "")
	fs.Bool("json", false, "")
	fs.String("ssh", "", "")
	v := pflagValues(fs)
	for _, c := range []struct{ in, want []string }{
		{[]string{"send", "S", "--msg", "--ssh"}, []string{"send", "S", "--msg", "--ssh"}},
		{[]string{"send", "S", "--msg", "--ssh", "--ssh", "h"}, []string{"send", "S", "--msg", "--ssh"}},
		{[]string{"--ssh", "h", "send", "--json"}, []string{"send", "--json"}},
		{[]string{"send", "--ssh=h", "--", "--ssh", "x"}, []string{"send", "--", "--ssh", "x"}},
		{[]string{"grep", "-vC", "--ssh", "--ssh", "h"}, []string{"grep", "-vC", "--ssh"}},
		{[]string{"grep", "-C3", "--ssh", "h"}, []string{"grep", "-C3"}},
		{[]string{"grep", "-v", "--ssh", "h"}, []string{"grep", "-v"}},
	} {
		if got := stripSSH(c.in, v); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
	// A legacy verb's own flags are not known: only the root's value flags
	// keep their value.
	if got := stripSSH([]string{"--config", "--ssh", "x", "--ssh", "h"}, rootValues); !slices.Equal(got, []string{"--config", "--ssh", "x"}) {
		t.Errorf("root values: %q", got)
	}
}

func TestSSHNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, out, errOut := run("--ssh", "h", "status")
	if code != 1 || out != "" || !strings.Contains(errOut, "Error: --ssh: ssh is not on PATH") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}
