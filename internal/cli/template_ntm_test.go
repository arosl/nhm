package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// templateProject is a git-free project in the cwd with nhm's config and
// state in temporary folders, so only the builtins and this project's
// files are templates.
func templateProject(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

// ntm's table of its 13 builtins, as ntm 9d5ed977 prints it: a
// description over 40 bytes is cut to 37 and "...".
const ntmBuiltinTable = `NAME             DESCRIPTION                               SOURCE
----             -----------                               ------
code_review      Review code for quality issues            builtin
explain          Explain how code works                    builtin
refactor         Refactor code for better quality          builtin
test             Write tests for code                      builtin
document         Add documentation to code                 builtin
fix              Fix a specific issue in code              builtin
implement        Implement a feature or function           builtin
optimize         Optimize code for performance             builtin
marching_orders  Initial instructions for an agent wor...  builtin
self_review      Agent reviews its own work before mar...  builtin
cross_review     One agent reviews another agent's work    builtin
handoff          Transfer work context from one agent ...  builtin
batch_assign     Template for distributing multiple be...  builtin
`

func TestTemplateList(t *testing.T) {
	templateProject(t)
	for _, verb := range []string{"list", "ls"} {
		if code, out, errOut := run("template", verb); code != 0 || out != ntmBuiltinTable || errOut != "" {
			t.Fatalf("template %s = %d\n%s%s", verb, code, out, errOut)
		}
	}
}

// The project's .nhm/templates come first, then the builtins; -a adds
// each template's variables.
func TestTemplateListAll(t *testing.T) {
	dir := templateProject(t)
	if err := os.MkdirAll(filepath.Join(dir, ".nhm", "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl := "---\nname: ship\ndescription: Ship it\nvariables:\n  - name: target\n    description: Where to\n    required: true\n  - name: note\n---\nShip {{target}}.\n"
	if err := os.WriteFile(filepath.Join(dir, ".nhm", "templates", "ship.md"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("template", "list", "-a")
	if code != 0 || !strings.HasPrefix(out, "NAME             DESCRIPTION                               SOURCE\n") ||
		!strings.Contains(out, "\nship             Ship it                                   project\n") {
		t.Fatalf("list -a = %d\n%s", code, out)
	}
	if !strings.Contains(out, "\nTemplate 'ship' variables:\n  - target (required) - Where to\n  - note\n\n") ||
		!strings.Contains(out, "Template 'code_review' variables:\n  - file (required) - File content to review\n") {
		t.Fatalf("list -a variables:\n%s", out)
	}
}

// --json is ntm's bare array of TemplateListItem, one line; the
// variables only with -a.
func TestTemplateListJSON(t *testing.T) {
	templateProject(t)
	code, out, _ := run("template", "list", "--json")
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); code != 0 || err != nil || len(items) != 13 || strings.Count(out, "\n") != 1 {
		t.Fatalf("list --json = %d %v %q", code, err, out)
	}
	first := items[0]
	if first["name"] != "code_review" || first["source"] != "builtin" || first["description"] != "Review code for quality issues" ||
		first["variables"] != nil || first["source_path"] != nil {
		t.Fatalf("first item = %v", first)
	}
	_, out, _ = run("--json", "template", "list", "-a")
	if err := json.Unmarshal([]byte(out), &items); err != nil || items[0]["variables"] == nil {
		t.Fatalf("list -a --json = %v %q", err, out)
	}
}

// show prints ntm's card; -b adds the body between rules.
func TestTemplateShow(t *testing.T) {
	templateProject(t)
	want := `Name:        code_review
Description: Review code for quality issues
Source:      builtin
Tags:        review, quality

Variables:
  - file (required)
    File content to review
  - focus
    Specific area to focus on
`
	if code, out, errOut := run("template", "show", "code_review"); code != 0 || out != want || errOut != "" {
		t.Fatalf("show = %d\n%s%s", code, out, errOut)
	}
	code, out, _ := run("template", "show", "code_review", "-b")
	rule := "─────────────────────────────────────────"
	if code != 0 || !strings.HasPrefix(out, want+"\nBody:\n"+rule+"\n") || !strings.HasSuffix(out, "\n"+rule+"\n") {
		t.Fatalf("show -b = %d\n%s", code, out)
	}
}

func TestTemplateShowJSON(t *testing.T) {
	templateProject(t)
	code, out, _ := run("template", "show", "code_review", "--json")
	var o TemplateShowOutput
	if err := json.Unmarshal([]byte(out), &o); code != 0 || err != nil || o.Name != "code_review" || o.Source != "builtin" ||
		len(o.Variables) != 2 || !o.Variables[0].Required || o.Body != "" {
		t.Fatalf("show --json = %d %v %q", code, err, out)
	}
	_, out, _ = run("template", "show", "code_review", "--body", "--json")
	if err := json.Unmarshal([]byte(out), &o); err != nil || !strings.Contains(o.Body, "{{file}}") {
		t.Fatalf("show --body --json = %v %q", err, out)
	}
}

// An unknown name is the loader's error, and show takes one name: exit 1,
// as ntm's.
func TestTemplateUnknownName(t *testing.T) {
	templateProject(t)
	for args, want := range map[string]string{
		"show nosuch": "Error: template not found: nosuch\n",
		"show":        "Error: accepts 1 arg(s), received 0\n",
		"show a b":    "Error: accepts 1 arg(s), received 2\n",
	} {
		if code, out, errOut := run(append([]string{"template"}, strings.Fields(args)...)...); code != exitError || out != "" || errOut != want {
			t.Errorf("template %s = %d %q %q, want %q", args, code, out, errOut, want)
		}
	}
}

// Bare template prints its help, exit 0, as ntm's; the sub-verbs get
// cobra's help with ntm's texts.
func TestTemplateBarePrintsHelp(t *testing.T) {
	templateProject(t)
	for _, args := range [][]string{{"template"}, {"template", "--help"}, {"template", "nosuch"}} {
		if code, out, errOut := run(args...); code != 0 || out != templateUsage || errOut != "" {
			t.Fatalf("%v = %d %q %q", args, code, out, errOut)
		}
	}
	code, out, _ := run("template", "show", "--help")
	if code != 0 || !strings.HasPrefix(out, "Show detailed information about a specific template.") || !strings.Contains(out, "-b, --body") {
		t.Fatalf("template show --help = %d\n%s", code, out)
	}
}
