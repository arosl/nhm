package cli

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func extractText(pane string) string {
	switch pane {
	case "w1:p4":
		return "```\nplain\n```\n"
	case "w1:p1":
		return "intro\n```go\npackage util\n\nfunc A() {}\n```\n"
	case "w1:p2":
		return "```python\n# tools/x.py\nprint(1)\n```\n"
	}
	return ""
}

var extractBar = strings.Repeat("─", 60)

// extract lists every pane's blocks in ntm's pane order, each with its
// language, file and source pane, in ntm's box.
func TestExtractListsBlocks(t *testing.T) {
	s, _, reads := paneServer(t, extractText)
	code, out, errOut := run("extract", "swarm", "--socket", s.SocketPath)
	want := "Found 3 code block(s) in swarm:\n\n" +
		"[1] text (from swarm:w1:p4)\n    ┌" + extractBar + "\n    │ plain\n    └" + extractBar + "\n\n" +
		"[2] go (new file: util/util.go) (from swarm:.1)\n    ┌" + extractBar + "\n    │ package util\n    │ \n    │ func A() {}\n    └" + extractBar + "\n\n" +
		"[3] python (tools/x.py) (from swarm:.2)\n    ┌" + extractBar + "\n    │ # tools/x.py\n    │ print(1)\n    └" + extractBar + "\n\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("extract = %d %q\n%s\nwant\n%s", code, errOut, out, want)
	}
	if got := reads(); len(got) != 4 || got[0].lines != 500 {
		t.Errorf("reads = %v, want 500 lines of every pane", got)
	}
	// The pane: a pane index, a part of the title, --last (the active
	// pane); none found is ntm's error.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"1"}, "Found 1 code block(s) in swarm:1:\n\n[1] go (new file: util/util.go) (from swarm:.1)\n"},
		{[]string{"BlueLake"}, "Found 1 code block(s) in swarm:BlueLake:\n\n[1] python (tools/x.py) (from swarm:.2)\n"},
		{[]string{"--last"}, "Found 1 code block(s) in swarm:\n\n[1] python (tools/x.py) (from swarm:.2)\n"},
		{[]string{"w1:p3"}, "No code blocks found in swarm:w1:p3\n"},
	} {
		code, out, _ := run(append(append([]string{"extract", "swarm"}, c.args...), "--socket", s.SocketPath)...)
		if code != 0 || !strings.HasPrefix(out, c.want) {
			t.Errorf("extract %v = %d\n%s", c.args, code, out)
		}
	}
	code, _, errOut = run("extract", "swarm", "Nobody", "--socket", s.SocketPath)
	if code != 1 || errOut != "Error: pane 'Nobody' not found in session 'swarm'\n" {
		t.Errorf("unknown pane = %d %q", code, errOut)
	}
	code, _, errOut = run("extract", "nosuch", "--socket", s.SocketPath)
	if code != 1 || errOut != "Error: session 'nosuch' does not exist\n" {
		t.Errorf("unknown session = %d %q", code, errOut)
	}
	code, _, errOut = run("extract", "bad:name", "--socket", s.SocketPath)
	if code != 1 || !strings.HasPrefix(errOut, "Error: invalid session name: ") {
		t.Errorf("bad session name = %d %q", code, errOut)
	}
}

// --lang keeps one language; --select keeps block N; a block past the
// last is ntm's error; --copy puts the blocks on the clipboard, each
// under ntm's separator.
func TestExtractLangAndSelect(t *testing.T) {
	s, _, _ := paneServer(t, extractText)
	code, out, _ := run("extract", "swarm", "-l", "py", "--socket", s.SocketPath)
	if code != 0 || !strings.HasPrefix(out, "Found 1 code block(s) in swarm:\n\n[1] python (tools/x.py)") {
		t.Errorf("--lang py = %d\n%s", code, out)
	}
	code, out, _ = run("extract", "swarm", "-s", "2", "--socket", s.SocketPath)
	if code != 0 || !strings.HasPrefix(out, "Found 1 code block(s) in swarm:\n\n[1] go (new file: util/util.go)") {
		t.Errorf("--select 2 = %d\n%s", code, out)
	}
	code, _, errOut := run("extract", "swarm", "-s", "9", "--socket", s.SocketPath)
	if code != 1 || errOut != "Error: block 9 does not exist (found 3 blocks)\n" {
		t.Errorf("--select 9 = %d %q", code, errOut)
	}
	noClipboardTools(t)
	t.Setenv("HERDR_PANE_ID", "w1:p9")
	tty := filepath.Join(t.TempDir(), "tty")
	old := openTTY
	t.Cleanup(func() { openTTY = old })
	openTTY = func() (io.WriteCloser, error) { return os.Create(tty) }
	code, out, _ = run("extract", "swarm", "--copy", "-l", "go", "--socket", s.SocketPath)
	if code != 0 || out != "✓ Copied 1 code block(s) (3 lines) to clipboard\n" {
		t.Errorf("--copy -l go = %d %q", code, out)
	}
	if data, _ := os.ReadFile(tty); string(data) != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("package util\n\nfunc A() {}"))+"\a" {
		t.Errorf("tty = %q", data)
	}
	code, out, _ = run("extract", "swarm", "--copy", "--socket", s.SocketPath)
	want := "// === Block 1 (text) ===\n\nplain\n\n// === Block 2 (go) ===\n\npackage util\n\nfunc A() {}\n\n// === Block 3 (python) ===\n\n# tools/x.py\nprint(1)"
	if data, _ := os.ReadFile(tty); code != 0 || out != "✓ Copied 3 code block(s) (14 lines) to clipboard\n" ||
		string(data) != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(want))+"\a" {
		t.Errorf("--copy = %d %q, tty %q", code, out, data)
	}
}

