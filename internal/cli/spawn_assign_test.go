package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

// ntm's resolveRobotSharedFlag: an explicit --spawn-assign-strategy wins,
// then an explicit --strategy, then the default top-n.
func TestRobotAssignStrategyResolution(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "top-n"},
		{[]string{"--strategy=diverse"}, "diverse"},
		{[]string{"--spawn-assign-strategy=skill-matched"}, "skill-matched"},
		{[]string{"--strategy=diverse", "--spawn-assign-strategy=dependency-aware"}, "dependency-aware"},
	} {
		fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := addRobotAssignFlags(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if got := f.strategyValue(); got != tc.want {
			t.Fatalf("%v: strategy=%q, want %q", tc.args, got, tc.want)
		}
	}
}

// --robot-spawn --spawn-assign-work runs the preflight before the spawn,
// in a dry run too: a refusal is the spawn's answer, with ntm's code and
// assignments [], and nothing is planned or created.
func TestRobotSpawnAssignPreflightComesFirst(t *testing.T) {
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	base := []string{"--robot-spawn=swarm", "--spawn-dir=" + project, "--spawn-cc=1", "--spawn-no-user", "--spawn-assign-work",
		"--robot-format=json", "--dry-run", "--socket=" + s.SocketPath}
	for _, tc := range []struct {
		extra []string
		code  string
		says  string
	}{
		{[]string{"--require-reservation"}, robot.ErrCodeInvalidFlag, "does not reserve files"},
		{[]string{"--strategy=fastest"}, robot.ErrCodeInvalidFlag, "fastest"},
		// No bv on PATH: the actionable plan cannot be verified.
		{nil, robot.ErrCodeInternalError, "verify actionable spawn work"},
	} {
		out, code := robotJSON(t, append(append([]string(nil), base...), tc.extra...)...)
		assignments, ok := out["assignments"].([]any)
		if code != robotError || out["success"] != false || out["error_code"] != tc.code ||
			!strings.Contains(fmt.Sprint(out["error"]), tc.says) || !ok || len(assignments) != 0 {
			t.Fatalf("%v: %d %v", tc.extra, code, out)
		}
		if _, planned := out["would_create"]; planned || out["working_dir"] != project {
			t.Fatalf("%v: the spawn ran after a refused preflight: %v", tc.extra, out)
		}
	}
}

// The preflight refuses ntm's reservation flags before it reads anything,
// and an unknown strategy with INVALID_FLAG.
func TestRobotAssignPreflightRefusals(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ie := &instanceEnv{inst: store.Instance{ID: "inst-000000000000"}}
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"--require-reservation"}, robot.ErrCodeInvalidFlag},
		{[]string{"--reservation-paths=internal/**"}, robot.ErrCodeInvalidFlag},
		{[]string{"--strategy=fastest"}, robot.ErrCodeInvalidFlag},
	} {
		fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := addRobotAssignFlags(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		_, _, refusal := robotAssignPreflight(context.Background(), ie, f, t.TempDir())
		if refusal == nil || refusal.ErrorCode != tc.code || refusal.Success {
			t.Fatalf("%v: refusal=%+v", tc.args, refusal)
		}
	}
}

// ntm's reservation flags are --require-reservation and
// --reservation-paths (its root flags): nhm refuses them in the
// preflight, as it reserves no files for agents. The --spawn- names,
// which ntm does not have, are unknown flags.
func TestRobotSpawnAssignReservationFlagNames(t *testing.T) {
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	base := []string{"--robot-spawn=swarm", "--spawn-dir=" + t.TempDir(), "--spawn-cc=1", "--spawn-assign-work", "--robot-format=json",
		"--dry-run", "--socket=" + s.SocketPath}
	for _, tc := range []struct {
		flag, says string
	}{
		{"--require-reservation", "nhm does not reserve files for spawn assignments (--require-reservation, --reservation-paths)"},
		{"--reservation-paths=internal/**", "nhm does not reserve files"},
		{"--spawn-require-reservation", "flag provided but not defined: -spawn-require-reservation"},
		{"--spawn-reservation-paths=internal/**", "flag provided but not defined: -spawn-reservation-paths"},
	} {
		out, code := robotJSON(t, append(append([]string(nil), base...), tc.flag)...)
		if code != robotError || out["error_code"] != robot.ErrCodeInvalidFlag || !strings.Contains(fmt.Sprint(out["error"]), tc.says) {
			t.Errorf("%s: %d %v", tc.flag, code, out)
		}
	}
}

// ntm's --no-cass turns the work prompts' CASS context off, over
// --with-cass and the config; CASS and CM otherwise follow the flags or
// nhm's config ([cass] context, [memory] cm), as --robot-send's do.
func TestRobotSpawnAssignNoCass(t *testing.T) {
	for _, tc := range []struct {
		config string
		args   []string
		want   spawnassign.PromptContextOptions
	}{
		{"", nil, spawnassign.PromptContextOptions{}},
		{"", []string{"--with-cass", "--with-memory"}, spawnassign.PromptContextOptions{WithCASS: true, WithMemory: true}},
		{"", []string{"--with-cass", "--no-cass"}, spawnassign.PromptContextOptions{}},
		{"[cass]\ncontext = \"best-effort\"\n", nil, spawnassign.PromptContextOptions{WithCASS: true}},
		{"[cass]\ncontext = \"best-effort\"\n", []string{"--no-cass"}, spawnassign.PromptContextOptions{}},
		{"[memory]\ncm = \"best-effort\"\n", []string{"--no-cass"}, spawnassign.PromptContextOptions{WithMemory: true}},
	} {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		if tc.config != "" {
			writeAll(t, filepath.Join(dir, "nhm", "config.toml"), tc.config)
		}
		cfg, err := loadEnrichConfig()
		if err != nil {
			t.Fatal(err)
		}
		fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		f := addRobotAssignFlags(fs)
		if err := fs.Parse(tc.args); err != nil {
			t.Fatal(err)
		}
		if got := f.promptContext(cfg); got != tc.want {
			t.Errorf("%q %v = %+v, want %+v", tc.config, tc.args, got, tc.want)
		}
		// --no-cass enters the operation's fingerprint only when given.
		if opts := f.options("p"); strings.Contains(opts, "no_cass") != slices.Contains(tc.args, "--no-cass") {
			t.Errorf("%v: options %s", tc.args, opts)
		}
	}
}
