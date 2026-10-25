package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearRobotFormatEnv unsets every name that sets the robot format, ntm's
// too, for one test.
func clearRobotFormatEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"NTM_ROBOT_FORMAT", "NTM_OUTPUT_FORMAT", "NHM_CONFIG"}, robotFormatEnv...) {
		t.Setenv(name, "")
	}
}

// ntm's order: the flag, NHM_ROBOT_FORMAT, NHM_OUTPUT_FORMAT,
// TOON_DEFAULT_FORMAT, the config's [robot.output] format, else auto.
// ntm's NTM_* names have no effect (nhm-2p6.2.2).
func TestRobotFormatResolutionOrder(t *testing.T) {
	clearRobotFormatEnv(t)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("[robot.output]\nformat = \"json\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NHM_CONFIG", cfg)
	got := func(flag string, useConfig bool) string {
		t.Helper()
		f, err := resolveRobotFormat(flag, useConfig)
		if err != nil {
			t.Fatalf("resolveRobotFormat(%q): %v", flag, err)
		}
		return f
	}
	if got("", true) != "json" || got("", false) != "auto" {
		t.Fatalf("config: %q, without it %q", got("", true), got("", false))
	}
	t.Setenv("TOON_DEFAULT_FORMAT", "auto")
	if got("", true) != "auto" {
		t.Fatal("TOON_DEFAULT_FORMAT does not win over the config")
	}
	t.Setenv("NHM_OUTPUT_FORMAT", "json")
	if got("", true) != "json" {
		t.Fatal("NHM_OUTPUT_FORMAT does not win over TOON_DEFAULT_FORMAT")
	}
	t.Setenv("NHM_ROBOT_FORMAT", "auto")
	if got("", true) != "auto" {
		t.Fatal("NHM_ROBOT_FORMAT does not win over NHM_OUTPUT_FORMAT")
	}
	if got("json", true) != "json" {
		t.Fatal("the flag does not win over the environment")
	}
	clearRobotFormatEnv(t)
	t.Setenv("NTM_ROBOT_FORMAT", "xml")
	t.Setenv("NTM_OUTPUT_FORMAT", "xml")
	if got("", true) != "auto" {
		t.Fatal("an NTM_* name set the format")
	}
	// A config that does not load gives no format.
	if err := os.WriteFile(cfg, []byte("[robot.output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NHM_CONFIG", cfg)
	if got("", true) != "auto" {
		t.Fatal("a broken config set the format")
	}
}

// --robot-output-format is --robot-format's alias. A format from the flag
// or the environment that ntm does not know is ntm's INVALID_FLAG, with
// ntm's hint (toon: robot_toon_test.go).
func TestRobotFormatAliasAndRefusals(t *testing.T) {
	clearRobotFormatEnv(t)
	t.Setenv("HERDR_SOCKET_PATH", "")
	if env, code := robotRun(t, "--robot-version", "--robot-output-format=json"); code != 0 || env.Raw["output_format"] != "json" {
		t.Fatalf("--robot-output-format=json = %d %v", code, env.Raw)
	}
	for _, c := range []struct {
		args       []string
		env, error string
		hint       string
	}{
		{[]string{"--robot-version", "--robot-format=xml"}, "", `invalid robot format "xml": must be json, toon, or auto`, "Use json, toon, or auto"},
		{[]string{"--robot-capabilities", "--robot-output-format=xml"}, "", `invalid robot format "xml"`, "Use json, toon, or auto"},
		{[]string{"--robot-version"}, "xml", `invalid robot format "xml"`, "Use json, toon, or auto"},
	} {
		t.Setenv("NHM_ROBOT_FORMAT", c.env)
		env, code := robotRun(t, c.args...)
		if code != robotError || env.ErrorCode != "INVALID_FLAG" || !strings.Contains(env.Error, c.error) || env.Hint != c.hint ||
			env.Raw["output_format"] != "auto" {
			t.Errorf("%v with NHM_ROBOT_FORMAT=%q: %d %+v", c.args, c.env, code, env.Raw)
		}
	}
	// The flag wins over a bad environment.
	t.Setenv("NHM_ROBOT_FORMAT", "xml")
	if _, code := robotRun(t, "--robot-version", "--robot-format=json"); code != 0 {
		t.Fatalf("--robot-format=json over NHM_ROBOT_FORMAT=xml = %d", code)
	}
}
