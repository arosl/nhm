package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModesList(t *testing.T) {
	code, out, _ := run("modes", "list")
	if code != 0 || !strings.Contains(out, "deductive") || !strings.HasSuffix(strings.TrimSpace(out), "mode(s)") {
		t.Fatalf("list = %d %q", code, out)
	}
	// ntm's --format json: the bare list, core tier by default.
	code, out, _ = run("modes", "list", "--format", "json")
	var core []map[string]any
	if err := json.Unmarshal([]byte(out), &core); code != 0 || err != nil {
		t.Fatalf("list --format json = %d %v", code, err)
	}
	for _, m := range core {
		if m["tier"] != "core" {
			t.Fatalf("a %v mode in the default list", m["tier"])
		}
	}
	code, out, _ = run("modes", "list", "--all", "--json")
	var env struct {
		Schema string
		Result []map[string]any
	}
	if err := json.Unmarshal([]byte(out), &env); code != 0 || err != nil || env.Schema != "nhm.v1" || len(env.Result) != 80 {
		t.Fatalf("list --all --json = %d %v %d", code, err, len(env.Result))
	}
	code, out, _ = run("modes", "list", "-a", "-c", "causal", "--format", "json")
	var causal []map[string]any
	if err := json.Unmarshal([]byte(out), &causal); code != 0 || err != nil || len(causal) == 0 {
		t.Fatalf("list --category causal = %d %v", code, err)
	}
	for _, m := range causal {
		if m["category"] != "Causal" {
			t.Fatalf("category filter let %v through", m["category"])
		}
	}
	if code, _, _ := run("modes", "list", "--tier", "legendary"); code != 2 {
		t.Fatalf("unknown tier = %d", code)
	}
	if code, _, _ := run("modes", "list", "--format", "yaml"); code != 2 {
		t.Fatalf("yaml format = %d", code)
	}
}

func TestModesExplain(t *testing.T) {
	code, out, _ := run("modes", "explain", "A1")
	if code != 0 || !strings.HasPrefix(out, "Deductive Inference (A1) deductive") || !strings.Contains(out, "Differentiator:") {
		t.Fatalf("explain A1 = %d %q", code, out)
	}
	code, out, _ = run("modes", "explain", "bayesian", "--format", "json")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); code != 0 || err != nil || m["id"] != "bayesian" {
		t.Fatalf("explain bayesian = %d %v %v", code, err, m)
	}
	if code, _, errOut := run("modes", "explain", "telepathy"); code != 2 || !strings.Contains(errOut, "unknown mode") {
		t.Fatalf("unknown mode = %d %q", code, errOut)
	}
	if code, _, _ := run("modes"); code != 2 {
		t.Fatalf("no verb = %d", code)
	}
}
