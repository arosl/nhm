package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

type logSink struct{ lines []string }

func (l *logSink) log(s string) { l.lines = append(l.lines, s) }

func (l *logSink) has(sub string) bool {
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func step(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	nm, cmd := m.Update(msg)
	mm, ok := nm.(model)
	if !ok {
		t.Fatalf("Update returned %T", nm)
	}
	return mm, cmd
}

func TestResizeAndKeysAreLogged(t *testing.T) {
	l := &logSink{}
	m := newModel(time.Millisecond, l.log, "truecolor")
	m, _ = step(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x"), Alt: true})
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	if m.width != 80 || m.height != 24 {
		t.Fatalf("size = %dx%d", m.width, m.height)
	}
	for _, want := range []string{"resize 80x24 phase=form", `key "esc"`, `key "alt+x"`, `key " "`} {
		if !l.has(want) {
			t.Errorf("log lacks %q: %q", want, l.lines)
		}
	}
	if !strings.Contains(m.View(), "80x24") || !strings.Contains(m.View(), "truecolor") {
		t.Errorf("view lacks size or profile")
	}
}

func TestPasteIsLoggedAsOneLine(t *testing.T) {
	l := &logSink{}
	m := newModel(time.Millisecond, l.log, "ascii (mono)")
	text := strings.Repeat("line of pasted text\n", 199) + "last"
	step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true})
	if !l.has("paste runes=3984 lines=200") {
		t.Fatalf("log = %q", l.lines)
	}
}

func TestCtrlCQuits(t *testing.T) {
	l := &logSink{}
	m := newModel(time.Millisecond, l.log, "ansi16")
	_, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit")
	}
}

// The form completes, the operation runs asynchronously, and the result
// screen stays through other keys until enter dismisses it.
func TestFormOperationResultFlow(t *testing.T) {
	l := &logSink{}
	m := newModel(10*time.Millisecond, l.log, "truecolor")
	m.vals.label = "probe"
	m.vals.text = "a\nb"
	m.form.State = huh.StateCompleted
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.phase != phaseRunning || cmd == nil {
		t.Fatalf("phase = %s, cmd nil = %t", m.phase, cmd == nil)
	}
	start := time.Now()
	msg := cmd()
	if time.Since(start) < 10*time.Millisecond {
		t.Errorf("operation finished before its delay")
	}
	done, ok := msg.(opDoneMsg)
	if !ok || done.label != "probe" || done.lines != 2 {
		t.Fatalf("msg = %#v", msg)
	}
	// Keys during the operation are logged and do not end it.
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if m.phase != phaseRunning {
		t.Fatalf("phase = %s after a key while running", m.phase)
	}
	m, _ = step(t, m, done)
	if m.phase != phaseResult || !strings.Contains(m.View(), `label "probe", 2 text line(s)`) {
		t.Fatalf("phase = %s view = %q", m.phase, m.View())
	}
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyRunes, Runes: []rune("x")}, {Type: tea.KeySpace, Runes: []rune(" ")}} {
		m, _ = step(t, m, k)
		if m.phase != phaseResult {
			t.Fatalf("%s left the result screen", k)
		}
	}
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.phase != phaseForm || m.vals.label != "" {
		t.Fatalf("enter did not give a fresh form: %s %q", m.phase, m.vals.label)
	}
	for _, want := range []string{"form completed, op 1 starts", "op done after", "result dismissed"} {
		if !l.has(want) {
			t.Errorf("log lacks %q", want)
		}
	}
}

// drain runs cmd and feeds its messages back into m, like bubbletea's
// loop, but drops any command that blocks longer than 50 ms (cursor
// blinks, the fake operation's tick).
func drain(t *testing.T, m model, cmd tea.Cmd, depth int) model {
	t.Helper()
	if cmd == nil || depth > 20 {
		return m
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(50 * time.Millisecond):
		return m
	}
	switch msg := msg.(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = drain(t, m, c, depth+1)
		}
		return m
	case opDoneMsg:
		return m
	default:
		nm, next := step(t, m, msg)
		return drain(t, nm, next, depth+1)
	}
}

// Typing through the embedded huh form (driven by the parent's Update,
// not huh.Form.Run) completes it and starts the operation.
func TestEmbeddedFormByKeys(t *testing.T) {
	l := &logSink{}
	m := newModel(time.Hour, l.log, "truecolor")
	m = drain(t, m, m.Init(), 0)
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("lbl")},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune("one\ntwo"), Paste: true},
		{Type: tea.KeyEnter},
		{Type: tea.KeyEnter},
	}
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = step(t, m, k)
		m = drain(t, m, cmd, 0)
	}
	if m.phase != phaseRunning || m.vals.label != "lbl" || m.vals.text != "one\ntwo" || !m.vals.run {
		t.Fatalf("phase %s vals %+v log %q", m.phase, *m.vals, l.lines)
	}
}

func TestDeclinedRunShowsResultWithoutOperation(t *testing.T) {
	l := &logSink{}
	m := newModel(time.Hour, l.log, "truecolor")
	m.vals.run = false
	m.form.State = huh.StateCompleted
	m, cmd := step(t, m, tea.WindowSizeMsg{Width: 40, Height: 10})
	if m.phase != phaseResult || cmd != nil {
		t.Fatalf("phase = %s cmd nil = %t", m.phase, cmd == nil)
	}
}

func TestFileLoggerStampsUTC(t *testing.T) {
	var b bytes.Buffer
	at := time.Date(2026, 10, 13, 2, 3, 4, 5e6, time.FixedZone("x", 3600))
	fileLogger(&b, func() time.Time { return at })("resize 1x2")
	if got := b.String(); got != "2026-10-13T01:03:04.005Z resize 1x2\n" {
		t.Fatalf("got %q", got)
	}
}
