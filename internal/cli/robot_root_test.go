package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// ntm's root flags after the robot flag, in each robot flag set: the
// parse takes them (the bad --robot-format is the next refusal);
// --redact and --allow-secret are not built (exit 2); a bad value is
// INVALID_FLAG. A root flag's word after a robot flag that takes a value
// is that value.
func TestRootFlagsAfterARobotFlag(t *testing.T) {
	t.Setenv("NHM_CONFIG", "")
	for _, call := range []string{"--robot-status", "--robot-wait=s", "--robot-interrupt=s", "--robot-assign=s",
		"--robot-palette", "--robot-send=s", "--robot-spawn=s"} {
		for _, c := range []struct {
			after []string
			exit  int
			error string
		}{
			{[]string{"--json", "--no-color", "--profile-startup", "--config=", "--ssh="}, robotError, `invalid robot format "xml"`},
			{[]string{"--json=maybe"}, robotError, `invalid boolean value "maybe" for -json`},
			{[]string{"--redact", "warn"}, robotNotImplemented, "--redact is not in this nhm"},
			{[]string{"--allow-secret"}, robotNotImplemented, "--allow-secret is not in this nhm"},
		} {
			args := append([]string{call, "--robot-format=xml"}, c.after...)
			code, out, errOut := run(args...)
			var m map[string]any
			err := json.Unmarshal([]byte(out), &m)
			if msg, _ := m["error"].(string); err != nil || code != c.exit || !strings.Contains(msg, c.error) || errOut != "" {
				t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errOut)
			}
		}
	}
	for _, args := range [][]string{
		{"--robot-send=s", "--msg", "--redact", "--robot-format=xml"},
		{"--robot-interrupt=s", "--msg", "--allow-secret", "--robot-format=xml"},
		{"--robot-spawn=s", "--spawn-prompt", "--redact", "--robot-format=xml"},
	} {
		if _, out, _ := run(args...); !strings.Contains(out, `invalid robot format \"xml\"`) {
			t.Errorf("%v: %q", args, out)
		}
	}
}

// --config F after the robot flag: robot spawn takes F's kind flags, the
// last --config wins, and a "--config" that is another flag's value stays
// that value; robot send reads F's enrichment. NHM_CONFIG ends with the
// call.
func TestConfigAfterARobotFlag(t *testing.T) {
	t.Setenv("NHM_CONFIG", "")
	cfg, other := kindConfig(t, "zzafter"), kindConfig(t, "zzother")
	t.Chdir(t.TempDir()) // no project: a dry run stops there
	for _, c := range []struct {
		args    []string
		unknown bool
	}{
		{[]string{"--robot-spawn=s", "--spawn-zzafter=1", "--dry-run"}, true},
		{[]string{"--robot-spawn=s", "--spawn-zzafter=1", "--dry-run", "--config", cfg}, false},
		{[]string{"--robot-spawn=s", "--spawn-zzafter", "1", "--config=" + cfg, "--dry-run"}, false},
		{[]string{"--config", cfg, "--robot-spawn=s", "--spawn-zzafter=1", "--config", other, "--dry-run"}, true},
		{[]string{"--config", other, "--robot-spawn=s", "--spawn-zzafter=1", "--config", cfg, "--dry-run"}, false},
		{[]string{"--robot-spawn=s", "--spawn-prompt", "--config", "--config", cfg, "--spawn-zzafter=1", "--dry-run"}, false},
	} {
		_, out, _ := run(c.args...)
		if strings.Contains(out, "flag provided but not defined: -spawn-zzafter") != c.unknown {
			t.Errorf("%v: %q", c.args, out)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("[[[\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := run("--robot-send=s", "--msg", "hi", "--config", bad); !strings.Contains(out, bad+": toml") {
		t.Errorf("robot send with a bad --config: %q", out)
	}
	if v := os.Getenv("NHM_CONFIG"); v != "" {
		t.Errorf("NHM_CONFIG outlived the run: %q", v)
	}
}
