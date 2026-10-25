package cli

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/compat"
)

// A robot flag that nhm does not answer gets its own row's disposition as
// the hint, whatever rows stand before it: --robot-ensemble never gets the
// hint of --robot-ensemble-modes, which its name is a prefix of
// (nhm-2p6.2.10).
func TestNotImplementedHintMatchesWholeFlag(t *testing.T) {
	for _, c := range []struct{ form, flag string }{
		{"`--robot-status`", "--robot-status"},
		{"`--robot-tail=S --lines=N`", "--robot-tail"},
		{"`--robot-schema=TYPE`, `--schema=TYPE`", "--robot-schema"},
		{"`--robot-limit=N` (with `--robot-status`)", "--robot-limit"},
		{"`--robot-ensemble-modes [--tier=T]`", "--robot-ensemble-modes"},
		{"`spawn <session>`", ""},
		{"`--json`, `--config F`", ""},
	} {
		if got := robotRowFlag(c.form); got != c.flag {
			t.Errorf("robotRowFlag(%q) = %q, want %q", c.form, got, c.flag)
		}
	}
	checked := 0
	for _, r := range compat.Rows() {
		flag := robotRowFlag(r.Form)
		if flag == "" || r.Disposition == "implemented" {
			continue
		}
		checked++
		if got, want := notImplementedHint(flag), r.Disposition+": "+r.Note; got != want {
			t.Errorf("%s: hint %q, want its row's %q", flag, got, want)
		}
	}
	if checked == 0 {
		t.Fatal("no robot row is deferred or waived")
	}
	env, code := robotRun(t, "--robot-ensemble=swarm")
	if code != 2 || env.ErrorCode != "NOT_IMPLEMENTED" || !strings.HasPrefix(env.Hint, "deferred: nhm-") {
		t.Fatalf("--robot-ensemble = %d %+v", code, env)
	}
}
