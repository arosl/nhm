package cli

import (
	"context"
	"flag"
	"io"
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
