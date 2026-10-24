package templates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// List gives what Load finds, in Load's order: the project's files, the
// user's, the catalogs' prompts, then ntm's builtins; the first of a name
// wins.
func TestLoaderList(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	l := NewLoader(project)
	all, err := l.List()
	if err != nil || len(all) != len(ListBuiltins()) || all[0].Source != SourceBuiltin {
		t.Fatalf("no files: %d templates, %v", len(all), err)
	}
	write(t, filepath.Join(home, AcfsCatalog), catalog)
	write(t, filepath.Join(home, ".config", "nhm", "templates", "hello.md"), "user file")
	write(t, filepath.Join(home, ".config", "nhm", "templates", "notes.txt"), "not a template")
	write(t, filepath.Join(project, ".nhm", "templates", "b_proj.md"), "---\nname: b_proj\ndescription: Project B\n---\nB")
	write(t, filepath.Join(project, ".nhm", "templates", "a_proj.md"), "A")
	all, err = l.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tm := range all[:6] {
		got = append(got, tm.Name+":"+tm.Source.String())
	}
	if want := "[a_proj:project b_proj:project hello:user fresh_review:user code_review:user explain:builtin]"; fmt.Sprint(got) != want {
		t.Fatalf("list = %v, want %s", got, want)
	}
	if all[1].Description != "Project B" || all[2].Body != "user file" || all[3].SourcePath != filepath.Join(home, AcfsCatalog) ||
		all[3].Description != "Fresh Review" {
		t.Fatalf("fields: %+v %+v %+v", all[1], all[2], all[3])
	}
	// code_review is a builtin too, and hello is in the catalog: each is
	// listed once.
	n := 0
	for _, tm := range all {
		if tm.Name == "code_review" || tm.Name == "hello" {
			n++
		}
	}
	if n != 2 || len(all) != len(ListBuiltins())+4 {
		t.Fatalf("code_review listed %d times; %d templates", n, len(all))
	}
	write(t, filepath.Join(project, ".nhm", "templates", "bad.md"), "---\nname: [unclosed\n---\nx")
	if _, err := l.List(); err == nil || !strings.Contains(err.Error(), "parsing template") {
		t.Fatalf("a bad file: %v", err)
	}
}
