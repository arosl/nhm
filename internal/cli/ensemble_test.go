package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsemblePresets(t *testing.T) {
	code, out, _ := run("ensemble", "presets")
	if code != 0 || !strings.Contains(out, "project-diagnosis") || len(strings.Split(strings.TrimSpace(out), "\n")) != 9 {
		t.Fatalf("presets = %d %q", code, out)
	}
	code, out, _ = run("ensemble", "presets", "--verbose")
	if code != 0 || !strings.Contains(out, "modes: systems-thinking, worst-case") {
		t.Fatalf("presets --verbose = %d %q", code, out)
	}
	code, out, _ = run("ensemble", "presets", "--format", "json")
	var views []PresetView
	if err := json.Unmarshal([]byte(out), &views); code != 0 || err != nil || len(views) != 9 {
		t.Fatalf("presets --format json = %d %v %d", code, err, len(views))
	}
	for _, v := range views {
		if len(v.ModeIDs) != len(v.Modes) || len(v.ModeIDs) == 0 {
			t.Fatalf("%s: %d mode ids for %d modes", v.Name, len(v.ModeIDs), len(v.Modes))
		}
	}
	code, out, _ = run("ensemble", "presets", "--tag", "health", "--format", "json")
	if err := json.Unmarshal([]byte(out), &views); code != 0 || err != nil || len(views) != 1 || views[0].Name != "project-diagnosis" {
		t.Fatalf("presets --tag health = %d %v %+v", code, err, views)
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
	code, out, errOut := run("ensemble", "list", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "0 run(s)") {
		t.Fatalf("list = %d %q %q", code, out, errOut)
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
	code, out, _ = run("ensemble", "list", "--socket", s.SocketPath, "--json")
	var env struct {
		Result []struct{ Status, Preset string }
	}
	if err := json.Unmarshal([]byte(out), &env); code != 0 || err != nil || len(env.Result) != 1 || env.Result[0].Status != "failed" ||
		env.Result[0].Preset != "project-diagnosis" {
		t.Fatalf("list after the refused run = %d %v %+v", code, err, env.Result)
	}
	if code, _, _ := run("ensemble", "nope", "q", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("an unknown preset = %d", code)
	}
	if code, _, _ := run("ensemble", "status", "x", "--partial", "--socket", s.SocketPath); code != 2 {
		t.Fatalf("--partial outside synthesize = %d", code)
	}
}
