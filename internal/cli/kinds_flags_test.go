package cli

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// send's kind flags come from the kind registry: every name and flag,
// and send's own flags win.
func TestSendKindFlagsFromTheRegistry(t *testing.T) {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("all", false, "")
	sel := registerKindFlags(fs)
	got := map[string]string{}
	for _, k := range sel {
		got[k.flag] = k.kind
	}
	for flagName, kind := range map[string]string{"cc": "claude", "claude": "claude", "cod": "codex", "agy": "agy",
		"pi": "pi", "omp": "omp", "oc": "opencode", "opencode": "opencode", "grok": "grok"} {
		if got[flagName] != kind {
			t.Errorf("--%s selects %q, want %q", flagName, got[flagName], kind)
		}
	}
	if err := fs.Parse([]string{"--oc", "--cc", "--claude"}); err != nil {
		t.Fatal(err)
	}
	req, err := sendFlags{kindSel: sel, when: "ready"}.request("hi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(req.Selector.Kinds, ",") != "claude,opencode" {
		t.Fatalf("kinds = %v", req.Selector.Kinds)
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
