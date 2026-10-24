package spawn

import (
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// A role is "reviewer" or nothing: reap retires a bead-less agent only
// when it was spawned as a reviewer.
func TestRequestRole(t *testing.T) {
	r := req(Want{Kind: "claude", Count: 1})
	r.Role = RoleReviewer
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Role = "boss"
	if err := r.Validate(); err == nil {
		t.Fatal("an unknown role was accepted")
	}
	if !ValidModel("claude-opus-5-5") || ValidModel("--dangerously-skip-permissions") {
		t.Fatal("ValidModel")
	}
}

// Validate takes what ntm's grammar gives: a model with '@' or '+', a
// repeated kind, and an effort on a kind without one, which the launch
// drops, as ntm's does. nhm's own rule: a model or an effort starts with
// a letter or a number, since each is a value of its own in the argv.
func TestValidateTakesNTMSpecs(t *testing.T) {
	r := req(Want{Kind: "claude", Count: 2, Model: "opus"}, Want{Kind: "opencode", Count: 1, Model: "openrouter/m@beta", Effort: "high"},
		Want{Kind: "claude", Count: 1, Model: "vendor/model+x", Effort: "max"})
	if err := r.Validate(); err != nil || r.Count() != 4 {
		t.Fatalf("Validate = %v, count %d", err, r.Count())
	}
	for _, w := range []Want{{Kind: "claude", Count: 1, Model: "-x"}, {Kind: "claude", Count: 1, Model: ".hidden"}} {
		r := req(w)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "starts with a letter or a number") {
			t.Errorf("model %q = %v", w.Model, err)
		}
	}
	r = req(Want{Kind: "codex", Count: 1, Effort: "-c"})
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "invalid reasoning effort") {
		t.Errorf("effort -c = %v", err)
	}
}

// grok, omp and opencode take ntm's model flag, and grok and omp ntm's
// effort flag (ntm's DefaultAgentTemplates and DefaultOmpCommand); a kind
// without an effort flag drops the effort.
func TestLaunchArgvModelAndEffort(t *testing.T) {
	for _, tt := range []struct {
		kind, model, effort string
		want                []string
	}{
		{"grok", "grok-5", "high", []string{"grok", "--model", "grok-5", "--effort", "high"}},
		{"omp", "opus", "xhigh", []string{"omp", "--model", "opus", "--thinking", "xhigh"}},
		{"opencode", "openrouter/m@beta", "high", []string{"opencode", "--model", "openrouter/m@beta"}},
		{"grok", "", "", []string{"grok"}},
	} {
		if got := LaunchArgv(kinds.Lookup(tt.kind), tt.model, tt.effort, ""); !slices.Equal(got, tt.want) {
			t.Errorf("%s %q %q = %q, want %q", tt.kind, tt.model, tt.effort, got, tt.want)
		}
	}
}
