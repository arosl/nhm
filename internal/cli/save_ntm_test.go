package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// saveServer is paneServer with herdr's process facts: the shell runs
// zsh and IcyKnoll's pane claude; the other panes have none, so their
// command is the agent's kind.
func saveServer(t *testing.T) *herdrtest.Server {
	t.Helper()
	s, _, _ := paneServer(t, copyText)
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		info := map[string]any{"pane_id": p.PaneID}
		switch p.PaneID {
		case "w1:p3":
			info["foreground_processes"] = []any{map[string]any{"pid": 10, "name": "zsh"}}
		case "w1:p1":
			info["foreground_processes"] = []any{map[string]any{"pid": 11, "name": "claude"}}
		}
		return map[string]any{"type": "pane_process_info", "process_info": info}, nil
	})
	return s
}

// save writes one file per pane (shells too, without a type flag), named
// <session>_<title>_<UTC time>.txt, mode 0644, under ntm's header, and
// prints ntm's lines.
func TestSaveWritesOneFilePerPane(t *testing.T) {
	s := saveServer(t)
	dir := filepath.Join(t.TempDir(), "out")
	code, out, errOut := run("save", "swarm", "-o", dir, "--socket", s.SocketPath)
	if code != 0 || errOut != "" {
		t.Fatalf("save = %d %q %q", code, out, errOut)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	re := regexp.MustCompile(`^swarm_(.+)_\d{8}_\d{6}\.txt$`)
	var titles []string
	for _, n := range names {
		m := re.FindStringSubmatch(n)
		if m == nil {
			t.Fatalf("file name %q is not ntm's", n)
		}
		titles = append(titles, m[1])
	}
	if strings.Join(titles, ",") != "BlueLake_gpt,IcyKnoll_opus,loose,user@host--p" {
		t.Errorf("titles = %v", titles)
	}
	if !strings.HasPrefix(out, "  ✓ "+dir+"/swarm_user@host--p_") || !strings.HasSuffix(out, "\n✓ Saved 4 file(s) to "+dir+"\n") ||
		strings.Count(out, "  ✓ ") != 4 {
		t.Errorf("out = %q", out)
	}
	var icy string
	for _, n := range names {
		if strings.Contains(n, "IcyKnoll") {
			icy = filepath.Join(dir, n)
		}
	}
	data, err := os.ReadFile(icy)
	if err != nil {
		t.Fatal(err)
	}
	header := regexp.MustCompile("^" + saveSeparator + "Session: swarm\nPane: IcyKnoll opus \\(index 1\\)\nCommand: claude\n" +
		"Captured: \\d{4}-\\d\\d-\\d\\dT\\d\\d:\\d\\d:\\d\\dZ\nLines: 2000\n" + saveSeparator + "\nicy one\n")
	if !header.Match(data) {
		t.Errorf("file = %q", data)
	}
	if st, _ := os.Stat(icy); st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	for _, n := range names {
		data, _ := os.ReadFile(filepath.Join(dir, n))
		switch {
		case strings.Contains(n, "BlueLake") && !strings.Contains(string(data), "Command: codex\n"):
			t.Errorf("BlueLake's command: %q", data)
		case strings.Contains(n, "user@host") && !strings.Contains(string(data), "Pane: user@host:/p (index w1:p3)\nCommand: zsh\n"):
			t.Errorf("the shell's header: %q", data)
		}
	}
}

// The type flags keep their panes; dump and export are save; without
// --output the files go to nhm's .nhm/outputs.
func TestSaveTypeFilters(t *testing.T) {
	s := saveServer(t)
	dir := t.TempDir()
	count := func(args ...string) int {
		t.Helper()
		out := filepath.Join(t.TempDir(), "o")
		code, stdout, stderr := run(append(append(args, "-o", out), "--socket", s.SocketPath)...)
		if code != 0 {
			t.Fatalf("%v = %d %q %q", args, code, stdout, stderr)
		}
		entries, _ := os.ReadDir(out)
		return len(entries)
	}
	if n := count("save", "swarm", "--cc"); n != 2 {
		t.Errorf("--cc saved %d", n)
	}
	if n := count("dump", "swarm", "--cod"); n != 1 {
		t.Errorf("dump --cod saved %d", n)
	}
	if n := count("export", "swarm", "--all"); n != 4 {
		t.Errorf("export --all saved %d", n)
	}
	t.Chdir(dir)
	if code, out, _ := run("save", "swarm", "--cod", "--socket", s.SocketPath); code != 0 || !strings.HasSuffix(out, "to .nhm/outputs\n") {
		t.Errorf("default folder = %d %q", code, out)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".nhm", "outputs")); len(entries) != 1 {
		t.Errorf("default folder holds %d", len(entries))
	}
}

// ntm's errors, exit 1; --json prints the same text, as ntm's save has no
// JSON.
func TestSaveRefusals(t *testing.T) {
	s := saveServer(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "--gmi"}, "Error: no matching panes found\n"},
		{[]string{"nosuch"}, "Error: session \"nosuch\" not found (available: swarm)\n"},
		{[]string{"swarm", "-o", filepath.Join(blocker, "sub")}, "Error: failed to create output directory: "},
	} {
		code, out, errOut := run(append(append([]string{"save"}, c.args...), "--socket", s.SocketPath)...)
		if code != 1 || out != "" || !strings.HasPrefix(errOut, c.want) {
			t.Errorf("save %v = %d %q %q", c.args, code, out, errOut)
		}
	}
	out := filepath.Join(t.TempDir(), "o")
	code, stdout, _ := run("save", "swarm", "--cod", "-o", out, "--json", "--socket", s.SocketPath)
	if code != 0 || !strings.HasPrefix(stdout, "  ✓ "+out+"/swarm_BlueLake_gpt_") {
		t.Errorf("--json = %d %q", code, stdout)
	}
}
