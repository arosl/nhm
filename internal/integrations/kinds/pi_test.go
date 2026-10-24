package kinds

import (
	"slices"
	"strings"
	"testing"
)

// pi's facts come from pi 0.7.1's help and its own screen text (nhm-y7q)
// and, for the guard, from pi 0.8.0 in the sandbox (nhm-dyr); what no pi
// run here proved stays in Cannot.
func TestPiKind(t *testing.T) {
	p, ok := NewRegistry().Get("pi")
	if !ok {
		t.Fatal("no pi kind")
	}
	if p.ModelFlag != "--model" || !slices.Equal(p.Effort("high"), []string{"--thinking", "high"}) || p.Effort("") != nil ||
		p.ExitCommand != "/exit" || p.NewSession != "/new" || !slices.Equal(p.InterruptKeys, []string{"esc"}) {
		t.Fatalf("pi = %+v", p)
	}
	// The guard: nhm's extension on -e, the policy that lets it run dcg,
	// and fail-closed hooks in the pane.
	if !p.GuardSupported || p.SettingsFlag != "-e" || !slices.Equal(p.SettingsArgs, []string{"--extension-policy", "permissive"}) ||
		len(p.GuardEnv) != 1 || p.GuardEnv["PI_EXTENSION_HOOKS_FAIL_CLOSED"] != "1" {
		t.Fatalf("pi = %+v", p)
	}
	// No --yolo (the operator decides), and nothing that nhm cannot prove:
	// no resume, no trust answer, no settings file.
	if len(p.LaunchArgs) != 0 || p.ResumeArgv != nil || p.Resume("s", "", "", "") != nil || p.TrustKeys != nil ||
		p.Transcript.Dir != "" || p.QuotaSource != "" || p.GuardSettings != "" {
		t.Fatalf("pi = %+v", p)
	}
	cannot := strings.Join(p.Cannot, "; ")
	for _, want := range []string{"rch", "Agent Mail stop hook", "hub, debug and subagent", "MCP server", "TypeScript pi", "--yolo",
		"approval question", `"!" shell command`,
		"usage", "session resume", "limit", "transcript", "herdr"} {
		if !strings.Contains(cannot, want) {
			t.Errorf("pi's Cannot names no %q: %s", want, cannot)
		}
	}
	for _, gone := range []string{"interrupt key", "runs no hooks", "any guard", "nothing guards pi"} {
		if strings.Contains(cannot, gone) {
			t.Errorf("pi's Cannot still names %q: %s", gone, cannot)
		}
	}
}

// A resume adds the settings flag's args after its value, as a launch
// does (spawn.LaunchArgv).
func TestResumeAddsSettingsArgs(t *testing.T) {
	p := Lookup("pi")
	p.ResumeArgv = []string{"pi", "--session", "{session}"}
	got := p.Resume("s-1", "m", "", "/i/pi/nhm-guard.ts")
	if !slices.Equal(got, []string{"pi", "--session", "s-1", "--model", "m", "-e", "/i/pi/nhm-guard.ts", "--extension-policy", "permissive"}) {
		t.Fatalf("resume = %v", got)
	}
}
