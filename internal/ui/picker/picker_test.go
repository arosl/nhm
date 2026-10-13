package picker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

func bound(pane, slot, name, id string, gen int64, kind string, state herdr.AgentStatus) snapshot.Agent {
	return snapshot.Agent{PaneID: pane, WorkspaceID: "w1", Slot: slot, Kind: kind, State: state,
		Binding:   &snapshot.BindingRef{ID: id, Generation: gen, Status: bindings.Active, MailName: name},
		Session:   &herdr.AgentSession{Kind: "id", Value: "s-" + pane},
		HerdrName: strings.ToLower(name)}
}

func unbound(pane, session string) snapshot.Agent {
	return snapshot.Agent{PaneID: pane, WorkspaceID: "w1", Kind: "claude", State: herdr.StatusIdle, HerdrName: "loose",
		Session: &herdr.AgentSession{Kind: "id", Value: session}}
}

func model(agents ...snapshot.Agent) *snapshot.Model { return &snapshot.Model{Agents: agents} }

// fixed is a source that answers with whatever *m holds when it runs.
func fixed(m **snapshot.Model) Source {
	return func(context.Context) (*snapshot.Model, error) { return *m, nil }
}

func keys(s string) []tea.KeyMsg {
	var out []tea.KeyMsg
	for _, k := range strings.Fields(s) {
		switch k {
		case "enter":
			out = append(out, tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			out = append(out, tea.KeyMsg{Type: tea.KeyEsc})
		case "down":
			out = append(out, tea.KeyMsg{Type: tea.KeyDown})
		case "up":
			out = append(out, tea.KeyMsg{Type: tea.KeyUp})
		case "space":
			out = append(out, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		default:
			out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		}
	}
	return out
}

// press sends keys and returns the model and the last command's message.
func press(t *testing.T, m Model, s string) (Model, tea.Msg) {
	t.Helper()
	var msg tea.Msg
	for _, k := range keys(s) {
		var cmd tea.Cmd
		m, cmd = m.Update(k)
		if cmd != nil {
			msg = cmd()
			if l, ok := msg.(loaded); ok {
				m, _ = m.Update(l)
				msg = nil
			}
		}
	}
	return m, msg
}

// start returns a picker that read its first snapshot.
func start(t *testing.T, src Source, opts Options) Model {
	t.Helper()
	opts.Workspace = "w1"
	m := New(src, opts)
	m, _ = m.Update(m.Init()())
	return m
}

var (
	blue = bound("w1:p1", "1.1", "BlueLake", "bnd-1", 3, "claude", herdr.StatusWorking)
	red  = bound("w1:p2", "1.2", "RedFox", "bnd-2", 1, "codex", herdr.StatusIdle)
	gray = bound("w1:p3", "1.3", "GrayOwl", "bnd-3", 2, "claude", herdr.StatusIdle)
	away = snapshot.Agent{PaneID: "w2:p1", WorkspaceID: "w2", Kind: "claude",
		Binding: &snapshot.BindingRef{ID: "bnd-9", Generation: 1, Status: bindings.Active, MailName: "Elsewhere"}}
)

func TestPicksMarkedTargetsFrozen(t *testing.T) {
	snap := model(blue, away, red, gray)
	m := start(t, fixed(&snap), Options{Title: "interrupt", Many: true})
	if v := m.View(); strings.Contains(v, "Elsewhere") || !strings.Contains(v, "pick agents to interrupt") {
		t.Fatalf("view shows another workspace or no title:\n%s", v)
	}
	m, msg := press(t, m, "x down down x enter")
	p, ok := msg.(Picked)
	if !ok || p.ID != m.ID() || !m.Done() || m.Canceled() {
		t.Fatalf("msg = %#v, done %v", msg, m.Done())
	}
	want := []ui.Target{ui.AgentTarget(blue), ui.AgentTarget(gray)}
	if !slices.Equal(p.Targets, want) || !slices.Equal(m.Targets(), want) || p.Targets[0].Generation != 3 {
		t.Fatalf("targets = %+v", p.Targets)
	}
	// Done: further keys change nothing.
	if m2, _ := press(t, m, "x down enter"); !slices.Equal(m2.Targets(), want) {
		t.Fatal("keys after the pick changed it")
	}
}

// TestMarksSurviveRefreshAndFilter: marks and the cursor stay on their
// agents when the list is filtered or comes back in another order.
func TestMarksSurviveRefreshAndFilter(t *testing.T) {
	snap := model(blue, red, gray)
	m := start(t, fixed(&snap), Options{Many: true})
	m, _ = press(t, m, "x / r e d enter")
	if ls := m.lines(); len(ls) != 1 || ls[0].row.name != "RedFox" {
		t.Fatalf("filtered lines = %d", len(ls))
	}
	m, _ = press(t, m, "esc")
	snap = model(gray, red, blue)
	m, _ = press(t, m, "down r")
	if m.lines()[m.cursor].row.name != "RedFox" {
		t.Fatalf("the cursor moved to %s", m.lines()[m.cursor].row.name)
	}
	_, msg := press(t, m, "enter")
	if p, ok := msg.(Picked); !ok || len(p.Targets) != 1 || p.Targets[0].BindingID != "bnd-1" {
		t.Fatalf("pick after refresh = %#v", msg)
	}
}

// TestReplacedMarkBlocksThePick: a marked agent recycled (new generation)
// or gone stays marked as invalid; the pick is refused until it is
// unmarked, and the agent now in its place is never picked for it.
func TestReplacedMarkBlocksThePick(t *testing.T) {
	snap := model(blue, red)
	m := start(t, fixed(&snap), Options{Many: true})
	m, _ = press(t, m, "x down x")
	recycled := blue
	recycled.Binding = &snapshot.BindingRef{ID: "bnd-1", Generation: 4, Status: bindings.Active, MailName: "BlueLake"}
	snap = model(recycled)
	m, msg := press(t, m, "r enter")
	if msg != nil || m.Done() || !strings.Contains(m.note, "generation 4") {
		t.Fatalf("pick with a replaced mark: %#v, note %q", msg, m.note)
	}
	v := m.View()
	if !strings.Contains(v, "[!] BlueLake  replaced: the binding is at generation 4 now") || !strings.Contains(v, "[!] RedFox  gone") {
		t.Fatalf("view does not show the invalid marks:\n%s", v)
	}
	// Unmark both invalid lines (they follow the list), then pick the new
	// generation explicitly.
	m, msg = press(t, m, "down x x up x enter")
	if p, ok := msg.(Picked); !ok || !m.Done() || len(p.Targets) != 1 || p.Targets[0].Generation != 4 {
		t.Fatalf("pick after unmarking = %#v (note %q, marks %+v)", msg, m.note, m.marks)
	}
}

// TestUnboundPaneReplaced: an unbound agent is its pane and session; a new
// agent in the same pane is another target.
func TestUnboundPaneReplaced(t *testing.T) {
	snap := model(unbound("w1:p5", "s-1"))
	m := start(t, fixed(&snap), Options{Many: true, Takes: actions.TakesAny})
	m, _ = press(t, m, "x")
	snap = model(unbound("w1:p5", "s-2"))
	m, _ = press(t, m, "r enter")
	if m.Done() || !strings.Contains(m.note, "its pane holds another agent now") {
		t.Fatalf("note %q", m.note)
	}
}

func TestEnterPicksTheCursorWithoutMarks(t *testing.T) {
	snap := model(blue, red)
	m := start(t, fixed(&snap), Options{})
	m, msg := press(t, m, "x down enter")
	p, ok := msg.(Picked)
	if !ok || len(p.Targets) != 1 || p.Targets[0].BindingID != "bnd-2" {
		t.Fatalf("single pick = %#v (note %q)", msg, m.note)
	}
	m = start(t, fixed(&snap), Options{Many: true})
	_, msg = press(t, m, "enter")
	if p, ok := msg.(Picked); !ok || len(p.Targets) != 1 || p.Targets[0].BindingID != "bnd-1" {
		t.Fatalf("cursor pick = %#v", msg)
	}
}

// TestWhatCanBePicked: a bound-only action refuses an unbound agent and
// one whose binding is not active; adopt takes only unbound agents.
func TestWhatCanBePicked(t *testing.T) {
	displaced := red
	displaced.Binding = &snapshot.BindingRef{ID: "bnd-2", Generation: 1, Status: bindings.Displaced, MailName: "RedFox"}
	snap := model(unbound("w1:p5", "s-1"), displaced, blue)
	m := start(t, fixed(&snap), Options{Many: true})
	m, msg := press(t, m, "x enter")
	if msg != nil || len(m.marks) != 0 || !strings.Contains(m.note, "nhm adopt binds it") {
		t.Fatalf("unbound agent: marks %d, note %q", len(m.marks), m.note)
	}
	m, msg = press(t, m, "down enter")
	if msg != nil || !strings.Contains(m.note, "displaced") {
		t.Fatalf("displaced binding: note %q", m.note)
	}
	adopt, _ := actions.ByID("agent.adopt")
	a := ForAction(adopt, ui.Context{WorkspaceID: "w1"}, fixed(&snap))
	a, _ = a.Update(a.Init()())
	a, msg = press(t, a, "down down enter")
	if msg != nil || !strings.Contains(a.note, "bound already") {
		t.Fatalf("adopt a bound agent: note %q", a.note)
	}
	_, msg = press(t, a, "up up enter")
	if p, ok := msg.(Picked); !ok || p.Targets[0].Bound() || p.Targets[0].Session != "id:s-1" {
		t.Fatalf("adopt pick = %#v", msg)
	}
	// An unbound agent with no session cannot be frozen for adopt.
	noSession := unbound("w1:p8", "")
	noSession.Session = nil
	snap = model(noSession)
	b := ForAction(adopt, ui.Context{WorkspaceID: "w1"}, fixed(&snap))
	b, _ = b.Update(b.Init()())
	b, msg = press(t, b, "enter")
	if msg != nil || !strings.Contains(b.note, "no agent session") {
		t.Fatalf("adopt without a session: %#v, note %q", msg, b.note)
	}
}

// TestStaleAnswersAreDropped: one read is in flight at a time; a refresh
// during it waits, the older answer is dropped and the refresh's read
// runs then. Another picker's answer changes nothing.
func TestStaleAnswersAreDropped(t *testing.T) {
	first := model(blue)
	m := New(fixed(&first), Options{Workspace: "w1"})
	old := m.Init()
	m, cmd := m.Refresh()
	if cmd != nil || !m.pending {
		t.Fatal("a refresh during a read started a second read")
	}
	other := New(fixed(&first), Options{Workspace: "w1"})
	m, _ = m.Update(other.Init()())
	if m.rows != nil || !m.loading {
		t.Fatal("another picker's answer was used")
	}
	m, cmd = m.Update(old())
	if m.rows != nil || cmd == nil || !m.loading || m.pending {
		t.Fatalf("the older answer was used, or no read followed: rows %+v", m.rows)
	}
	first = model(red)
	m, _ = m.Update(cmd())
	if len(m.rows) != 1 || m.rows[0].name != "RedFox" || m.loading {
		t.Fatalf("rows = %+v", m.rows)
	}
}

func TestLoadingAndErrors(t *testing.T) {
	m := New(func(context.Context) (*snapshot.Model, error) { return nil, errors.New("herdr did not answer") }, Options{Workspace: "w1"})
	if _, msg := press(t, m, "enter"); msg != nil || !strings.Contains(m.View(), "reading the agents") {
		t.Fatalf("pick while loading: %#v\n%s", msg, m.View())
	}
	m, _ = m.Update(m.Init()())
	if v := m.View(); !strings.Contains(v, "cannot read the agents: herdr did not answer (r tries again)") {
		t.Fatalf("view:\n%s", v)
	}
	if _, msg := press(t, m, "enter"); msg != nil {
		t.Fatalf("pick with no agents: %#v", msg)
	}
}

func TestEscClearsTheFilterThenCancels(t *testing.T) {
	snap := model(blue)
	m := start(t, fixed(&snap), Options{})
	m, msg := press(t, m, "/ b l esc")
	if m.filter != "" || m.typing || msg != nil {
		t.Fatalf("esc while typing: filter %q", m.filter)
	}
	m, _ = press(t, m, "/ b l enter")
	m, msg = press(t, m, "esc")
	if m.filter != "" || m.Done() {
		t.Fatalf("first esc: filter %q, done %v", m.filter, m.Done())
	}
	m, msg = press(t, m, "esc")
	if c, ok := msg.(Canceled); !ok || c.ID != m.ID() || !m.Canceled() || len(m.Targets()) != 0 {
		t.Fatalf("second esc: %#v", msg)
	}
}

// TestHostileTextIsCleaned: names and titles from herdr lose their
// terminal controls before they are drawn.
func TestHostileTextIsCleaned(t *testing.T) {
	evil := unbound("w1:p7", "s-7")
	evil.HerdrName = "evil\x1b]0;owned\x07\x1b[2J\rname"
	snap := model(evil)
	m := start(t, fixed(&snap), Options{Takes: actions.TakesAny, Title: "read\x1b[31m"})
	v := m.View()
	if strings.ContainsAny(v, "\x1b\x07\r") || !strings.Contains(v, "evilname") {
		t.Fatalf("view keeps controls: %q", v)
	}
	if got := clean("a\x00b\tc"); got != "abc" {
		t.Fatalf("clean of C0 controls = %q", got)
	}
	// 0x9b is the 8-bit CSI: "\x9bc" asks the terminal for its
	// attributes, and goes as a whole.
	if got := clean("x\x9bcy"); got != "xy" {
		t.Fatalf("clean of an 8-bit CSI = %q", got)
	}
}

func TestSmallWindow(t *testing.T) {
	var agents []snapshot.Agent
	for i := range 20 {
		agents = append(agents, bound("w1:p"+string(rune('a'+i)), "", "Agent"+string(rune('A'+i)), "bnd-"+string(rune('a'+i)), 1, "claude", herdr.StatusIdle))
	}
	snap := model(agents...)
	m := start(t, fixed(&snap), Options{})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	m, _ = press(t, m, strings.Repeat("down ", 15))
	v := m.View()
	if lines := strings.Split(v, "\n"); len(lines) > 8 || !strings.Contains(v, "AgentP") {
		t.Fatalf("view does not fit 30x8 around the cursor:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if len([]rune(l)) > 30 {
			t.Fatalf("line wider than 30: %q", l)
		}
	}
}
