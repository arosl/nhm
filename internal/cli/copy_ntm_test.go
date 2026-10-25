package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// paneRead is one pane.read that a test server answered.
type paneRead struct {
	pane  string
	lines int
}

// paneServer is an enrolled instance with workspace w1, "swarm". Its tab
// w1:t1 holds IcyKnoll (claude, w1:p1) and BlueLake (codex, w1:p2), both
// bound (slots 0.1 and 0.2), and tab w1:t2 holds a shell (w1:p3) and an
// unbound claude (w1:p4). The active tab is w1:t1, and its focused pane is
// w1:p2. text answers each pane.read; reads returns the reads so far.
func paneServer(t *testing.T, text func(pane string) string) (s *herdrtest.Server, bs []bindings.Binding, reads func() []paneRead) {
	t.Helper()
	s, inst := cliInstance(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": paneSnapshot()}, nil
	})
	var mu sync.Mutex
	var got []paneRead
	s.Handle("pane.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Lines  int    `json:"lines"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		got = append(got, paneRead{p.PaneID, p.Lines})
		mu.Unlock()
		if p.PaneID == "w1:gone" {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane"}
		}
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3, "text": text(p.PaneID), "truncated": false}}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, n := range []struct{ name, pane, kind string }{{"IcyKnoll", "w1:p1", "claude"}, {"BlueLake", "w1:p2", "codex"}} {
		b, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: n.name, MailProjectKey: "/p", ProjectID: "proj",
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: n.pane,
				HerdrAlias: strings.ToLower(n.name), Kind: n.kind}})
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, b)
	}
	return s, bs, func() []paneRead {
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		return out
	}
}

// testPane is a herdr pane of the test snapshots.
func testPane(id, tab, agent, label, title string) map[string]any {
	ws, _, _ := strings.Cut(id, ":")
	p := map[string]any{"pane_id": id, "tab_id": tab, "workspace_id": ws, "terminal_id": "t-" + id,
		"agent_status": "idle", "focused": false, "revision": 1}
	if agent != "" {
		p["agent"] = agent
	}
	if label != "" {
		p["label"] = label
	}
	if title != "" {
		p["terminal_title_stripped"] = title
	}
	return p
}

// paneSnapshot is paneServer's herdr snapshot: workspace w1, "swarm".
func paneSnapshot() map[string]any {
	tab := func(id string, n int) map[string]any {
		return map[string]any{"tab_id": id, "workspace_id": "w1", "number": n, "label": "", "focused": n == 1,
			"pane_count": 2, "agent_status": "idle"}
	}
	return map[string]any{
		"protocol": 22, "version": "0.9.5", "agents": []any{},
		"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
			"pane_count": 4, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		"tabs": []any{tab("w1:t1", 1), tab("w1:t2", 2)},
		"panes": []any{testPane("w1:p1", "w1:t1", "claude", "IcyKnoll opus", ""), testPane("w1:p2", "w1:t1", "codex", "BlueLake gpt", ""),
			testPane("w1:p3", "w1:t2", "", "", "user@host:/p"), testPane("w1:p4", "w1:t2", "claude", "", "loose")},
		"layouts": []any{map[string]any{"workspace_id": "w1", "tab_id": "w1:t1", "zoomed": false, "focused_pane_id": "w1:p2"}},
	}
}

func copyText(pane string) string {
	switch pane {
	case "w1:p1":
		return "icy one\nERROR icy\n```go\nfmt.Println(1)\n```\n\n"
	case "w1:p2":
		return "blue one\nERROR blue\n"
	case "w1:p4":
		return "loose claude\n"
	}
	return ""
}

