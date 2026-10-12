package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ntm's output (acfs-9hkh.58): its table, its types under --format json and
// yaml, and nhm's envelope under --json.
func TestModesList(t *testing.T) {
	code, out, _ := run("modes", "list")
	if code != 0 || !strings.HasPrefix(out, "  CODE  ID ") || !strings.Contains(out, "deductive") || !strings.HasSuffix(out, " modes total\n") {
		t.Fatalf("list = %d %q", code, out)
	}
	code, out, _ = run("modes", "list", "--format", "json")
	var core struct {
		Modes []map[string]any
		Count int
	}
	if err := json.Unmarshal([]byte(out), &core); code != 0 || err != nil || core.Count != len(core.Modes) || core.Count == 0 {
		t.Fatalf("list --format json = %d %v %q", code, err, out)
	}
	for _, m := range core.Modes {
		if m["tier"] != "core" || m["short_desc"] == "" {
			t.Fatalf("a %v mode in the default list", m["tier"])
		}
	}
	code, out, _ = run("modes", "list", "--all", "--json")
	var env struct {
		Schema string
		Result struct{ Count int }
	}
	if err := json.Unmarshal([]byte(out), &env); code != 0 || err != nil || env.Schema != "nhm.v1" || env.Result.Count != 80 {
		t.Fatalf("list --all --json = %d %v %+v", code, err, env)
	}
	code, out, _ = run("modes", "list", "-a", "-c", "causal", "--format", "yaml")
	var causal struct {
		Modes  []map[string]any
		Filter string
	}
	if err := yaml.Unmarshal([]byte(out), &causal); code != 0 || err != nil || len(causal.Modes) == 0 || causal.Filter != "category=causal" {
		t.Fatalf("list --category causal --format yaml = %d %v %q", code, err, out)
	}
	for _, m := range causal.Modes {
		if m["category"] != "Causal" {
			t.Fatalf("category filter let %v through", m["category"])
		}
	}
	// As ntm: an unknown tier lists nothing.
	if code, out, _ := run("modes", "list", "--tier", "legendary"); code != 0 || out != "No modes found matching criteria\n" {
		t.Fatalf("unknown tier = %d %q", code, out)
	}
	if code, _, _ := run("modes", "list", "--format", "xml"); code != 2 {
		t.Fatalf("xml format = %d", code)
	}
}

func TestModesExplain(t *testing.T) {
	code, out, _ := run("modes", "explain", "A1")
	if code != 0 || !strings.HasPrefix(out, "📋 Deductive Inference (A1)\n   ID: deductive | Category: Formal | Tier: core\n") ||
		!strings.Contains(out, "What makes it unique:") || !strings.Contains(out, "Pairs well with: ") {
		t.Fatalf("explain A1 = %d %q", code, out)
	}
	code, out, _ = run("modes", "explain", "bayesian", "--format", "json")
	var m struct {
		Card struct {
			ModeID      string `json:"mode_id"`
			TypicalCost int    `json:"typical_cost"`
		}
	}
	if err := json.Unmarshal([]byte(out), &m); code != 0 || err != nil || m.Card.ModeID != "bayesian" || m.Card.TypicalCost == 0 {
		t.Fatalf("explain bayesian = %d %v %+v", code, err, m)
	}
	if code, _, errOut := run("modes", "explain", "telepathy"); code != 2 || !strings.Contains(errOut, "unknown mode") {
		t.Fatalf("unknown mode = %d %q", code, errOut)
	}
	if code, _, _ := run("modes"); code != 2 {
		t.Fatalf("no verb = %d", code)
	}
}
