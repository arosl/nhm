package kinds

import (
	"slices"
	"strings"
	"testing"
)

// pi's facts come from pi 0.7.1's help and its own screen text; what no
// pi run here proved stays in Cannot (nhm-y7q).
func TestPiKind(t *testing.T) {
	p, ok := NewRegistry().Get("pi")
	if !ok {
		t.Fatal("no pi kind")
	}
	if p.ModelFlag != "--model" || !slices.Equal(p.Effort("high"), []string{"--thinking", "high"}) || p.Effort("") != nil ||
		p.ExitCommand != "/exit" || p.NewSession != "/new" || !slices.Equal(p.InterruptKeys, []string{"esc"}) {
		t.Fatalf("pi = %+v", p)
	}
	// No --yolo while nhm has no guard for pi, and nothing that nhm
	// cannot prove: no resume, no trust answer, no settings file.
	if len(p.LaunchArgs) != 0 || p.GuardSupported || p.SettingsFlag != "" || p.ResumeArgv != nil ||
		p.Resume("s", "", "", "") != nil || p.TrustKeys != nil || p.Transcript.Dir != "" || p.QuotaSource != "" {
		t.Fatalf("pi = %+v", p)
	}
	cannot := strings.Join(p.Cannot, "; ")
	for _, want := range []string{"guard", "--yolo", "usage", "session resume", "limit", "transcript", "herdr"} {
		if !strings.Contains(cannot, want) {
			t.Errorf("pi's Cannot names no %q: %s", want, cannot)
		}
	}
	for _, gone := range []string{"interrupt key", "runs no hooks"} {
		if strings.Contains(cannot, gone) {
			t.Errorf("pi's Cannot still names %q: %s", gone, cannot)
		}
	}
}
