package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ntm's output (acfs-9hkh.58); `ensemble list` is ntm's alias of presets.
func TestEnsemblePresets(t *testing.T) {
	for _, verb := range []string{"presets", "list"} {
		code, out, _ := run("ensemble", verb)
		if code != 0 || !strings.HasPrefix(out, "NAME ") || !strings.Contains(out, "project-diagnosis") ||
			!strings.Contains(out, "\nTotal: 9 presets\n") {
			t.Fatalf("%s = %d %q", verb, code, out)
		}
	}
	code, out, _ := run("ensemble", "presets", "--verbose")
	if code != 0 || !strings.Contains(out, "  - systems-thinking") || !strings.Contains(out, "Source:      embedded") {
		t.Fatalf("presets --verbose = %d %q", code, out)
	}
	code, out, _ = run("ensemble", "list", "--format", "json")
	var res struct {
		Count   int
		Presets []struct {
			Name      string
			ModeCodes []string `json:"mode_codes"`
			ModeCount int      `json:"mode_count"`
			Source    string
		}
	}
	if err := json.Unmarshal([]byte(out), &res); code != 0 || err != nil || res.Count != 9 || len(res.Presets) != 9 {
		t.Fatalf("list --format json = %d %v %d", code, err, res.Count)
	}
	for _, p := range res.Presets {
		if len(p.ModeCodes) != p.ModeCount || p.ModeCount == 0 || p.Source != "embedded" {
			t.Fatalf("%s: %+v", p.Name, p)
		}
	}
	code, out, _ = run("ensemble", "presets", "--tag", "health", "--format", "json")
	if err := json.Unmarshal([]byte(out), &res); code != 0 || err != nil || res.Count != 1 || res.Presets[0].Name != "project-diagnosis" {
		t.Fatalf("presets --tag health = %d %v %+v", code, err, res)
	}
	if code, _, _ := run("ensemble", "spawn"); code != 2 {
		t.Fatalf("spawn without a session = %d", code)
	}
}

// A run's verbs on an enrolled instance that the Bash owns: list and
// status read; a run is refused by spawn's owner check before herdr is
// asked anything, and is recorded as failed.
func TestEnsembleRunVerbs(t *testing.T) {
	s, _ := cliInstance(t)
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run("ensemble", "runs", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "0 run(s)") {
		t.Fatalf("runs = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := run("ensemble", "status", "ens-20261012T060000-abcdef", "--socket", s.SocketPath); code != 2 || !strings.Contains(errOut, "no such run") {
		t.Fatalf("status of no run = %d %q", code, errOut)
	}
	before := len(s.Requests())
	code, out, errOut = run("ensemble", "project-diagnosis", "Is the cache safe?", "--project", project, "--socket", s.SocketPath, "--json")
	if code == 0 || !strings.Contains(out, "owned by another implementation") {
		t.Fatalf("a run under the Bash owner = %d %q %q", code, out, errOut)
	}
	if after := len(s.Requests()); after != before {
		t.Fatalf("a refused run asked herdr %d things", after-before)
	}
	code, out, _ = run("ensemble", "runs", "--socket", s.SocketPath, "--json")
	var env struct {
		Result []struct {
			ID                      string `json:"run_id"`
			Session, Status, Preset string
		}
	}
	if err := json.Unmarshal([]byte(out), &env); code != 0 || err != nil || len(env.Result) != 1 || env.Result[0].Status != "failed" ||
		env.Result[0].Preset != "project-diagnosis" {
		t.Fatalf("runs after the refused run = %d %v %+v", code, err, env.Result)
	}
	// ntm's argument is the session: it names the session's newest run.
	r := env.Result[0]
	if r.ID == "" || r.Session == "" {
		t.Fatalf("the run has no id or session: %+v", r)
	}
	for _, ref := range []string{r.ID, r.Session} {
		code, out, errOut := run("ensemble", "status", ref, "--format", "json", "--socket", s.SocketPath)
		var st struct {
			ID string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(out), &st); code != 0 || err != nil || st.ID != r.ID {
			t.Fatalf("status %s --format json = %d %v %q %q", ref, code, err, out, errOut)
		}
	}
	if code, _, _ := run("ensemble", "status", r.ID, "--format", "yaml", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("status --format yaml = %d", code)
	}
	if code, _, _ := run("ensemble", "nope", "q", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("an unknown preset = %d", code)
	}
	if code, _, _ := run("ensemble", "status", "x", "--partial", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("--partial outside synthesize = %d", code)
	}
	// acfs-9hkh.67: a run that is not running is refused (exit 2), with
	// the reason; nothing is sent.
	before = len(s.Requests())
	code, _, errOut = run("ensemble", "synthesize", r.ID, "--partial", "--socket", s.SocketPath)
	if code != 2 || !strings.Contains(errOut, "only a running run can be synthesized") || len(s.Requests()) != before {
		t.Fatalf("synthesize a failed run = %d %q", code, errOut)
	}
}
