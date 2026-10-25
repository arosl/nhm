package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// send's type flags are ntm's (--cc, --cod, --gmi, --agy, --grok, --oc,
// --omp), each with an optional variant; every other name and flag of
// the kind registry gets one too (--claude, --pi …).
func TestSendKindFlagsFromTheRegistry(t *testing.T) {
	cmd, f := newSendCommand()
	for _, name := range []string{"cc", "cod", "gmi", "agy", "grok", "oc", "omp", "claude", "codex", "pi", "opencode"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("no --%s", name)
		}
	}
	if err := cmd.ParseFlags([]string{"--oc", "--cc=opus", "--claude", "--pi", "--omp=false"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range f.targets {
		got = append(got, tg.kind+":"+tg.variant)
	}
	if want := []string{"opencode:", "claude:opus", "claude:", "pi:"}; !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
}

func TestDoctorReportsTheKindsConfig(t *testing.T) {
	if c := kindsCheck(nil, []string{"claude", "codex"}); c.Result != checkOK || c.Detail != "claude, codex" {
		t.Fatalf("ok = %+v", c)
	}
	c := kindsCheck(errors.New("config.toml: unknown keys: kinds.mine.flagz"), []string{"claude"})
	if c.Result != checkWarn || !strings.Contains(c.Detail, "flagz") || c.Fix == "" {
		t.Fatalf("broken = %+v", c)
	}
}
