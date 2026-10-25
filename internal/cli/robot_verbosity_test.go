package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/robot"
)

// robotKeys runs a robot call and returns its exit code and the sorted
// top-level keys of its answer.
func robotKeys(t *testing.T, args ...string) (int, map[string]any, []string) {
	t.Helper()
	code, out, _ := run(args...)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%v: not JSON: %s", args, out)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return code, m, keys
}

// --robot-verbosity shapes a successful answer as ntm's does: terse
// shortens ntm's keys, debug adds _debug, default changes nothing. The
// flag wins, then NHM_ROBOT_VERBOSITY; ntm's NTM_ROBOT_VERBOSITY has no
// effect (nhm-2p6.2.3).
func TestRobotVerbosityTerseAndDebug(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("NHM_ROBOT_VERBOSITY", "")
	t.Setenv("NTM_ROBOT_VERBOSITY", "terse")
	t.Setenv("NHM_CONFIG", "")
	code, _, keys := robotKeys(t, "--robot-version")
	if want := []string{"_nhm", "output_format", "success", "system", "timestamp", "version"}; code != 0 || !slices.Equal(keys, want) {
		t.Fatalf("default = %d %v", code, keys)
	}
	code, _, keys = robotKeys(t, "--robot-version", "--robot-verbosity=terse")
	if want := []string{"_nhm", "of", "ok", "system", "ts", "v"}; code != 0 || !slices.Equal(keys, want) {
		t.Fatalf("terse = %d %v", code, keys)
	}
	code, m, _ := robotKeys(t, "--robot-version", "--robot-verbosity=debug", "--robot-format=json")
	debug, _ := m["_debug"].(map[string]any)
	if code != 0 || debug["verbosity"] != "debug" || debug["format"] != "json" || debug["payload_type"] != "robot.VersionOutput" ||
		debug["os"] == nil || debug["arch"] == nil || debug["go_version"] == nil || m["success"] != true {
		t.Fatalf("debug = %d %v", code, m)
	}
	t.Setenv("NHM_ROBOT_VERBOSITY", "terse")
	if _, _, keys := robotKeys(t, "--robot-version"); !slices.Contains(keys, "ok") {
		t.Fatalf("NHM_ROBOT_VERBOSITY=terse: %v", keys)
	}
	if _, _, keys := robotKeys(t, "--robot-version", "--robot-verbosity=default"); !slices.Contains(keys, "success") {
		t.Fatalf("the flag does not win over the environment: %v", keys)
	}
	// The call's verbosity ends with the call.
	if robotOut.verbosity != robot.VerbosityDefault || robotOut.format != "auto" {
		t.Fatalf("robotOut outlived the call: %+v", robotOut)
	}
}

// An error envelope is never shaped, and a verbosity ntm does not know is
// its INVALID_FLAG, with ntm's hint, exit 1.
func TestRobotVerbosityErrorsAndRefusals(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("NHM_ROBOT_VERBOSITY", "")
	t.Setenv("NHM_CONFIG", "")
	code, m, _ := robotKeys(t, "--robot-version", "--robot-snapshot", "--robot-verbosity=terse")
	if code != robotError || m["success"] != false || m["error_code"] != "INVALID_FLAG" {
		t.Fatalf("an error under terse = %d %v", code, m)
	}
	for _, c := range []struct{ flag, env string }{{"--robot-verbosity=loud", ""}, {"", "loud"}} {
		t.Setenv("NHM_ROBOT_VERBOSITY", c.env)
		args := []string{"--robot-version"}
		if c.flag != "" {
			args = append(args, c.flag)
		}
		env, code := robotRun(t, args...)
		if code != robotError || env.ErrorCode != "INVALID_FLAG" || env.Hint != "Use terse, default, or debug" ||
			!strings.Contains(env.Error, `invalid robot verbosity "loud": must be terse, default, or debug`) {
			t.Errorf("%v with NHM_ROBOT_VERBOSITY=%q: %d %+v", args, c.env, code, env)
		}
	}
}

// The config's [robot] verbosity counts after the environment, for the
// calls that read the config.
func TestRobotVerbosityFromConfig(t *testing.T) {
	t.Setenv("NHM_ROBOT_VERBOSITY", "")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("[robot]\nverbosity = \"debug\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NHM_CONFIG", cfg)
	for _, c := range []struct {
		flag, env string
		useConfig bool
		want      robot.RobotVerbosity
	}{
		{"", "", true, robot.VerbosityDebug},
		{"", "", false, robot.VerbosityDefault},
		{"", "terse", true, robot.VerbosityTerse},
		{"default", "terse", true, robot.VerbosityDefault},
	} {
		t.Setenv("NHM_ROBOT_VERBOSITY", c.env)
		if got, err := resolveRobotVerbosity(c.flag, c.useConfig); err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
}