// copy session:pane --output writes the pane's text under ntm's header to
// the file, making its folder, mode 0644, and prints ntm's line.
func TestCopyToFile(t *testing.T) {
	s, bs, reads := paneServer(t, copyText)
	if bs[0].Slot != "0.1" || bs[1].Slot != "0.2" {
		t.Fatalf("slots %s, %s", bs[0].Slot, bs[1].Slot)
	}
	out := filepath.Join(t.TempDir(), "sub", "out.txt")
	code, stdout, stderr := run("copy", "swarm:1", "--output", out, "--socket", s.SocketPath)
	if code != 0 || stderr != "" || stdout != "✓ Copied 6 lines from 1 pane(s) to file ("+out+")\n" {
		t.Fatalf("copy = %d %q %q", code, stdout, stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "═══ IcyKnoll opus (pane 1) ═══\nicy one\nERROR icy\n```go\nfmt.Println(1)\n```\n\n\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	if r := reads(); len(r) != 1 || r[0] != (paneRead{"w1:p1", 1000}) {
		t.Errorf("reads = %v, want 1000 lines of w1:p1", r)
	}
	// -l sets the lines read; cp and yank are copy; --quiet prints nothing.
	for _, verb := range []string{"cp", "yank"} {
		code, stdout, stderr = run(verb, "swarm:BlueLake", "-l", "50", "-o", out, "--quiet", "--socket", s.SocketPath)
		if code != 0 || stdout != "" || stderr != "" {
			t.Errorf("%s = %d %q %q", verb, code, stdout, stderr)
		}
		if r := reads(); len(r) != 1 || r[0] != (paneRead{"w1:p2", 50}) {
			t.Errorf("%s reads = %v", verb, r)
		}
	}
}

// Without a pane, the type flags pick the panes; without those, the
// active pane (herdr's focused pane of the active tab).
func TestCopyTargetOrder(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	out := filepath.Join(t.TempDir(), "out.txt")
	panes := func(args ...string) []string {
		t.Helper()
		code, stdout, stderr := run(append(append([]string{"copy"}, args...), "-o", out, "--json", "--socket", s.SocketPath)...)
		if code != 0 {
			t.Fatalf("copy %v = %d %q %q", args, code, stdout, stderr)
		}
		var r copyResult
		if err := json.Unmarshal([]byte(stdout), &r); err != nil {
			t.Fatalf("%v: %q", err, stdout)
		}
		reads()
		return r.Panes
	}
	if got := strings.Join(panes("swarm"), ","); got != "swarm:.2" {
		t.Errorf("no filter = %s, want the active pane", got)
	}
	if got := strings.Join(panes("swarm", "--cc"), ","); got != "swarm:w1:p4,swarm:.1" {
		t.Errorf("--cc = %s", got)
	}
	// The shell has no text, so --all copies three panes.
	if got := strings.Join(panes("swarm", "--all"), ","); got != "swarm:w1:p4,swarm:.1,swarm:.2" {
		t.Errorf("--all = %s", got)
	}
	if got := strings.Join(panes("swarm:p4", "--cod"), ","); got != "swarm:w1:p4" {
		t.Errorf("a pane and --cod = %s", got)
	}
	if got := strings.Join(panes("swarm:0.2"), ","); got != "swarm:.2" {
		t.Errorf("a slot = %s", got)
	}
}

// --pattern keeps the matching lines; --code keeps the code blocks, with
// no header unless --headers.
func TestCopyPatternAndCode(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	out := filepath.Join(t.TempDir(), "out.txt")
	file := func(args ...string) string {
		t.Helper()
		code, stdout, stderr := run(append(append([]string{"copy", "swarm"}, args...), "-o", out, "--socket", s.SocketPath)...)
		if code != 0 {
			t.Fatalf("copy %v = %d %q %q", args, code, stdout, stderr)
		}
		data, _ := os.ReadFile(out)
		return string(data)
	}
	// The unbound claude has no matching line, so it is left out.
	if got := file("--cc", "-p", "^ERROR"); got != "═══ IcyKnoll opus (pane 1) ═══\nERROR icy\n" {
		t.Errorf("--pattern = %q", got)
	}
	if got := file("--all", "--code"); got != "fmt.Println(1)" {
		t.Errorf("--code = %q", got)
	}
	if got := file("--all", "--code", "--headers"); got != "═══ IcyKnoll opus (pane 1) ═══\nfmt.Println(1)\n" {
		t.Errorf("--code --headers = %q", got)
	}
}

// ntm's errors, as text with exit 1, or as the robot envelope with
// --json.
func TestCopyRefusals(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	noClipboardTools(t)
	out := filepath.Join(t.TempDir(), "out.txt")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "--last", "0", "-o", out}, "Error: --last must be positive\n"},
		{[]string{"swarm:7", "-o", out}, "Error: no matching panes found\n"},
		{[]string{"swarm", "--gmi", "-o", out}, "Error: no matching panes found\n"},
		{[]string{"swarm", "-p", "(", "-o", out}, "Error: invalid pattern regex: error parsing regexp: missing closing ): `(`\n"},
		{[]string{"swarm:p3", "-o", out}, "Error: no content captured (check filters)\n"},
		{[]string{"swarm:%12", "-o", out}, "Error: pane %12: herdr has no tmux pane ids; give a pane index, the slot N.M (nhm status shows it) or a herdr pane id\n"},
		{[]string{"swarm"}, "Error: failed to init clipboard: no clipboard utility found (install wl-copy, xclip, or xsel)\n"},
		{[]string{"nosuch", "-o", out}, "Error: session \"nosuch\" not found (available: swarm)\n"},
		{[]string{"a", "b", "-o", out}, "Error: accepts at most 1 arg(s), received 2\n"},
	} {
		code, stdout, stderr := run(append(append([]string{"copy"}, c.args...), "--socket", s.SocketPath)...)
		if code != 1 || stdout != "" || !strings.HasPrefix(stderr, c.want) {
			t.Errorf("copy %v = %d %q %q, want %q", c.args, code, stdout, stderr, c.want)
		}
	}
	reads()
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a refused copy wrote %s", out)
	}
	code, stdout, _ := run("copy", "swarm", "--last", "0", "--json", "--socket", s.SocketPath)
	var env struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if code != 1 || json.Unmarshal([]byte(stdout), &env) != nil || env.Success || env.Error != "--last must be positive" {
		t.Errorf("--json refusal = %d %q", code, stdout)
	}
}

