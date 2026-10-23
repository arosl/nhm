package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// depsPath makes a PATH of fake tools, each printing its version line,
// with no Agent Mail and no HERDR_BIN_PATH.
func depsPath(t *testing.T, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, version := range tools {
		script := "#!/bin/sh\necho '" + version + "'\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("HERDR_BIN_PATH", "")
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	return dir
}

// nhm deps is ntm's deps with herdr for tmux: the required tool, the
// agent CLIs of nhm's kinds, fzf and git, Agent Mail and the flywheel
// tools, in ntm's report (the operator's ruling, nhm-k9d, 2026-10-23).
func TestDepsReportsAsNTM(t *testing.T) {
	dir := depsPath(t, map[string]string{"herdr": "herdr 0.9.3", "claude": "2.1.0 (Claude Code)", "git": "git version 2.43.0", "br": "br 0.9.1"})
	for _, verb := range []string{"deps", "check"} {
		code, out, errOut := run(verb)
		if code != 0 || errOut != "" {
			t.Fatalf("%s = %d %q %q", verb, code, out, errOut)
		}
		for _, want := range []string{
			"\n nhm Dependency Check\n═══",
			"Required:\n\n  ✓ herdr\n\nAI Agents:\n\n  ✓ Claude Code\n  ✗ OpenAI Codex\n  ✗ Antigravity CLI (agy)\n",
			"  ✗ Grok Build (xAI)\n\nRecommended:\n\n  ✗ fzf\n  ✓ git\n\nServices:\n\n  ○ Agent Mail\n\nFlywheel Tools:\n\n  ○ bv *\n  ✓ bd\n  ○ caam\n",
			"✓ All required dependencies installed. 1 agent(s), 1 flywheel tool(s) available.\n\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s lacks %q:\n%s", verb, want, out)
			}
		}
	}
	// -v adds the versions and the install hints of what is missing.
	code, out, _ := run("deps", "-v")
	for _, want := range []string{
		"  ✓ herdr           herdr 0.9.3\n",
		"  ✓ Claude Code     2.1.0 (Claude Code)\n  ✗ OpenAI Codex\n      Install: npm install -g @openai/codex\n",
		"  ○ Agent Mail      not detected (optional)\n",
		"  ✓ bd              br 0.9.1\n",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("deps -v = %d, lacks %q:\n%s", code, want, out)
		}
	}
	// --json: nhm's envelope with ntm's fields.
	code, out, _ = run("deps", "--json")
	res := envelopeOf(t, out)["result"].(map[string]any)
	deps := res["dependencies"].([]any)
	first := deps[0].(map[string]any)
	last := deps[len(deps)-1].(map[string]any)
	if code != 0 || res["all_installed"] != true || res["generated_at"] == nil || first["name"] != "herdr" || first["required"] != true ||
		first["installed"] != true || first["version"] != "herdr 0.9.3" || first["path"] != filepath.Join(dir, "herdr") ||
		last["name"] != "Agent Mail" || last["installed"] != false {
		t.Fatalf("deps --json = %d %s", code, out)
	}
}

// Without herdr, deps exits 1, as ntm's without tmux; without an agent
// it warns.
func TestDepsWithoutHerdrOrAgents(t *testing.T) {
	depsPath(t, map[string]string{"claude": "2.1.0"})
	code, out, _ := run("deps")
	if code != exitError || !strings.Contains(out, "  ✗ herdr\n") || !strings.Contains(out, "✗ Missing required dependencies!\n") {
		t.Fatalf("deps without herdr = %d\n%s", code, out)
	}
	code, out, _ = run("deps", "--json")
	env := decodeEnvelope(t, out)
	if code != exitError || env["error"] != "required dependencies are missing" || env["result"].(map[string]any)["all_installed"] != false {
		t.Fatalf("deps --json without herdr = %d %s", code, out)
	}
	depsPath(t, map[string]string{"herdr": "herdr 0.9.3"})
	if code, out, _ := run("deps"); code != 0 || !strings.Contains(out, "⚠ No AI agents installed. Install at least one to use nhm spawn.\n") {
		t.Fatalf("deps without agents = %d\n%s", code, out)
	}
	if code, _, errOut := run("deps", "extra"); code != exitRefused || !strings.Contains(errOut, "Usage: nhm deps") {
		t.Fatalf("deps extra = %d %q", code, errOut)
	}
}

// A version is one line made safe for a terminal, at most 240 runes.
func TestDepVersionIsOneSafeLine(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 300)
	script := "#!/bin/sh\nprintf 'tool \\033[1m1.2\\033[0m\\n  built\\ttoday\\n'\nexit 3\n"
	for name, body := range map[string]string{"tool": script, "long": "#!/bin/sh\necho " + long + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := depVersion(t.Context(), filepath.Join(dir, "tool"), nil); got != "tool 1.2 built today" {
		t.Errorf("version = %q", got)
	}
	if got := depVersion(t.Context(), filepath.Join(dir, "long"), nil); got != strings.Repeat("x", 240)+"..." {
		t.Errorf("long version = %q", got)
	}
	// The report cuts by runes: a box drawing does not split.
	if got := clip("0.15.4 "+strings.Repeat("─", 40), 10); got != "0.15.4 ───..." {
		t.Errorf("clip = %q", got)
	}
}

// nhm tutorial points to ACFS's lessons and exits 0; ntm's flags are
// taken (the operator's ruling, nhm-k9d, 2026-10-23).
func TestTutorialPointsToTheLessons(t *testing.T) {
	for _, args := range [][]string{{"tutorial"}, {"tutorial", "--skip-animations"}, {"tutorial", "--slide", "5"}} {
		code, out, errOut := run(args...)
		if code != 0 || errOut != "" || out != tutorialText || !strings.Contains(out, "onboard 5") {
			t.Errorf("%v = %d %q %q", args, code, out, errOut)
		}
	}
	if code, _, errOut := run("tutorial", "extra"); code != exitRefused || !strings.Contains(errOut, "Usage: nhm tutorial") {
		t.Errorf("tutorial extra = %d %q", code, errOut)
	}
}
