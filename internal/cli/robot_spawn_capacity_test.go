package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A capacity refusal names both ways out (nhm-1pq).
const (
	capacityWayOutFlag   = "--ignore-capacity for this spawn"
	capacityWayOutConfig = "capacity = [] under [providers] in ~/.config/nhm/config.toml"
)

// redCapacityConfig writes nhm's config with a capacity provider that
// answers red as ACFS's guard does (JSON, exit 1), and admits Claude's
// unknown quota, so that only the capacity and the guards decide.
func redCapacityConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	provider := filepath.Join(t.TempDir(), "capacity")
	writeAll(t, provider, "#!/bin/sh\nprintf '%s\\n' '{\"status\":\"red\",\"reasons\":[\"MemAvailable 3000 MiB < 4096 MiB\"]}'\nexit 1\n")
	if err := os.Chmod(provider, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAll(t, filepath.Join(home, ".config", "nhm", "config.toml"),
		fmt.Sprintf("[providers]\ncapacity = [%q]\n[quota]\nadmit_unknown = [\"claude\"]\n", provider))
}

// --robot-spawn takes --ignore-capacity, spelled as spawn's, and passes
// it to the spawn; without it a red capacity refuses.
func TestRobotSpawnIgnoreCapacity(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--ignore-capacity"}, true},
		{[]string{"--ignore-capacity=false"}, false},
	} {
		fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		rf := addRobotSpawnFlags(fs)
		args := append([]string{"--robot-spawn=fresh", "--spawn-dir=/p", "--spawn-cc=1", "--spawn-safety", "--robot-format=json"}, c.args...)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if req, _, refusal := robotSpawnRequest(fs, rf); refusal != nil || req.IgnoreCapacity != c.want || !req.RequireObservations {
			t.Errorf("%s: IgnoreCapacity %v, want %v (request %+v)", strings.Join(c.args, " "), req.IgnoreCapacity, c.want, req)
		}
	}

	// The runner, in a dry run: no dcg refuses the launch either way, so
	// the capacity's part is in the reasons or in the warnings. A dry run
	// reports the decision and succeeds, as ntm's does.
	s, _, _ := newSendInstance(t)
	redCapacityConfig(t)
	t.Setenv("PATH", t.TempDir())
	args := []string{"--robot-spawn=fresh", "--spawn-dir=" + t.TempDir(), "--spawn-cc=1", "--spawn-safety",
		"--robot-format=json", "--dry-run", "--socket=" + s.SocketPath}
	out, code := robotJSON(t, args...)
	ad, _ := out["admission"].(map[string]any)
	reasons, warnings := fmt.Sprint(ad["reasons"]), fmt.Sprint(ad["warnings"])
	if code != 0 || out["success"] != true || ad["decision"] != "refuse" ||
		!strings.Contains(reasons, "capacity red: MemAvailable 3000 MiB < 4096 MiB (to start anyway: "+capacityWayOutFlag+", or "+capacityWayOutConfig) ||
		strings.Contains(warnings, "capacity") {
		t.Fatalf("red = %d %v", code, out)
	}
	out, code = robotJSON(t, append(args, "--ignore-capacity")...)
	ad, _ = out["admission"].(map[string]any)
	reasons, warnings = fmt.Sprint(ad["reasons"]), fmt.Sprint(ad["warnings"])
	if code != 0 || ad["decision"] != "refuse" || strings.Contains(reasons, "capacity") ||
		!strings.Contains(warnings, "capacity red: MemAvailable 3000 MiB < 4096 MiB (overridden by --ignore-capacity)") {
		t.Fatalf("red, --ignore-capacity = %d %v", code, out)
	}
}

// nhm spawn and add take --ignore-capacity: a red capacity refuses with
// exit 3 and names both ways out; with the flag the spawn goes on, with
// the warning.
func TestSpawnIgnoreCapacity(t *testing.T) {
	s, _, _ := newSendInstance(t)
	redCapacityConfig(t)
	t.Setenv("PATH", t.TempDir())
	args := []string{"spawn", "swarm", "--cc=1", "--project", t.TempDir(), "--no-kickoff", "--allow-unguarded", "--dry-run", "--socket", s.SocketPath}
	code, _, errOut := run(args...)
	if code != exitError || !strings.Contains(errOut, "Error: admission refused the spawn: capacity red: MemAvailable 3000 MiB < 4096 MiB") ||
		!strings.Contains(errOut, capacityWayOutFlag) || !strings.Contains(errOut, capacityWayOutConfig) {
		t.Fatalf("red = %d %s", code, errOut)
	}
	code, out, errOut := run(append(args, "--ignore-capacity")...)
	if code != 0 || !strings.Contains(out, "would start 1 agents in swarm (w1)") ||
		!strings.Contains(errOut, "warning: capacity red: MemAvailable 3000 MiB < 4096 MiB (overridden by --ignore-capacity)") {
		t.Fatalf("red, --ignore-capacity = %d %s %s", code, out, errOut)
	}
}
