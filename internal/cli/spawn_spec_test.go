package cli

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/arosl/nhm/internal/core/spawn"
)

// parseSpawnArgs parses spawn's flags as the verb does and builds the
// request.
func parseSpawnArgs(t *testing.T, args ...string) (spawn.Request, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	fs := pflag.NewFlagSet("spawn", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := addSpawnFlags(fs)
	if err := fs.Parse(args); err != nil {
		return spawn.Request{}, err
	}
	if fs.NArg() != 1 {
		t.Fatalf("positional = %q", fs.Args())
	}
	return f.request(fs.Arg(0), false)
}

// ntm's count flags add up: each value is a spec of its own, kept in the
// order of the flags, with its model and effort (ntm's AgentSpecs).
func TestParseCountRepeatsAddUp(t *testing.T) {
	req, err := parseSpawnArgs(t, "swarm", "--cc=2:opus", "--cod=1:gpt-5:high", "--cc=1:sonnet@max", "--agent", "agy=1", "--claude=1")
	if err != nil {
		t.Fatal(err)
	}
	want := []spawn.Want{{Kind: "claude", Count: 2, Model: "opus"}, {Kind: "codex", Count: 1, Model: "gpt-5", Effort: "high"},
		{Kind: "claude", Count: 1, Model: "sonnet", Effort: "max"}, {Kind: "agy", Count: 1}, {Kind: "claude", Count: 1}}
	if !reflect.DeepEqual(req.Agents, want) {
		t.Fatalf("agents = %+v, want %+v", req.Agents, want)
	}
	if req.Count() != 6 {
		t.Fatalf("count = %d", req.Count())
	}
}

// A bad spec stops the parse with ntm's words, at the flag.
func TestSpawnSpecErrors(t *testing.T) {
	for _, tt := range []struct {
		arg, want string
	}{
		{"--cc=0", "count must be at least 1, got 0"},
		{"--cod=x", `invalid count in agent spec: "x"`},
		{"--agy=1:some-model", `antigravity model overrides are not supported; model is pinned to "Gemini 3.8 Flash (High)" (use --agy=N)`},
		{"--cc=1:m@high:max", "sets reasoning effort twice"},
	} {
		_, err := parseSpawnArgs(t, "swarm", tt.arg)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s = %v, want %q", tt.arg, err, tt.want)
		}
	}
}
