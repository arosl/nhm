package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// pacingConfig writes nhm's config with a [spawn_pacing] table that
// caps Claude at one agent, the retired [providers] capacity key, and
// [quota] admit_unknown for Claude, so that only ntm's admission and the
// guards decide (nhm-a421).
func pacingConfig(t *testing.T, pacing string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := filepath.Join(home, ".config", "nhm", "config.toml")
	writeAll(t, path, "[providers]\ncapacity = [\"acfs\", \"capacity\", \"--guard\", \"--json\"]\n[quota]\nadmit_unknown = [\"claude\"]\n"+pacing)
	return path
}

const claudeCapOfOne = "[spawn_pacing.agent_type_limits]\nclaude = 1\n"

// --robot-spawn takes --ignore-capacity, spelled as spawn's, and runs
// ntm's admission: over an agent cap it refuses, in ntm's words; with the
// flag the refusal is a warning. A config that still sets [providers]
// capacity gets one warning that nhm ignores it.
func TestRobotSpawnIgnoreCapacity(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--ignore-capacity"}, true},
		{[]string{"--ignore-capacity=false"}, false},
	} {
		fs, _ := newRobotFlagSet(nil, io.Discard)
		rf := addRobotSpawnFlags(fs)
		args := append([]string{"--robot-spawn=fresh", "--spawn-dir=/p", "--spawn-cc=1", "--spawn-safety", "--robot-format=json"}, c.args...)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if req, _, refusal := robotSpawnRequest(fs, rf); refusal != nil || req.IgnoreCapacity != c.want || !req.RequireObservations || !req.NTMAdmission {
			t.Errorf("%s: IgnoreCapacity %v, want %v (request %+v)", strings.Join(c.args, " "), req.IgnoreCapacity, c.want, req)
		}
	}

	// The runner, in a dry run: no dcg refuses the launch either way, so
	// ntm's part is in the reasons or in the warnings. A dry run reports
	// the decision and succeeds, as ntm's does.
	s, _, _ := newSendInstance(t)
	path := pacingConfig(t, claudeCapOfOne)
	t.Setenv("PATH", t.TempDir())
	args := []string{"--robot-spawn=fresh", "--spawn-dir=" + t.TempDir(), "--spawn-cc=2", "--spawn-safety",
		"--robot-format=json", "--dry-run", "--socket=" + s.SocketPath}
	limit := "spawn admission refuse: agent_type_limit_exceeded (claude: running 0 + requested 2 exceeds per-type cap 1"
	retired := "[providers] capacity in " + path + " is ignored: nhm's own admission (ntm's) decides; remove the key"
	out, code := robotJSON(t, args...)
	ad, _ := out["admission"].(map[string]any)
	reasons, warnings := fmt.Sprint(ad["reasons"]), fmt.Sprint(ad["warnings"])
	if code != 0 || out["success"] != true || ad["decision"] != "refuse" || !strings.Contains(reasons, limit) ||
		strings.Contains(warnings, "agent_type_limit") || strings.Count(warnings, retired) != 1 ||
		ad["requested_agents"] != float64(2) || ad["agent_inventory_available"] != true || ad["pressure_level"] == nil {
		t.Fatalf("capped = %d %v", code, out)
	}
	limits, _ := ad["agent_type_limits"].([]any)
	if row, _ := limits[0].(map[string]any); len(limits) != 1 || row["agent_type"] != "claude" || row["blocks_request"] != true {
		t.Fatalf("agent_type_limits = %v", ad["agent_type_limits"])
	}
	out, code = robotJSON(t, append(args, "--ignore-capacity")...)
	ad, _ = out["admission"].(map[string]any)
	reasons, warnings = fmt.Sprint(ad["reasons"]), fmt.Sprint(ad["warnings"])
	if code != 0 || ad["decision"] != "refuse" || strings.Contains(reasons, "agent_type_limit") ||
		!strings.Contains(warnings, limit) || !strings.Contains(warnings, "(overridden by --ignore-capacity)") {
		t.Fatalf("capped, --ignore-capacity = %d %v", code, out)
	}
}

// nhm add runs ntm's admission and refuses in ntm's words; spawn runs
// none, as ntm's; --ignore-capacity starts the add, with the warning.
// A bad [spawn_pacing] stops both, naming the key.
func TestSpawnIgnoreCapacity(t *testing.T) {
	s, _, _ := newSendInstance(t)
	path := pacingConfig(t, claudeCapOfOne)
	t.Setenv("PATH", t.TempDir())
	common := []string{"swarm", "--cc=2", "--project", t.TempDir(), "--no-kickoff", "--allow-unguarded", "--dry-run", "--socket", s.SocketPath}
	retired := "warning: [providers] capacity in " + path + " is ignored"
	code, out, errOut := run(append([]string{"spawn"}, common...)...)
	if code != 0 || !strings.Contains(out, "would start 2 agents in swarm (w1)") || strings.Count(errOut, retired) != 1 {
		t.Fatalf("spawn = %d %s %s", code, out, errOut)
	}
	code, _, errOut = run(append([]string{"add"}, common...)...)
	if code != exitError || !strings.Contains(errOut,
		"Error: agent addition refuse: agent_type_limit_exceeded; claude: running 0 + requested 2 exceeds per-type cap 1; "+
			"reduce that agent type or adjust spawn_pacing.agent_type_limits.claude") {
		t.Fatalf("add = %d %s", code, errOut)
	}
	code, out, errOut = run(append([]string{"add"}, append(common, "--ignore-capacity")...)...)
	if code != 0 || !strings.Contains(out, "would start 2 agents in swarm (w1)") ||
		!strings.Contains(errOut, "warning: spawn admission refuse: agent_type_limit_exceeded") || !strings.Contains(errOut, "(overridden by --ignore-capacity)") {
		t.Fatalf("add, --ignore-capacity = %d %s %s", code, out, errOut)
	}
	pacingConfig(t, "[spawn_pacing]\nmax_concurrent_spawns = 0\n")
	code, _, errOut = run(append([]string{"spawn"}, common...)...)
	if code != exitError || !strings.Contains(errOut, "[spawn_pacing] max_concurrent_spawns must be at least 1, got 0") {
		t.Fatalf("bad config = %d %s", code, errOut)
	}
}
