package cli

import (
	"strings"
	"testing"
)

// acfs-9hkh.48: quota and limits are reached through Run, as `nhm quota`
// and `nhm limits`, not only by calling their handlers.
func TestQuotaAndLimitsDispatch(t *testing.T) {
	s := quotaInstance(t)
	code, out, errOut := run("quota", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "codex (pro)") {
		t.Fatalf("nhm quota = %d\n%s%s", code, out, errOut)
	}
	for _, verb := range [][]string{{"quota"}, {"quota", "check"}, {"limits"}} {
		_, _, errOut := run(append(verb, "--help")...)
		want := "Usage: nhm " + verb[0]
		if !strings.Contains(errOut, want) || strings.Contains(errOut, "unknown command") {
			t.Errorf("nhm %s --help: stderr %q lacks %q", strings.Join(verb, " "), errOut, want)
		}
	}
	if !strings.Contains(usage, "nhm quota") || !strings.Contains(usage, "nhm limits") {
		t.Errorf("nhm help does not list quota and limits:\n%s", usage)
	}
}
