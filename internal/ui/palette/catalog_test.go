package palette

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestCatalogPathsFollowTheFzfPalette(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, "sub")
	cfg := filepath.Join(home, "cfg")
	for _, p := range []string{
		filepath.Join(dir, CatalogName),
		filepath.Join(root, CatalogName),
		filepath.Join(cfg, "nhm", CatalogName),
		filepath.Join(home, ".acfs", "onboard", "docs", "ntm", CatalogName),
	} {
		writeFile(t, p, "## C\n### k | K\nk\n")
	}
	env := envOf(map[string]string{"HOME": home, "XDG_CONFIG_HOME": cfg})
	got := CatalogPaths(env, dir, root, "")
	want := []string{filepath.Join(dir, CatalogName), filepath.Join(root, CatalogName),
		filepath.Join(cfg, "nhm", CatalogName), filepath.Join(home, ".acfs", "onboard", "docs", "ntm", CatalogName)}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paths:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The project root is the directory itself: one file, once. A link to
	// a listed file is the same file.
	if err := os.Symlink(filepath.Join(root, CatalogName), filepath.Join(cfg, "nhm", "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(cfg, "nhm", "link.md"), filepath.Join(cfg, "nhm", CatalogName)); err != nil {
		t.Fatal(err)
	}
	got = CatalogPaths(env, root, root, "")
	if len(got) != 2 || got[0] != filepath.Join(root, CatalogName) || !strings.Contains(got[1], ".acfs") {
		t.Fatalf("deduplicated paths = %v", got)
	}

	// ACFS_HOME moves ACFS's catalog; a missing file is left out.
	got = CatalogPaths(envOf(map[string]string{"HOME": home, "ACFS_HOME": t.TempDir()}), "", "", "")
	if len(got) != 0 {
		t.Fatalf("no catalogs expected, got %v", got)
	}
}

func TestCatalogPathsOneFile(t *testing.T) {
	env := envOf(map[string]string{"HOME": t.TempDir(), "NHM_PALETTE_FILE": "/env/palette.md"})
	if got := CatalogPaths(env, "", "", "/flag/palette.md"); len(got) != 1 || got[0] != "/flag/palette.md" {
		t.Fatalf("--file: %v", got)
	}
	if got := CatalogPaths(env, "", "", ""); len(got) != 1 || got[0] != "/env/palette.md" {
		t.Fatalf("NHM_PALETTE_FILE: %v", got)
	}
}

func TestLoadCommandsFirstKeyWins(t *testing.T) {
	d := t.TempDir()
	first, second, broken := filepath.Join(d, "a.md"), filepath.Join(d, "b.md"), filepath.Join(d, "c.md")
	writeFile(t, first, "intro\n## Review\n### review | Review the diff\nRead the diff.\n\nThen report.\n### bare\nNo label.\n")
	writeFile(t, second, "## Other\n### review | Shadowed\nnever\n### fix | Fix it\nFix {{thread}}.\n")
	writeFile(t, broken, "## X\n### bad key | Bad\nx\n")
	cmds, errs := LoadCommands([]string{first, filepath.Join(d, "missing.md"), broken, second})
	if len(errs) != 2 {
		t.Fatalf("errors = %v", errs)
	}
	if len(cmds) != 3 {
		t.Fatalf("commands = %+v", cmds)
	}
	r := cmds[0]
	if r.Key != "review" || r.Label != "Review the diff" || r.Category != "Review" || r.Source != first ||
		r.Prompt != "Read the diff.\n\nThen report." {
		t.Fatalf("review = %+v", r)
	}
	if cmds[1].Key != "bare" || cmds[1].Label != "bare" {
		t.Fatalf("a command with no label shows its key: %+v", cmds[1])
	}
	if cmds[2].Key != "fix" || cmds[2].Source != second || cmds[2].Category != "Other" {
		t.Fatalf("fix = %+v", cmds[2])
	}
}

// A catalog's terminal controls are neither shown nor sent.
func TestLoadCommandsCleansTerminalControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.md")
	writeFile(t, path, "## Cat\x1b[31m\n### k | La\x1b]0;x\x07bel\nLine one\x1b[2J\n\tLine two\x07\n")
	cmds, errs := LoadCommands([]string{path})
	if len(errs) != 0 || len(cmds) != 1 {
		t.Fatalf("= %+v %v", cmds, errs)
	}
	if c := cmds[0]; c.Label != "Label" || c.Category != "Cat" || c.Prompt != "Line one\n\tLine two" {
		t.Fatalf("command = %q %q %q", c.Label, c.Category, c.Prompt)
	}
}
