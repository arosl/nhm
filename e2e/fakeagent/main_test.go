// Taken from ntm (github.com/Dicklesworthstone/ntm) e2e/fakeagent/main_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: the tests that ran ntm's tmux detectors are removed (nhm
// has no detection engine; herdr detects state). The render test below is
// nhm's and checks the screen text directly.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureRender returns the screen the fixture would paint, as the plain
// text a tmux capture would show (ANSI clear/home stripped, \r\n -> \n).
func captureRender(t *testing.T, s *agentState) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	s.render()
	os.Stdout = orig
	_ = w.Close()
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	_ = r.Close()
	out := string(buf[:n])
	out = strings.ReplaceAll(out, "\x1b[2J\x1b[H", "")
	out = strings.ReplaceAll(out, "\r\n", "\n")
	return out
}

func newState(t *testing.T, personaName string) *agentState {
	t.Helper()
	p, ok := personas[personaName]
	if !ok {
		t.Fatalf("unknown persona %q", personaName)
	}
	return &agentState{p: p, width: 80, height: 24}
}

// The idle render ends with the persona's composer marker line and its
// footer; a gate screen has no composer marker.
func TestRenderComposerAndGate(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		s := newState(t, name)
		lines := strings.Split(captureRender(t, s), "\n")
		if len(lines) < 2 {
			t.Fatalf("%s: render too short: %q", name, lines)
		}
		if got, want := lines[len(lines)-2], s.p.marker+" "; got != want {
			t.Errorf("%s: composer line = %q, want %q", name, got, want)
		}
		if got := lines[len(lines)-1]; got != s.p.footer {
			t.Errorf("%s: footer = %q, want %q", name, got, s.p.footer)
		}
		s.gate = "trust"
		if out := captureRender(t, s); strings.Contains(out, s.p.marker+" ") {
			t.Errorf("%s: gate screen shows a composer marker: %q", name, out)
		}
	}
}

// Narrow-pane fidelity: the wrap helper hard-wraps at the pane width so a
// 26-column render produces genuinely wrapped physical rows.
func TestWrapNarrowPane(t *testing.T) {
	s := newState(t, "codex")
	s.width = 26
	rows := s.wrap("• Working (4m 51s • esc to interrupt)")
	if len(rows) < 2 {
		t.Fatalf("expected wrapping at width 26, got %d row(s): %q", len(rows), rows)
	}
	for i, r := range rows {
		if n := len([]rune(r)); n > 26 {
			t.Errorf("row %d exceeds width: %d runes", i, n)
		}
	}
}

// Input-byte handling: paste brackets hold newlines as literals, C-u
// clears, bare Escape dismisses the picker, and a split ESC survives
// across chunks.
func TestConsumeInputSemantics(t *testing.T) {
	s := newState(t, "codex")

	rest := s.consume([]byte("hello"))
	if len(rest) != 0 || s.composer != "hello" {
		t.Fatalf("plain text: composer=%q rest=%q", s.composer, rest)
	}

	// Paste with an embedded newline must not submit.
	s.composer = ""
	s.consume([]byte("\x1b[200~line1\rline2\x1b[201~"))
	if s.composer != "line1\nline2" {
		t.Fatalf("paste handling: composer=%q", s.composer)
	}
	if len(s.transcript) != 0 {
		t.Fatalf("paste newline submitted: %v", s.transcript)
	}

	// Enter outside paste submits.
	s.consume([]byte("\r"))
	if s.composer != "" || len(s.transcript) == 0 {
		t.Fatalf("submit failed: composer=%q transcript=%v", s.composer, s.transcript)
	}

	// C-u clears.
	s.consume([]byte("draft"))
	s.consume([]byte{0x15})
	if s.composer != "" {
		t.Fatalf("C-u did not clear composer: %q", s.composer)
	}

	// Split ESC across chunks: first chunk ends in lone ESC -> held back.
	rest = s.consume([]byte("x\x1b"))
	if string(rest) != "\x1b" {
		t.Fatalf("lone trailing ESC not held: %q", rest)
	}
	rest = s.consume(append(rest, []byte("[201~")...)) // completes a CSI
	if len(rest) != 0 {
		t.Fatalf("CSI completion left residue: %q", rest)
	}

	// Bare Escape dismisses the picker; picker swallows Enter first.
	s.pickerOpen = true
	s.composer = "queued"
	s.consume([]byte("\r"))
	if s.pickerOpen || s.composer != "queued" {
		t.Fatalf("picker should swallow Enter and close: open=%v composer=%q", s.pickerOpen, s.composer)
	}
}

// Control verbs mutate state as documented, and the event log is valid JSONL.
func TestControlVerbsAndEventLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	s := newState(t, "claude")
	s.logFile = f

	s.handleControl("strand 3")
	if s.strandRemaining != 3 {
		t.Errorf("strand 3: remaining=%d", s.strandRemaining)
	}
	s.handleControl("work 7")
	if !s.working() {
		t.Error("work verb did not start a working window")
	}
	s.handleControl("gate trust")
	if s.gate != "trust" {
		t.Errorf("gate verb: %q", s.gate)
	}
	s.handleControl("ratelimit codex")
	if len(s.transcript) == 0 || !strings.Contains(s.transcript[len(s.transcript)-1], "usage limit") {
		t.Errorf("ratelimit verb transcript: %v", s.transcript)
	}
	s.handleControl("clear")
	if len(s.transcript) != 0 {
		t.Errorf("clear verb left transcript: %v", s.transcript)
	}
	s.handleControl("picker")
	if !s.pickerOpen {
		t.Error("picker verb did not open picker")
	}
	_ = f.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 5 {
		t.Fatalf("expected >=5 log events, got %d", len(lines))
	}
	for i, line := range lines {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("log line %d not valid JSON: %v (%q)", i, err, line)
		}
		if ev.TS == "" || ev.Event == "" {
			t.Fatalf("log line %d missing ts/event: %+v", i, ev)
		}
		if _, err := time.Parse(time.RFC3339Nano, ev.TS); err != nil {
			t.Fatalf("log line %d bad timestamp: %v", i, err)
		}
	}
	t.Logf("event log: %d valid JSONL events", len(lines))
}
