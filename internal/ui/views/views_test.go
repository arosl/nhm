package views

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/tui/components"
)

func keyOf(k string) tea.KeyMsg {
	switch k {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// step sends msg to v and runs the command it returns, once; it returns
// the command's message (nil for none).
func step(t *testing.T, v tea.Model, msg tea.Msg) tea.Msg {
	t.Helper()
	_, cmd := v.Update(msg)
	if cmd == nil {
		return nil
	}
	return cmd()
}

// open starts a view the way the menu does: its size, then Init, and
// feeds the answer of Init's load back.
func open(t *testing.T, v View, w, h int) {
	t.Helper()
	v.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if cmd := v.Init(); cmd != nil {
		v.Update(cmd())
	}
}

func text(fn func(context.Context) (string, error)) Text { return fn }

func fixed(s string, err error) Text {
	return func(context.Context) (string, error) { return s, err }
}

// Every view is a view row of the action table, and the view rows that
// this package owns all open (the split with nhm-qbi.4).
func TestNewOpensTheMenuRows(t *testing.T) {
	for _, id := range IDs {
		it, ok := actions.ByID(id)
		if !ok || it.Kind != actions.KindView || it.Selection != "" && it.Selection != actions.SelectNone {
			t.Errorf("%s: action row %+v, %v", id, it, ok)
		}
		v, ok := New(id, Env{})
		if !ok || v.Title() == "" {
			t.Errorf("New(%s) = %v, %v", id, v, ok)
		}
	}
	for _, id := range []string{"agent.read", "operations", "view.status", "nope"} {
		if _, ok := New(id, Env{}); ok {
			t.Errorf("New(%s) opened a view of another track", id)
		}
	}
}

func TestPageShowsCleanText(t *testing.T) {
	hostile := "red \x1b[31mtext\x1b[0m\x1b]0;evil title\x07 bell\a c1\u009b2J tab\tend"
	v, _ := New("diag.health", Env{Health: fixed(hostile, nil), Now: func() time.Time { return time.Date(2026, 10, 13, 3, 4, 5, 0, time.UTC) }})
	open(t, v, 80, 10)
	out := v.View()
	if strings.ContainsAny(out, "\x1b\a\u009b") || !strings.Contains(out, "red text bell c12J tab    end") {
		t.Fatalf("view = %q", out)
	}
	if !strings.Contains(out, "as of 03:04:05Z") || !strings.Contains(out, "health") {
		t.Errorf("header: %q", out)
	}
}

func TestPageDropsAStaleAnswer(t *testing.T) {
	n := 0
	p := newPage("x", Env{}, text(func(context.Context) (string, error) {
		n++
		return strings.Repeat("answer", 1) + string(rune('0'+n)), nil
	}))
	first := p.Init()
	second := p.refresh()
	newer, older := second(), first()
	p.Update(older)
	if p.text != "" || p.state != pageLoading {
		t.Fatalf("a stale answer was taken: %q %v", p.text, p.state)
	}
	p.Update(newer)
	if p.state != pageReady || !strings.HasPrefix(p.text, "answer") {
		t.Fatalf("the current answer = %q %v", p.text, p.state)
	}
	// An answer to a closed view never matches a newer view.
	q := newPage("y", Env{}, fixed("y", nil))
	q.Init()
	q.Update(older)
	if q.text != "" {
		t.Errorf("another view's answer was taken: %q", q.text)
	}
}

func TestPageTimesOutAndKeepsText(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	calls := 0
	p := newPage("x", Env{Timeout: 50 * time.Millisecond}, text(func(ctx context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "first", nil
		}
		<-block // a loader that ignores ctx, as a verb run in process may
		return "late", nil
	}))
	open(t, p, 60, 10)
	if p.text != "first" {
		t.Fatalf("first load = %q", p.text)
	}
	start := time.Now()
	p.Update(p.refresh()())
	if time.Since(start) > 5*time.Second || p.state != pageFailed || !strings.Contains(p.err.Error(), "no answer in") {
		t.Fatalf("a hung load: state %v err %v after %s", p.state, p.err, time.Since(start))
	}
	if out := p.View(); !strings.Contains(out, "first") || !strings.Contains(out, "no answer in") {
		t.Errorf("a failed refresh lost the text or its error: %q", out)
	}
}

func TestPageErrorsAndMissingLoaders(t *testing.T) {
	v, _ := New("diag.recover", Env{Recover: fixed("", errors.New("no instance \x1b[2Jhere"))})
	open(t, v, 80, 10)
	if out := v.View(); !strings.Contains(out, "no instance here") || strings.Contains(out, "\x1b") {
		t.Errorf("error view = %q", out)
	}
	v, _ = New("diag.version", Env{})
	open(t, v, 80, 10)
	if out := v.View(); !strings.Contains(out, "not available in this menu") {
		t.Errorf("missing loader view = %q", out)
	}
	// An empty answer says what it means.
	v, _ = New("diag.recover", Env{Recover: fixed("", nil)})
	open(t, v, 80, 10)
	if out := v.View(); !strings.Contains(out, "nothing to recover") {
		t.Errorf("empty recover view = %q", out)
	}
	// A panic in a loader is an error, not a crash.
	v, _ = New("view.feedback", Env{Feedback: text(func(context.Context) (string, error) { panic("boom") })})
	open(t, v, 80, 10)
	if out := v.View(); !strings.Contains(out, "the load failed: boom") {
		t.Errorf("panic view = %q", out)
	}
	// A verb that fails a check still shows what it printed.
	v, _ = New("diag.health", Env{Health: fixed("overall: critical\n", errors.New("exit 1"))})
	open(t, v, 80, 10)
	if out := v.View(); !strings.Contains(out, "overall: critical") {
		t.Errorf("failed verb's text = %q", out)
	}
}

func TestBackKeysAndRefresh(t *testing.T) {
	calls := 0
	for _, id := range IDs {
		v, _ := New(id, Env{Health: text(func(context.Context) (string, error) { calls++; return "ok", nil })})
		open(t, v, 80, 20)
		for _, k := range []string{"esc", "q", "backspace", "left"} {
			if msg := step(t, v, keyOf(k)); msg != (Back{}) {
				t.Errorf("%s: %s gave %#v", id, k, msg)
			}
		}
	}
	h, _ := New("diag.health", Env{Health: text(func(context.Context) (string, error) { calls++; return "ok", nil })})
	open(t, h, 80, 20)
	before := calls
	if msg := step(t, h, keyOf("r")); msg == nil {
		t.Fatal("r gave no load")
	} else {
		h.Update(msg)
	}
	if calls != before+1 {
		t.Errorf("r loaded %d times", calls-before)
	}
}

func TestDoctorShowsChecks(t *testing.T) {
	v, _ := New("diag.doctor", Env{Doctor: func(context.Context) ([]Check, error) {
		return []Check{
			{Name: "herdr", Result: "ok", Detail: "protocol 22", Fix: "never shown"},
			{Name: "agent mail", Result: "fail", Detail: "down\x1b[2J", Fix: "start it"},
		}, nil
	}})
	open(t, v, 100, 20)
	out := v.View()
	for _, want := range []string{"ok   herdr", "protocol 22", "fail agent mail", "fix: start it", "1 checks failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "never shown") || strings.Contains(out, "\x1b[2J") {
		t.Errorf("doctor = %q", out)
	}
}

func TestModesListAndExplain(t *testing.T) {
	v, _ := New("view.modes", Env{})
	m := v.(*modes)
	open(t, v, 120, 30)
	core := len(m.rows)
	if core == 0 || m.err != nil {
		t.Fatalf("core modes: %d %v", core, m.err)
	}
	v.Update(keyOf("down"))
	picked := m.rows[m.list.cursor].ID
	v.Update(keyOf("a"))
	if len(m.rows) <= core || m.rows[m.list.cursor].ID != picked {
		t.Fatalf("all tiers: %d rows, cursor on %s, want %s", len(m.rows), m.rows[m.list.cursor].ID, picked)
	}
	if out := v.View(); !strings.Contains(out, "every tier") {
		t.Errorf("all tiers note: %q", out)
	}
	name := m.rows[m.list.cursor].Name
	if msg := step(t, v, keyOf("enter")); msg != nil {
		v.Update(msg)
	}
	if m.detail == nil || m.detail.state != pageReady || !strings.Contains(v.View(), name) {
		t.Fatalf("explain: %v %q", m.detail, v.View())
	}
	if msg := step(t, v, keyOf("esc")); msg != nil || m.detail != nil {
		t.Fatalf("esc in the card: %#v, detail %v", msg, m.detail)
	}
	if msg := step(t, v, keyOf("esc")); msg != (Back{}) {
		t.Errorf("esc in the list: %#v", msg)
	}
}

// writeRun puts a run on disk as ensemble.Start leaves it.
func writeRun(t *testing.T, dir string, r ensemble.Run) {
	t.Helper()
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, r.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, r.ID, "run.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// treeHash is the content of every file under dir.
func treeHash(t *testing.T, dir string) [32]byte {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		_, _ = io.WriteString(h, p)
		_, _ = h.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return [32]byte(h.Sum(nil))
}

func TestEnsemblesPresetsRunsAndStop(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	running := ensemble.Run{ID: "ens-20261013T030000-aaaaaa", Preset: "project-diagnosis", Status: ensemble.RunRunning, CreatedAt: now}
	stopped := ensemble.Run{ID: "ens-20261013T020000-bbbbbb", Preset: "idea-forge", Status: ensemble.RunStopped, CreatedAt: now.Add(-time.Hour)}
	writeRun(t, dir, running)
	writeRun(t, dir, stopped)
	before := treeHash(t, dir)
	var asked []string
	v, _ := New("view.ensembles", Env{RunsDir: dir, RunStatus: func(_ context.Context, id string) (string, error) {
		asked = append(asked, id)
		return "run " + id + ": status text\x1b]0;x\x07", nil
	}})
	e := v.(*ensembles)
	open(t, v, 140, 30)
	if len(e.presets) == 0 || len(e.runs) != 2 || e.runs[0].ID != running.ID {
		t.Fatalf("presets %d, runs %+v", len(e.presets), e.runs)
	}
	// Presets: s does nothing, enter shows ntm's verbose detail.
	if msg := step(t, v, keyOf("s")); msg != nil {
		t.Errorf("s on a preset: %#v", msg)
	}
	if msg := step(t, v, keyOf("enter")); msg != nil {
		v.Update(msg)
	}
	if !strings.Contains(v.View(), "Name:") {
		t.Fatalf("preset detail: %q", v.View())
	}
	v.Update(keyOf("esc"))

	// Runs: the newest first; s on a running run asks for the stop form.
	v.Update(keyOf("tab"))
	stopHint := components.RenderKeyHint(components.KeyHint{Key: "s", Desc: "stop"})
	if out := v.View(); !strings.Contains(out, running.ID) || !strings.Contains(out, stopHint) {
		t.Fatalf("runs list: %q", out)
	}
	if msg := step(t, v, keyOf("s")); msg != (Open{Row: "view.ensembles", Action: "stop", Arg: running.ID}) {
		t.Fatalf("s on a running run: %#v", msg)
	}
	v.Update(keyOf("down"))
	if msg := step(t, v, keyOf("s")); msg != nil || strings.Contains(v.View(), stopHint) {
		t.Errorf("s on a stopped run: %#v", msg)
	}
	v.Update(keyOf("up"))
	if msg := step(t, v, keyOf("enter")); msg != nil {
		v.Update(msg)
	}
	out := v.View()
	if !slices.Equal(asked, []string{running.ID}) || !strings.Contains(out, "status text") || strings.Contains(out, "\x1b]") {
		t.Fatalf("run status: asked %v, view %q", asked, out)
	}
	if msg := step(t, v, keyOf("s")); msg != (Open{Row: "view.ensembles", Action: "stop", Arg: running.ID}) {
		t.Errorf("s in the status page: %#v", msg)
	}
	v.Update(keyOf("esc"))
	if msg := step(t, v, keyOf("esc")); msg != (Back{}) {
		t.Errorf("esc in the runs list: %#v", msg)
	}
	// r reads the runs again and keeps the cursor on its run.
	v.Update(keyOf("down"))
	writeRun(t, dir, ensemble.Run{ID: "ens-20261013T040000-cccccc", Preset: "p", Status: ensemble.RunStarting, CreatedAt: now.Add(time.Hour)})
	before = treeHash(t, dir)
	if msg := step(t, v, keyOf("r")); msg != nil {
		v.Update(msg)
	}
	if len(e.runs) != 3 || e.runs[e.lists[tabRuns].cursor].ID != stopped.ID {
		t.Errorf("after r: %d runs, cursor on %s", len(e.runs), e.runs[e.lists[tabRuns].cursor].ID)
	}
	// Read only: the view changed no file.
	if treeHash(t, dir) != before {
		t.Error("the ensembles view changed the runs directory")
	}

	// No instance: the runs list says so.
	v, _ = New("view.ensembles", Env{})
	open(t, v, 140, 30)
	v.Update(keyOf("tab"))
	if out := v.View(); !strings.Contains(out, "no nhm instance") {
		t.Errorf("runs without an instance: %q", out)
	}
}

// Every view fits a small popup: no line wider than the popup, no more
// lines than its height.
func TestViewsFitTheWindow(t *testing.T) {
	dir := t.TempDir()
	writeRun(t, dir, ensemble.Run{ID: "ens-20261013T030000-aaaaaa", Preset: strings.Repeat("long", 20), Status: ensemble.RunRunning})
	env := Env{RunsDir: dir, Health: fixed(strings.Repeat("a very long line of health text ", 20)+"\n"+strings.Repeat("x\n", 50), nil),
		RunStatus: func(context.Context, string) (string, error) { return strings.Repeat("status ", 50), nil }}
	for _, id := range IDs {
		for _, size := range [][2]int{{40, 12}, {100, 30}} {
			v, _ := New(id, env)
			open(t, v, size[0], size[1])
			for _, k := range []string{"tab", "enter"} {
				if msg := step(t, v, keyOf(k)); msg != nil {
					v.Update(msg)
				}
				lines := strings.Split(v.View(), "\n")
				if len(lines) > size[1] {
					t.Errorf("%s at %v after %s: %d lines", id, size, k, len(lines))
				}
				for _, l := range lines {
					if w := lipgloss.Width(l); w > size[0] {
						t.Errorf("%s at %v after %s: a line of %d columns: %q", id, size, k, w, l)
						break
					}
				}
			}
		}
	}
}

// harness runs a view alone in a tea.Program, as the menu will embed it,
// and quits on Back.
type harness struct {
	v    View
	back bool
}

func (h *harness) Init() tea.Cmd { return h.v.Init() }

func (h *harness) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(Back); ok {
		h.back = true
		return h, tea.Quit
	}
	_, cmd := h.v.Update(msg)
	return h, cmd
}

func (h *harness) View() string { return h.v.View() }

func TestViewsRunInATeaProgram(t *testing.T) {
	for _, id := range IDs {
		v, _ := New(id, Env{Health: fixed("ok", nil)})
		h := &harness{v: v}
		p := tea.NewProgram(h, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
		go func() {
			p.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
			p.Send(keyOf("down"))
			p.Send(keyOf("esc"))
			p.Send(keyOf("esc")) // a view with a detail page open closes it first
		}()
		done := make(chan error, 1)
		go func() { _, err := p.Run(); done <- err }()
		select {
		case err := <-done:
			if err != nil || !h.back {
				t.Errorf("%s: %v, back %v", id, err, h.back)
			}
		case <-time.After(10 * time.Second):
			p.Kill()
			t.Fatalf("%s: the program did not end", id)
		}
	}
}