// --apply writes a block with a relative path after a yes, and refuses an
// absolute path and one that climbs out with "..".
func TestExtractApplyRefusesAbsoluteAndDotDot(t *testing.T) {
	s, _, _ := paneServer(t, func(pane string) string {
		if pane == "w1:p1" {
			return "```conf\n# /etc/evil.conf\nx=1\n```\n```txt\n# ../up.txt\nup\n```\n```txt\n# ok/new.txt\nhello\n```\n```\nno path\n```\n"
		}
		return ""
	})
	dir := t.TempDir()
	t.Chdir(dir)
	answers := filepath.Join(t.TempDir(), "answers")
	if err := os.WriteFile(answers, []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(answers)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = in
	t.Cleanup(func() { os.Stdin = old; _ = in.Close() })
	code, out, _ := run("extract", "swarm", "1", "--apply", "--socket", s.SocketPath)
	want := "[1] update /etc/evil.conf (conf)\n    ✗ Blocked: path escapes current directory\n" +
		"[2] update ../up.txt (txt)\n    ✗ Blocked: path escapes current directory\n" +
		"[3] update ok/new.txt (txt)\n    Apply? [y]es / [n]o / [s]kip all:     ✓ Applied to ok/new.txt\n" +
		"[4] Skipped: no file path detected\n" +
		"\nApplied: 1, Skipped: 3\n"
	if code != 0 || out != want {
		t.Fatalf("--apply = %d\n%s\nwant\n%s", code, out, want)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "ok", "new.txt")); err != nil || string(data) != "# ok/new.txt\nhello" {
		t.Errorf("written = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "up.txt")); !os.IsNotExist(err) {
		t.Errorf("../up.txt was written")
	}
}

// --json prints ntm's ExtractResponse; its errors are ntm's ErrorResponse
// on stdout, exit 1.
func TestExtractJSONErrors(t *testing.T) {
	s, _, _ := paneServer(t, extractText)
	code, out, _ := run("extract", "swarm", "--json", "--socket", s.SocketPath)
	var r ExtractResponse
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("--json = %d %q", code, out)
	}
	if r.Session != "swarm" || r.Source != "swarm" || r.TotalFound != 3 || len(r.Blocks) != 3 || r.GeneratedAt.IsZero() ||
		r.Blocks[1].FilePath != "util/util.go" || !r.Blocks[1].IsNew || r.Blocks[1].SourcePane != "swarm:.1" || r.Blocks[2].StartLine != 1 {
		t.Errorf("response = %+v", r)
	}
	code, out, _ = run("extract", "swarm", "w1:p3", "--json", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "\"blocks\": null") || !strings.Contains(out, "\"total_found\": 0") {
		t.Errorf("no blocks --json = %d %s", code, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"swarm", "--copy"}, `{"success":false,"error":"--copy is not supported with --json output","code":"UNSUPPORTED_COMBINATION","hint":"Run without --json to use --copy, or parse blocks[] from the JSON output and act on it yourself"}`},
		{[]string{"swarm", "--copy", "--apply"}, `{"success":false,"error":"--copy/--apply is not supported with --json output","code":"UNSUPPORTED_COMBINATION","hint":"Run without --json to use --copy/--apply, or parse blocks[] from the JSON output and act on it yourself"}`},
		{[]string{"swarm", "-s", "9"}, `{"success":false,"error":"block 9 does not exist (found 3 blocks)","code":"INVALID_SELECTION"}`},
		{[]string{"nosuch"}, `{"success":false,"error":"session 'nosuch' does not exist","code":"SESSION_NOT_FOUND"}`},
		{[]string{"swarm", "Nobody"}, `{"success":false,"error":"pane 'Nobody' not found","code":"PANE_NOT_FOUND"}`},
	} {
		code, out, errOut := run(append(append([]string{"extract"}, c.args...), "--json", "--socket", s.SocketPath)...)
		if code != 1 || errOut != "" || out != c.want+"\n" {
			t.Errorf("extract %v --json = %d %q %q", c.args, code, out, errOut)
		}
	}
}