// noClipboardTools leaves the test no clipboard tool and no display, and
// no herdr pane.
func noClipboardTools(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	for _, v := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE", "WSL_DISTRO_NAME", "WSL_INTEROP", "HERDR_PANE_ID"} {
		t.Setenv(v, "")
	}
}

// Without --output, copy puts the text on the clipboard through ntm's
// first tool found (xclip here, with DISPLAY), and says so.
func TestCopyToClipboard(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	noClipboardTools(t)
	bin := t.TempDir()
	clip := filepath.Join(t.TempDir(), "clip")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"-selection clipboard\" ] || exit 3\nexec /bin/cat > " + clip + "\n"
	if err := os.WriteFile(filepath.Join(bin, "xclip"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("DISPLAY", ":0")
	code, stdout, stderr := run("copy", "swarm:1", "--socket", s.SocketPath)
	if code != 0 || stderr != "" || stdout != "✓ Copied 6 lines from 1 pane(s) to clipboard\n" {
		t.Fatalf("copy = %d %q %q", code, stdout, stderr)
	}
	if data, _ := os.ReadFile(clip); !strings.HasPrefix(string(data), "═══ IcyKnoll opus (pane 1) ═══\nicy one\n") {
		t.Errorf("clipboard = %q", data)
	}
	code, stdout, _ = run("copy", "swarm:1", "--json", "--socket", s.SocketPath)
	var r copyResult
	if code != 0 || json.Unmarshal([]byte(stdout), &r) != nil || r.Destination != "clipboard" || r.OutputPath != "" {
		t.Errorf("--json = %d %s", code, stdout)
	}
	// A tool that fails is ntm's error, with the backend's name.
	if err := os.WriteFile(filepath.Join(bin, "xclip"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = run("copy", "swarm:1", "--socket", s.SocketPath)
	if code != 1 || stderr != "Error: failed to copy to clipboard via xclip: exit status 1\n" {
		t.Errorf("a failing xclip = %d %q", code, stderr)
	}
}

// Inside a herdr pane with no clipboard tool, copy writes OSC 52 to the
// pane's terminal, which herdr passes to its clients.
func TestCopyToHerdrClipboard(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	noClipboardTools(t)
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	tty := filepath.Join(t.TempDir(), "tty")
	old := openTTY
	t.Cleanup(func() { openTTY = old })
	openTTY = func() (io.WriteCloser, error) { return os.Create(tty) }
	code, stdout, stderr := run("copy", "swarm:2", "--socket", s.SocketPath)
	if code != 0 || stderr != "" || stdout != "✓ Copied 3 lines from 1 pane(s) to clipboard\n" {
		t.Fatalf("copy = %d %q %q", code, stdout, stderr)
	}
	data, _ := os.ReadFile(tty)
	text := "═══ BlueLake gpt (pane 2) ═══\nblue one\nERROR blue\n\n"
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"; string(data) != want {
		t.Errorf("tty got %q, want %q", data, want)
	}
}

// --json prints ntm's copyResult; a secret is reported, in warn mode, on
// stderr and in redaction, and the text is not changed.
func TestCopyJSON(t *testing.T) {
	secret := "gh" + "p_" + strings.Repeat("Z9y8", 9)
	s, _, _ := paneServer(t, func(pane string) string { return "token " + secret + "\n" })
	out := filepath.Join(t.TempDir(), "out.txt")
	code, stdout, stderr := run("copy", "swarm:BlueLake", "-p", "token", "-o", out, "--json", "--socket", s.SocketPath)
	if code != 0 || stderr != "Warning: detected 1 potential secret(s) (GITHUB_TOKEN=1); output is NOT modified (mode=warn)\n" {
		t.Fatalf("copy = %d %q %q", code, stdout, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"source": "swarm:BlueLake", "panes": []any{"swarm:.2"}, "lines": float64(2), "destination": "file",
		"pattern": "token", "output_path": out,
		"redaction": map[string]any{"mode": "warn", "findings": float64(1), "categories": map[string]any{"GITHUB_TOKEN": float64(1)}, "action": "warn"}}
	for k, v := range want {
		gb, _ := json.Marshal(got[k])
		wb, _ := json.Marshal(v)
		if string(gb) != string(wb) {
			t.Errorf("%s = %s, want %s", k, gb, wb)
		}
	}
	if _, ok := got["code"]; ok {
		t.Errorf("code is set without --code: %s", stdout)
	}
	if data, _ := os.ReadFile(out); !strings.Contains(string(data), secret) {
		t.Errorf("warn mode changed the text: %q", data)
	}
	if !strings.Contains(stdout, "\n  \"source\"") {
		t.Errorf("JSON is not indented: %s", stdout)
	}
}
