package cli

import (
	"runtime"
	"slices"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// --robot-version is ntm's VersionOutput: the envelope and system{} with
// ntm's keys, tmux_available false, and nhm's extras under _nhm. It needs
// no herdr (nhm-2p6.2.1).
func TestRobotVersion(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	env, code := robotRun(t, "--robot-version")
	if code != 0 || !env.Success {
		t.Fatalf("--robot-version = %d %+v", code, env)
	}
	var keys []string
	for k := range env.Raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"_nhm", "output_format", "success", "system", "timestamp", "version"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	sys := env.Raw["system"].(map[string]any)
	if sys["version"] != Version || sys["commit"] != Commit || sys["build_date"] != Date ||
		sys["go_version"] != runtime.Version() || sys["os"] != runtime.GOOS || sys["arch"] != runtime.GOARCH ||
		sys["tmux_available"] != false || len(sys) != 7 {
		t.Fatalf("system = %v", sys)
	}
	nhm := env.Raw["_nhm"].(map[string]any)
	if nhm["ntm_commit"] != NTMCommit || nhm["herdr_protocol"] != float64(herdr.Protocol) {
		t.Fatalf("_nhm = %v", nhm)
	}
	// ntm's default format is auto, and its envelope says so.
	if env.Raw["output_format"] != "auto" || env.Raw["version"] != "1.0.0" {
		t.Fatalf("envelope = %v", env.Raw)
	}
	// ntm's root flags after the robot flag, and json, work; a format ntm
	// does not know is refused (TOON: robot_toon_test.go).
	if env, code := robotRun(t, "--robot-version", "--json", "--robot-format=json"); code != 0 || env.Raw["output_format"] != "json" {
		t.Fatalf("with --json and --robot-format=json = %d %v", code, env.Raw)
	}
	if env, code := robotRun(t, "--robot-version", "--robot-format=xml"); code != 1 || env.ErrorCode != "INVALID_FLAG" {
		t.Fatalf("xml = %d %+v", code, env)
	}
}
