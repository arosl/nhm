package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
		{"admin", "takeover"}, {"admin", "handback"}}
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
	// A help flag after "--" is an argument, and help with other flags is
	// still help.
	if _, ok := leafHelp([]string{"send", "swarm", "--", "--help"}); ok {
		t.Error("--help after -- asked for help")
	}
	if u, ok := leafHelp([]string{"spawn", "s", "--cc=2", "--help", "--json"}); !ok || u != spawnUsage {
		t.Error("spawn --cc=2 --help --json did not ask for help")
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
