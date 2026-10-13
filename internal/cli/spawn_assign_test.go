package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"testing"

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
		{[]string{"--spawn-require-reservation"}, robot.ErrCodeInvalidFlag, "does not reserve files"},
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
		{[]string{"--spawn-require-reservation"}, robot.ErrCodeInvalidFlag},
		{[]string{"--spawn-reservation-paths=internal/**"}, robot.ErrCodeInvalidFlag},
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
