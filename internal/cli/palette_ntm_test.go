package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/ui"
)

// paletteProject is a git project in the cwd with a palette file, a
// user palette, and pins and favorites; no ACFS palette.
func paletteProject(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("ACFS_HOME", filepath.Join(home, ".acfs"))
	t.Setenv("NHM_PALETTE_FILE", "")
	t.Setenv("NHM_PALETTE_CWD", "")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	t.Setenv(ui.ContextEnv, "")
	state := filepath.Join(home, "state")
	t.Setenv("NHM_PALETTE_STATE_DIR", state)
	proj := t.TempDir()
	writeTestFile(t, filepath.Join(proj, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(proj, "command_palette.md"),
		"## Review\n### fresh_review | Fresh Review\nLook again.\n### deep_review | Deep Review\nLook deeper.\n## Git\n### git_commit | Git Commit\nCommit.\n")
	writeTestFile(t, filepath.Join(home, ".config", "nhm", "command_palette.md"), "## Mine\n### fresh_review | Shadowed\nNo.\n### mine | Mine\nMy prompt.\n")
	writeTestFile(t, filepath.Join(state, "favorites"), "git_commit\ngone\nfresh_review\n")
	writeTestFile(t, filepath.Join(state, "pins"), "mine\n")
	t.Chdir(proj)
	return proj
}

func decodePalette(t *testing.T, out string) robot.PaletteOutput {
	t.Helper()
	var p robot.PaletteOutput
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("not PaletteOutput: %v\n%s", err, out)
	}
	return p
}

// palette --json is ntm's PaletteOutput over nhm's palette files and its
// pins and favorites: first key wins, the state's order, the categories
// in first-seen order, no recents.
func TestPaletteJSONIsPaletteOutput(t *testing.T) {
	paletteProject(t)
	code, out, errOut := run("palette", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("palette --json = %d %q", code, errOut)
	}
	p := decodePalette(t, out)
	var keys []string
	for _, c := range p.Commands {
		keys = append(keys, c.Key)
	}
	if !p.Success || fmt.Sprint(keys) != "[fresh_review deep_review git_commit mine]" ||
		fmt.Sprint(p.Favorites) != "[git_commit fresh_review]" || fmt.Sprint(p.Pinned) != "[mine]" ||
		fmt.Sprint(p.Categories) != "[Review Git Mine]" || p.Recent == nil || len(p.Recent) != 0 {
		t.Fatalf("palette --json = %+v", p)
	}
	if c := p.Commands[0]; c.Label != "Fresh Review" || c.Prompt != "Look again." || !c.IsFavorite || c.IsPinned {
		t.Fatalf("fresh_review = %+v", c)
	}
	if p.AgentHints == nil || p.AgentHints.Summary != "4 commands available across 3 categories" {
		t.Fatalf("hints = %+v", p.AgentHints)
	}
	// --list --json stays nhm's array.
	code, out, _ = run("palette", "--list", "--json")
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); code != 0 || err != nil || len(rows) != 4 || rows[0]["file"] == "" {
		t.Fatalf("--list --json = %d %v %q", code, err, out)
	}
	// Under --json, the error is the robot error envelope on stdout.
	if code, out, errOut := run("palette", "--json", "--show", "mine"); code != exitError || errOut != "" ||
		!strings.Contains(out, "it does not go with --show") || !strings.Contains(out, `"success":false`) {
		t.Fatalf("--json --show = %d %q %q", code, out, errOut)
	}
}

// The robot form filters by --category and --search (ntm's deprecated
// --palette-* names too).
func TestRobotPalette(t *testing.T) {
	paletteProject(t)
	code, out, _ := run("--robot-palette")
	if p := decodePalette(t, out); code != 0 || len(p.Commands) != 4 {
		t.Fatalf("--robot-palette = %d %+v", code, p)
	}
	for _, args := range [][]string{{"--robot-palette", "--category=Review"}, {"--robot-palette", "--palette-category", "Review"}} {
		code, out, _ := run(args...)
		if p := decodePalette(t, out); code != 0 || len(p.Commands) != 2 || fmt.Sprint(p.Categories) != "[Review]" || fmt.Sprint(p.Favorites) != "[fresh_review]" {
			t.Fatalf("%v = %d %+v", args, code, p)
		}
	}
	code, out, _ = run("--robot-palette", "--search=COMMIT")
	if p := decodePalette(t, out); code != 0 || len(p.Commands) != 1 || p.Commands[0].Key != "git_commit" {
		t.Fatalf("--search = %d %+v", code, p)
	}
	if code, out, _ := run("--robot-palette", "--robot-format=xml"); code != robotError || !strings.Contains(out, "INVALID_FLAG") {
		t.Fatalf("xml = %d %q", code, out)
	}
}

// A session names the palette's workspace; one that herdr does not have
// is ntm's error, exit 1. Under --json a session is the filter of its
// recents; an invalid name is SESSION_NOT_FOUND for the robot form.
func TestPaletteTakesASession(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	paletteProject(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	if code, out, errOut := run("palette", "nosuch"); code != exitError || out != "" || errOut != "Error: session 'nosuch' not found\n" {
		t.Fatalf("palette nosuch = %d %q %q", code, out, errOut)
	}
	if code, _, errOut := run("palette", "swarm", "--workspace", "w1"); code != exitError || errOut != "Error: give a session or --workspace, not both\n" {
		t.Fatalf("session and --workspace = %d %q", code, errOut)
	}
	code, out, _ := run("palette", "swarm", "--json")
	if p := decodePalette(t, out); code != 0 || p.Session != "swarm" {
		t.Fatalf("palette swarm --json = %d %+v", code, p)
	}
	code, out, _ = run("--robot-palette", "--session=a:b")
	if code != robotError || !strings.Contains(out, "SESSION_NOT_FOUND") || !strings.Contains(out, "invalid session name") {
		t.Fatalf("bad session = %d %q", code, out)
	}
	// nhm's palette errors keep their text, with ntm's exit 1.
	if code, _, errOut := run("palette", "--file", filepath.Join(t.TempDir(), "nosuch.md"), "--list"); code != exitError ||
		!strings.Contains(errOut, "no such readable file") {
		t.Fatalf("--file nosuch = %d %q", code, errOut)
	}
}
