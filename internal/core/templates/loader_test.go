package templates

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const catalog = `# Command Palette
#
# ### not_a_key | in the comments
## Analysis & Review

### fresh_review | Fresh Review
Read your code with fresh eyes.

### hello | Hello
Hello {{agent}} in {{session}}.
## Other
### code_review | Project's own review
Project review.
`

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "command_palette.md")
	write(t, path, catalog)
	entries, err := ReadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Key != "fresh_review" || entries[0].Body != "Read your code with fresh eyes." ||
		entries[1].Category != "Analysis & Review" || entries[2].Category != "Other" || entries[2].Label != "Project's own review" {
		t.Fatalf("entries = %+v", entries)
	}
	write(t, path, "## A\n### ../x | bad\nbody\n")
	if _, err := ReadCatalog(path); err == nil {
		t.Fatal("a key with a path was accepted")
	}
}

func TestLoaderOrder(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	write(t, filepath.Join(home, AcfsCatalog), catalog)
	l := NewLoader(project)

	// ACFS's catalog answers a palette key.
	tm, err := l.Load("hello")
	if err != nil || tm.Body != "Hello {{agent}} in {{session}}." {
		t.Fatalf("hello = %+v %v", tm, err)
	}
	out, err := tm.Execute(ExecutionContext{Session: "w1", AgentName: "IcyKnoll"})
	if err != nil || out != "Hello IcyKnoll in w1." {
		t.Fatalf("render = %q %v", out, err)
	}
	// A missing placeholder refuses.
	var unresolved *UnresolvedVariablesError
	if _, err := tm.Execute(ExecutionContext{Session: "w1"}); !errors.As(err, &unresolved) || unresolved.Names[0] != "agent" {
		t.Fatalf("missing agent = %v", err)
	}
	// The catalog wins over ntm's builtin of the same key.
	if tm, _ = l.Load("code_review"); tm.Body != "Project review." {
		t.Fatalf("code_review = %q", tm.Body)
	}
	// The user's catalog wins over ACFS's, the project's over the user's,
	// and a template file over every catalog.
	write(t, filepath.Join(home, ".config", "nhm", "command_palette.md"), "## U\n### hello | Hi\nuser hello\n")
	if tm, _ = l.Load("hello"); tm.Body != "user hello" {
		t.Fatalf("user catalog = %q", tm.Body)
	}
	write(t, filepath.Join(project, "command_palette.md"), "## P\n### hello | Hi\nproject hello\n")
	if tm, _ = l.Load("hello"); tm.Body != "project hello" {
		t.Fatalf("project catalog = %q", tm.Body)
	}
	write(t, filepath.Join(home, ".config", "nhm", "templates", "hello.md"), "user file")
	if tm, _ = l.Load("hello"); tm.Body != "user file" || tm.Source != SourceUser {
		t.Fatalf("user file = %q", tm.Body)
	}
	write(t, filepath.Join(project, ".nhm", "templates", "hello.md"), "---\nname: hello\n---\nproject file")
	if tm, _ = l.Load("hello"); tm.Body != "project file" || tm.Source != SourceProject {
		t.Fatalf("project file = %q", tm.Body)
	}
	// ntm's builtins come last.
	if tm, err = l.Load("marching_orders"); err != nil || tm.Source != SourceBuiltin {
		t.Fatalf("builtin = %v", err)
	}
	// A path to a file is that file.
	file := filepath.Join(project, "x", "mine.md")
	write(t, file, "mine")
	if tm, err = l.Load(file); err != nil || tm.Body != "mine" || tm.Name != "mine" {
		t.Fatalf("file = %+v %v", tm, err)
	}
	var nf *TemplateNotFoundError
	if _, err := l.Load("nothing"); !errors.As(err, &nf) {
		t.Fatalf("unknown = %v", err)
	}
	if _, err := l.Load("../escape"); err == nil {
		t.Fatal("a key with a path was accepted")
	}
}

// A relative XDG_CONFIG_HOME is ignored, as the XDG spec says: the user's
// templates stay in ~/.config, not in each working directory (nhm-mwo).
func TestLoaderIgnoresARelativeConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "rel-config")
	if got, want := NewLoader("").UserDir, filepath.Join(home, ".config", "nhm", "templates"); got != want {
		t.Fatalf("relative: UserDir = %q, want %q", got, want)
	}
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	if got, want := NewLoader("").UserDir, filepath.Join(config, "nhm", "templates"); got != want {
		t.Fatalf("absolute: UserDir = %q, want %q", got, want)
	}
}
