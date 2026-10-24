package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every leaf's -h and --help print the leaf's usage on stdout, exit 0
// and run nothing (nhm-4cb: quota record-claude --help read stdin and
// saved it). The verbs come from nhm help, so a new verb needs its usage
// in verbUsage; the sub-verbs are listed here.
func TestEveryLeafHelpPrintsUsageAndRunsNothing(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "ACFS_HOME", "CODEX_HOME"} {
		t.Setenv(v, filepath.Join(root, strings.ToLower(v)))
	}
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "herdr.sock"))
	t.Setenv("HERDR_PANE_ID", "")
	// What Claude's statusLine hands record-claude: a leaf that ran would
	// save its windows under XDG_STATE_HOME.
	payload := filepath.Join(t.TempDir(), "statusline.json")
	if err := os.WriteFile(payload, []byte(`{"rate_limits":{"five_hour":{"used_percentage":42}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(payload)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = old; _ = stdin.Close() })

	leaves := [][]string{{"board"}, {"menu"}, {"add"},
		{"quota", "check"}, {"quota", "record-claude"}, {"mail", "inbox"}, {"compatibility", "status"},
		{"feedback", "list"}, {"feedback", "mark"}, {"feedback", "outcomes"}, {"modes", "list"}, {"modes", "explain"},
		{"ensemble", "presets"}, {"ensemble", "spawn"}, {"ensemble", "status"}, {"ensemble", "synthesize"},
		{"ensemble", "stop"}, {"ensemble", "runs"}, {"ui", "palette"}, {"ui", "board-popup"},
		{"admin", "instance", "enroll"}, {"admin", "recover", "reconcile"}, {"admin", "recover", "inspect"},
		{"admin", "recover", "resume"}, {"admin", "restore"}, {"admin", "export"}, {"admin", "import"},
		{"admin", "takeover"}}
	_, help, _ := run("help")
	for _, m := range regexp.MustCompile(`(?m)^  nhm ([a-z]+)`).FindAllStringSubmatch(help, -1) {
		if m[1] != "help" {
			leaves = append(leaves, []string{m[1]})
		}
	}
	for _, leaf := range leaves {
		for _, flag := range []string{"--help", "-h"} {
			args := append(append([]string{}, leaf...), flag)
			code, out, errOut := run(args...)
			if code != 0 || errOut != "" || !strings.HasPrefix(out, "Usage: nhm ") {
				t.Errorf("nhm %s = %d, stdout %q, stderr %q", strings.Join(args, " "), code, out, errOut)
				continue
			}
			for _, word := range leaf {
				if !strings.Contains(out, word) {
					t.Errorf("nhm %s: the usage does not name %q:\n%s", strings.Join(args, " "), word, out)
				}
			}
		}
	}
	var made []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != root {
			made = append(made, p)
		}
		return nil
	})
	if len(made) > 0 {
		t.Errorf("a help request wrote %v", made)
	}
}

// A help flag counts only in flag position (nhm-ceu): never after "--",
// never as the value of the flag before it.
func TestHelpFlagPosition(t *testing.T) {
	for _, c := range []struct {
		args []string
		help bool
	}{
		{[]string{"quota", "record-claude", "--help"}, true},
		{[]string{"send", "swarm", "hello", "-h"}, true},
		{[]string{"spawn", "s", "--cc=2", "--help", "--json"}, true},
		{[]string{"send", "swarm", "--json", "--help"}, true},
		{[]string{"send", "swarm", "--dry-run", "-h"}, true},
		{[]string{"send", "swarm", "--", "--help"}, false},
		{[]string{"send", "swarm", "--msg", "--help"}, false},
		{[]string{"send", "swarm", "--prompt", "-h", "x"}, false},
		{[]string{"modes", "list", "-f", "--help"}, false},
	} {
		if _, got := leafHelp(c.args); got != c.help {
			t.Errorf("leafHelp(%q) = %v, want %v", c.args, got, c.help)
		}
	}
	// record-claude takes no argument: the verb refuses what Run left to
	// it, and never reads stdin.
	if code, out, _ := run("quota", "record-claude", "--x", "--help"); code != exitRefused || out != "" {
		t.Errorf("record-claude --x --help = %d %q", code, out)
	}
}

// Every name in boolFlags is a boolean flag wherever a verb defines it:
// a flag that takes a value would make its value "--help" a help request.
func TestHelpBoolFlagsTakeNoValue(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	valued, boolean := map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			for _, a := range call.Args {
				lit, ok := a.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				name, _ := strconv.Unquote(lit.Value)
				switch sel.Sel.Name {
				case "Bool", "BoolVar":
					boolean[name] = true
				case "String", "StringVar", "Int", "IntVar", "Int64", "Int64Var", "Uint", "UintVar", "Float64",
					"Float64Var", "Duration", "DurationVar", "Var", "Func", "TextVar":
					valued[name] = true
				}
				break
			}
			return true
		})
	}
	for name := range boolFlags {
		if valued[name] || !boolean[name] {
			t.Errorf("boolFlags has %q: a value flag %v, a bool flag %v", name, valued[name], boolean[name])
		}
	}
}

// Every robot flag that --robot-capabilities reports as available is in
// nhm help, so the two cannot drift (acfs-9hkh.57).
func TestHelpListsEveryAvailableRobotFlag(t *testing.T) {
	code, out, _ := run("help")
	if code != 0 {
		t.Fatalf("help = %d", code)
	}
	// Whole flags only: --robot-send must not pass on --robot-send-receipt
	// (acfs-9hkh.69).
	listed := map[string]bool{}
	for _, f := range regexp.MustCompile(`--robot-[a-z0-9-]*[a-z0-9]`).FindAllString(out, -1) {
		listed[f] = true
	}
	caps := robotCapabilities("json", time.Now())
	available := 0
	for _, c := range caps.Commands {
		if c.Unavailable {
			continue
		}
		available++
		if !listed[c.Flag] {
			t.Errorf("nhm help does not list %s, which --robot-capabilities offers", c.Flag)
		}
	}
	if available == 0 {
		t.Fatal("--robot-capabilities lists no available robot flag")
	}
}
