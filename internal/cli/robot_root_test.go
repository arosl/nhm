package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// robotCall finds a robot call behind ntm's root flags; a root flag's
// value is the next word, even a --robot-* one.
func TestRobotCall(t *testing.T) {
	for _, c := range []struct {
		args []string
		lead int
		ok   bool
	}{
		{[]string{"--robot-status"}, 0, true},
		{[]string{"--json", "--config", "f", "--robot-status", "--json"}, 3, true},
		{[]string{"--config=f", "--no-color=false", "--robot-tail=s"}, 2, true},
		{[]string{"--config", "--robot-status"}, 0, false},
		{[]string{"status", "--robot-status"}, 0, false},
		{[]string{"-V", "--robot-status"}, 0, false},
		{[]string{"--lines", "5", "--robot-tail=s"}, 0, false},
		{nil, 0, false},
	} {
		lead, rest, ok := robotCall(c.args)
		if ok != c.ok || ok && (len(lead) != c.lead || len(rest) != len(c.args)-c.lead) {
			t.Errorf("%v: lead %v, rest %v, ok %v", c.args, lead, rest, ok)
		}
	}
}

// ntm's root flags in front of a robot flag: the call runs, with --config
// in effect for it; --redact and --allow-secret are not built (ntm's
// NOT_IMPLEMENTED, exit 2); a bad value is the robot's INVALID_FLAG.
// Nothing goes to stderr.
func TestRootFlagsInFrontOfARobotFlag(t *testing.T) {
	t.Setenv("NHM_CONFIG", "")
	decode := func(out string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("not JSON: %q", out)
		}
		return m
	}
	_, out, _ := run("--robot-capabilities")
	want := len(decode(out)["commands"].([]any))
	for _, args := range [][]string{
		{"--json", "--robot-capabilities"},
		{"--no-color", "--profile-startup", "--robot-capabilities"},
		{"--config=", "--json=true", "--robot-capabilities"},
	} {
		code, out, errOut := run(args...)
		m := decode(out)
		if commands, _ := m["commands"].([]any); code != 0 || m["success"] != true || len(commands) != want || errOut != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	for _, c := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"--redact", "warn", "--robot-capabilities"}, robotNotImplemented, "NOT_IMPLEMENTED"},
		{[]string{"--allow-secret", "--robot-capabilities"}, robotNotImplemented, "NOT_IMPLEMENTED"},
		{[]string{"--json=maybe", "--robot-capabilities"}, robotError, "INVALID_FLAG"},
	} {
		code, out, errOut := run(c.args...)
		if m := decode(out); code != c.exit || m["error_code"] != c.code || errOut != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.args, code, out, errOut)
		}
	}

	// robot spawn takes a flag per kind: --config's kinds count.
	cfg := kindConfig(t, "zzrobot")
	t.Chdir(t.TempDir()) // no project: a dry run stops there
	if _, out, _ := run("--robot-spawn=s", "--spawn-zzrobot=1", "--dry-run"); !strings.Contains(out, "spawn-zzrobot") {
		t.Errorf("without --config: %q", out)
	}
	if _, out, _ := run("--config", cfg, "--robot-spawn=s", "--spawn-zzrobot=1", "--dry-run"); strings.Contains(out, "spawn-zzrobot") {
		t.Errorf("with --config: %q", out)
	}
	if v := os.Getenv("NHM_CONFIG"); v != "" {
		t.Errorf("NHM_CONFIG outlived the run: %q", v)
	}
}
