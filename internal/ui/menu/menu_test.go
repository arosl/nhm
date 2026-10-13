package menu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/forms"
	"github.com/arosl/nhm/internal/ui/runner"
	"github.com/arosl/nhm/internal/ui/views"
)

// collect runs cmd as bubbletea would, expanding batches; a command that
// takes longer than a moment (a blink, a refresh tick) is dropped.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(200 * time.Millisecond):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collect(c)...)
		}
		return out
	}
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			out = append(out, collect(v.Index(i).Interface().(tea.Cmd))...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// pump delivers what cmd produces, and what those deliveries produce.
func pump(m *Model, cmd tea.Cmd, depth int) {
	if depth > 12 {
		return
	}
	for _, msg := range collect(cmd) {
		if msg == tea.Quit() {
			continue
		}
		_, next := m.Update(msg)
		pump(m, next, depth+1)
	}
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		pump(m, cmd, 0)
	}
}

type fakeCore struct {
	kills []lifecycle.KillRequest
	woken [][]ui.Target
	// working makes kill's checks refuse IcyKnoll.
	working bool
}

func (f *fakeCore) core() actions.Core {
	return actions.Core{
		Kill: func(_ context.Context, r lifecycle.KillRequest) (lifecycle.KillResult, error) {
			f.kills = append(f.kills, r)
			if f.working {
				return lifecycle.KillResult{WorkspaceID: r.WorkspaceID, Refusals: []string{"IcyKnoll: it is working"},
						Agents: []lifecycle.RetireResult{{MailName: "IcyKnoll", Refusals: []string{"it is working"}}}},
					fmt.Errorf("%w: IcyKnoll: it is working", lifecycle.ErrRefused)
			}
			if !r.Yes {
				return lifecycle.KillResult{WorkspaceID: r.WorkspaceID, Refusals: []string{},
					Agents: []lifecycle.RetireResult{{MailName: "IcyKnoll", DryRun: true}}}, fmt.Errorf("%w: give --yes", lifecycle.ErrRefused)
			}
			return lifecycle.KillResult{OperationID: r.OperationID, Closed: true, Refusals: []string{}}, nil
		},
		Wake: func(_ context.Context, ts []ui.Target, _ string) (wake.Report, error) {
			f.woken = append(f.woken, ts)
			return wake.Report{Decisions: []wake.Decision{{BindingID: ts[0].BindingID, MailName: "IcyKnoll", Action: wake.ActionNudged}}}, nil
		},
	}
}

type fakeOps struct{}

func (fakeOps) RecentOperations(context.Context, int) ([]lifecycle.RecentOperation, error) {
	return []lifecycle.RecentOperation{{OperationID: "op-9", Verb: lifecycle.VerbKill, Status: lifecycle.KillClosed,
		CreatedAt: time.Now(), UpdatedAt: time.Now()}}, nil
}

func (fakeOps) RecentJobs(int) ([]runner.Entry, error) { return nil, nil }

var ctxW = ui.Context{SocketPath: "/run/herdr.sock", WorkspaceID: "w1", WorkspaceLabel: "proj-a", Cwd: "/data/p"}

func newMenu(c ui.Context, fc *fakeCore) *Model {
	n := 0
	return New(Env{Context: c, Core: fc.core(), Operations: fakeOps{},
		NewID: func() (string, error) { n++; return fmt.Sprintf("op-%d", n), nil },
		Views: views.Env{}})
}

func ids(rows []actions.Action) string {
	var out []string
	for _, a := range rows {
		out = append(out, a.ID)
	}
	return strings.Join(out, " ")
}

// A row shows when the menu can open it: groups always, a surface that is
// there, a view it can open, a built form whose seam the core has.
func TestRowsShowWhatCanOpen(t *testing.T) {
	m := newMenu(ctxW, &fakeCore{})
	top := ids(m.Rows(""))
	for _, want := range []string{"operations", "agent", "workspace", "views", "diagnostics"} {
		if !strings.Contains(top, want) {
			t.Errorf("top level %q lacks %s", top, want)
		}
	}
	if strings.Contains(top, "palette") || strings.Contains(top, "board") {
		t.Errorf("a surface that is not there shows: %q", top)
	}
	ws := ids(m.Rows("workspace"))
	if !strings.Contains(ws, "workspace.kill") || !strings.Contains(ws, "workspace.attach") {
		t.Errorf("workspace level = %q", ws)
	}
	// Built, but this core has no focus seam: shown, with the reason.
	attach, _ := actions.ByID("workspace.attach")
	if why := m.unavailable(attach); why != "this surface cannot focus a workspace" {
		t.Errorf("focus without its seam: %q", why)
	}
	// An agent's view needs the picker: hidden without one.
	if agent := ids(m.Rows("agent")); strings.Contains(agent, "agent.read") || strings.Contains(agent, "agent.inbox") {
		t.Errorf("agent views without a picker: %q", agent)
	}
	if diag := ids(m.Rows("diagnostics")); !strings.Contains(diag, "diag.doctor") {
		t.Errorf("diagnostics = %q", diag)
	}
	// A menu whose palettes are there (it looks for surfaces once).
	m = newMenu(ctxW, &fakeCore{})
	m.env.Surface = func(id string) *exec.Cmd {
		if id == "palette" || id == "palette.go" {
			return exec.Command("true")
		}
		return nil
	}
	if top := ids(m.Rows("")); !strings.Contains(top, "palette palette.go") || strings.Contains(top, "board") {
		t.Errorf("with the palettes: %q", top)
	}
}

func TestLevelsAndLeaving(t *testing.T) {
	m := newMenu(ui.Context{SocketPath: "/run/herdr.sock"}, &fakeCore{})
	press(m, "w")
	if m.level != "workspace" {
		t.Fatalf("level = %q", m.level)
	}
	press(m, "k")
	if m.open != nil || !strings.Contains(m.note, "no workspace") {
		t.Fatalf("kill without a workspace: open=%v note=%q", m.open, m.note)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.level != "" || cmd != nil {
		t.Fatalf("esc in a group: level %q", m.level)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("esc at the top did not close the menu")
	}
}

// kill through the menu: the review, a wrong label, the right one.
func TestKillThroughTheMenu(t *testing.T) {
	fc := &fakeCore{}
	m := newMenu(ctxW, fc)
	press(m, "w", "k")
	if _, ok := m.open.(*killScreen); !ok || len(fc.kills) != 1 || fc.kills[0].Yes {
		t.Fatalf("kill review: open=%T kills=%+v", m.open, fc.kills)
	}
	if v := m.View(); !strings.Contains(v, `Type the workspace's label, "proj-a"`) || !strings.Contains(v, "IcyKnoll: checked") {
		t.Fatalf("review view:\n%s", v)
	}
	press(m, "proj-b", "enter")
	if len(fc.kills) != 1 || !strings.Contains(m.View(), "does not match") {
		t.Fatalf("a wrong label: kills=%+v\n%s", fc.kills, m.View())
	}
	for i := 0; i < len("proj-b"); i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	press(m, "proj-a", "enter")
	if len(fc.kills) != 2 || !fc.kills[1].Yes || fc.kills[1].OperationID != "op-1" || !strings.Contains(m.View(), "closed") {
		t.Fatalf("the kill: %+v\n%s", fc.kills, m.View())
	}
	press(m, "enter")
	if m.open != nil {
		t.Fatal("enter after the result did not close the form")
	}
}

// A refused kill shows each refusal once, on its agent's row; a label
// that cannot be typed shows that reason.
func TestKillRefusedView(t *testing.T) {
	m := newMenu(ctxW, &fakeCore{working: true})
	press(m, "w", "k")
	if v := m.View(); strings.Count(v, "it is working") != 1 || !strings.Contains(v, "kill refuses") {
		t.Fatalf("refused review:\n%s", v)
	}
	c := ctxW
	c.WorkspaceLabel = "proj\x1b[31m-a"
	m = newMenu(c, &fakeCore{})
	press(m, "w", "k")
	// The reason is wrapped, not cut: all of it shows.
	if v := m.View(); !strings.Contains(strings.Join(strings.Fields(v), " "), "cannot be typed: rename the workspace in herdr, then kill it") {
		t.Fatalf("a label with controls:\n%s", v)
	}
}

// A form's body scrolls between its title and its keys, and enter
// confirms only a review that was on screen to its end (the re-check's
// B): at 21x4 a review of 3 agents shows one line at a time.
func TestReviewSeenWholeBeforeConfirm(t *testing.T) {
	var reaps []lifecycle.ReapRequest
	m := newMenu(ctxW, &fakeCore{})
	m.env.Core.Reap = func(_ context.Context, r lifecycle.ReapRequest, _ string) ([]lifecycle.ReapItem, error) {
		reaps = append(reaps, r)
		var items []lifecycle.ReapItem
		for i, n := range []string{"IcyKnoll", "BlueLake", "RedStone"} {
			action := "would_retire"
			if !r.DryRun {
				action = "retired"
			}
			items = append(items, lifecycle.ReapItem{MailName: n, BindingID: fmt.Sprintf("b%d", i), Generation: 1, Action: action})
		}
		return items, nil
	}
	m.Update(tea.WindowSizeMsg{Width: 21, Height: 4})
	press(m, "w", "r")
	s, ok := m.open.(*reapScreen)
	if !ok || s.f.Phase() != forms.Reviewed || len(reaps) != 1 {
		t.Fatalf("reap review: %T %d", m.open, len(reaps))
	}
	v := m.View()
	if len(strings.Split(v, "\n")) > 4 || !strings.Contains(v, "↑/↓ more") {
		t.Fatalf("21x4 review:\n%s", v)
	}
	press(m, "enter")
	if len(reaps) != 1 || !strings.Contains(m.View(), "read the whole") {
		t.Fatalf("enter before the whole review: %d reaps\n%s", len(reaps), m.View())
	}
	press(m, "down", "down", "enter")
	if len(reaps) != 2 || reaps[1].DryRun || len(reaps[1].Only) != 3 {
		t.Fatalf("enter after the whole review: %+v", reaps)
	}
	// At 54x20 the review fits: enter confirms at once.
	m = newMenu(ctxW, &fakeCore{})
	m.env.Core.Reap = func(_ context.Context, r lifecycle.ReapRequest, _ string) ([]lifecycle.ReapItem, error) {
		reaps = append(reaps, r)
		return []lifecycle.ReapItem{{MailName: "IcyKnoll", BindingID: "b0", Generation: 1, Action: "would_retire"}}, nil
	}
	m.Update(tea.WindowSizeMsg{Width: 54, Height: 20})
	press(m, "w", "r", "enter")
	if len(reaps) != 4 || reaps[3].DryRun {
		t.Fatalf("a review that fits: %+v", reaps)
	}
}

// The menu's forms share one guard: a reap whose answer was lost, closed
// and opened again, finds its set held and cannot submit it again (the
// re-check's note on a guard per form).
func TestReopenedFormFindsItsRequestHeld(t *testing.T) {
	var reaps []lifecycle.ReapRequest
	m := newMenu(ctxW, &fakeCore{})
	m.env.Core.Reap = func(_ context.Context, r lifecycle.ReapRequest, _ string) ([]lifecycle.ReapItem, error) {
		reaps = append(reaps, r)
		if !r.DryRun {
			return nil, fmt.Errorf("%w: the job's answer did not come in", actions.ErrNoAnswer)
		}
		return []lifecycle.ReapItem{{MailName: "IcyKnoll", BindingID: "b0", Generation: 1, Action: "would_retire"}}, nil
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	press(m, "w", "r", "enter", "esc", "esc")
	press(m, "w", "r")
	s, ok := m.open.(*reapScreen)
	if !ok || s.f.Phase() != forms.Failed || !errors.Is(s.f.Err(), actions.ErrSubmitted) {
		t.Fatalf("the reopened reap: %T %v", m.open, s.f.Err())
	}
	press(m, "enter")
	confirms := 0
	for _, r := range reaps {
		if !r.DryRun {
			confirms++
		}
	}
	if confirms != 1 {
		t.Fatalf("%d confirms: %+v", confirms, reaps)
	}
}

// The kill's prompt and field stay under a long review, and its key line
// names how many agents it kills.
func TestKillFieldStaysInView(t *testing.T) {
	fc := &fakeCore{}
	m := newMenu(ctxW, fc)
	m.Update(tea.WindowSizeMsg{Width: 54, Height: 9})
	press(m, "w", "k")
	v := m.View()
	if len(strings.Split(v, "\n")) > 9 || !strings.Contains(v, "Type the workspace's label") || !strings.Contains(v, "▌") ||
		!strings.Contains(v, "enter: kill (1 agent(s))") {
		t.Fatalf("kill at 54x9:\n%s", v)
	}
}

// A selection handed on with the context skips the picker.
func TestWakeWithAHandedOnSelection(t *testing.T) {
	fc := &fakeCore{}
	b1 := ui.Target{BindingID: "b1", Generation: 2, PaneID: "w1:p1"}
	m := newMenu(ctxW.WithTargets("board", []ui.Target{b1}), fc)
	press(m, "w", "w")
	if _, ok := m.open.(*wakeScreen); !ok {
		t.Fatalf("open = %T, note %q", m.open, m.note)
	}
	press(m, "enter")
	if len(fc.woken) != 1 || fc.woken[0][0] != b1 || !strings.Contains(m.View(), "IcyKnoll: nudged") {
		t.Fatalf("wake: %+v\n%s", fc.woken, m.View())
	}
}

// A surface runs in the popup; the menu comes back when it ends.
func TestSurfaceComesBack(t *testing.T) {
	m := newMenu(ctxW, &fakeCore{})
	m.env.Surface = func(id string) *exec.Cmd {
		if id == "palette" {
			return exec.Command("true")
		}
		return nil
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if _, ok := m.open.(*execScreen); !ok {
		t.Fatalf("open = %T", m.open)
	}
	m.Update(surfaceEnded{id: "palette", err: fmt.Errorf("exit status 2")})
	if m.open != nil || !strings.Contains(m.note, "exit status 2") || !strings.Contains(m.View(), "palette") {
		t.Fatalf("after the palette: open=%T note=%q", m.open, m.note)
	}
}

// fakeView asks for the stop form on "s", as the ensembles view does.
type fakeView struct{}

func (fakeView) Init() tea.Cmd { return nil }
func (v fakeView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "s" {
		return v, func() tea.Msg { return views.Open{Row: "view.ensembles", Action: "stop", Arg: "run-1"} }
	}
	return v, nil
}
func (fakeView) View() string  { return "the runs" }
func (fakeView) Title() string { return "ensembles" }

// A view's form opens over the view, and the view comes back after it.
func TestViewOpensAFormAndComesBack(t *testing.T) {
	var asked []string
	m := newMenu(ctxW, &fakeCore{})
	m.env.Core.EnsembleStop = func(_ context.Context, run string, dryRun bool, opID string) (ensemble.Run, error) {
		asked = append(asked, fmt.Sprintf("%s %v %s", run, dryRun, opID))
		return ensemble.Run{ID: run, Status: ensemble.RunRunning}, nil
	}
	pump(m, m.show(&viewScreen{v: fakeView{}, open: m.openFromView}), 0)
	press(m, "s")
	if _, ok := m.open.(*confirmScreen); !ok || len(m.stack) != 1 || len(asked) != 1 || asked[0] != "run-1 true op-1" {
		t.Fatalf("after s: open=%T stack=%d asked=%v", m.open, len(m.stack), asked)
	}
	press(m, "enter")
	if len(asked) != 2 || asked[1] != "run-1 false op-1" {
		t.Fatalf("the stop: %v", asked)
	}
	press(m, "esc")
	if _, ok := m.open.(*viewScreen); !ok || len(m.stack) != 0 || !strings.Contains(m.View(), "the runs") {
		t.Fatalf("after the form: open=%T\n%s", m.open, m.View())
	}
}

// herdr sizes the menu to the popup's inside: 21 or 22 by 4 at a 40x12
// client, 54x20 at 120x40. At each size the view fits, and the cursor
// row shows for every row of every level (PearlKnoll's surfaces check).
func TestPopupSizes(t *testing.T) {
	levels := []string{""}
	for _, a := range newMenu(ctxW, &fakeCore{}).Rows("") {
		if a.Kind == actions.KindGroup {
			levels = append(levels, a.ID)
		}
	}
	for _, size := range [][2]int{{22, 4}, {21, 4}, {54, 20}, {22, 3}, {22, 2}, {22, 1}} {
		w, h := size[0], size[1]
		for _, level := range levels {
			m := newMenu(ctxW, &fakeCore{})
			m.env.Surface = func(id string) *exec.Cmd { return exec.Command("true") }
			m.Update(tea.WindowSizeMsg{Width: w, Height: h})
			m.level = level
			rows := m.Rows(level)
			for i, a := range rows {
				m.cursor = i
				v := m.View()
				lines := strings.Split(v, "\n")
				bad := len(lines) > h || !strings.Contains(v, "▶ "+a.Menu.Key)
				if h >= 3 {
					// The header and the keys stay too.
					bad = bad || !strings.HasPrefix(lines[0], "nhm") || !strings.Contains(lines[len(lines)-1], "q close")
				}
				for _, l := range lines {
					bad = bad || lipgloss.Width(l) > w
				}
				if bad {
					t.Fatalf("%dx%d, level %q, cursor on %s:\n%s", w, h, level, a.ID, v)
				}
			}
		}
	}
	// A compact menu shows its group on the header line.
	m := newMenu(ctxW, &fakeCore{})
	m.Update(tea.WindowSizeMsg{Width: 22, Height: 4})
	press(m, "x")
	if v := m.View(); !strings.HasPrefix(v, "nhm › diagnostics") {
		t.Fatalf("22x4 diagnostics:\n%s", v)
	}
	// A long label keeps a gap before its help text.
	m.Update(tea.WindowSizeMsg{Width: 54, Height: 20})
	if v := m.View(); !strings.Contains(v, "unfinished operations  operations") {
		t.Fatalf("54x20 diagnostics:\n%s", v)
	}
	// The help text wraps to the popup and scrolls to its last line; up
	// goes back to its first.
	m.Update(tea.WindowSizeMsg{Width: 22, Height: 4})
	press(m, "?")
	help := m.helpLines()
	for range help {
		press(m, "down")
	}
	v := m.View()
	for _, l := range strings.Split(v, "\n") {
		if lipgloss.Width(l) > 22 {
			t.Fatalf("22x4 help:\n%s", v)
		}
	}
	if !strings.Contains(v, strings.TrimSpace(help[len(help)-1])) || !strings.Contains(v, "esc back") || m.cursor != 0 {
		t.Fatalf("22x4 help at its end (cursor %d):\n%s", m.cursor, v)
	}
	for range help {
		press(m, "up")
	}
	if v := m.View(); !strings.Contains(v, strings.TrimSpace(help[0])) {
		t.Fatalf("22x4 help at its top:\n%s", v)
	}
	// An open screen gets the height below the header line, and the view
	// keeps the header, the selected operation and the keys.
	m = newMenu(ctxW, &fakeCore{})
	m.Update(tea.WindowSizeMsg{Width: 21, Height: 4})
	press(m, "o")
	if v := m.View(); m.screenHeight() != 3 || len(strings.Split(v, "\n")) != 4 || !strings.HasPrefix(v, "nhm") ||
		!strings.Contains(v, "▶ ") || !strings.Contains(v, "esc back") {
		t.Fatalf("21x4 operations (screen height %d):\n%s", m.screenHeight(), v)
	}
	// A screen taller than the popup keeps its top.
	if got := fit(strings.Repeat("x\n", 30)+"end", 22, 12); len(strings.Split(got, "\n")) != 12 || !strings.HasPrefix(got, "x") ||
		!strings.Contains(got, "more below") || lipgloss.Width(strings.Split(got, "\n")[11]) > 22 {
		t.Fatalf("fit = %q", got)
	}
}

// opsBase is the newest operation's time in manyOps.
var opsBase = time.Date(2026, 10, 13, 4, 0, 0, 0, time.UTC)

// manyOps has n kill records a minute apart, the newest first, and the
// newest has targets. With fail, every read after the first fails.
type manyOps struct {
	n, targets int
	reads      *int
	fail       bool
}

func (o manyOps) RecentOperations(context.Context, int) ([]lifecycle.RecentOperation, error) {
	*o.reads++
	if o.fail && *o.reads > 1 {
		return nil, errors.New("database is locked")
	}
	var out []lifecycle.RecentOperation
	for i := range o.n {
		at := opsBase.Add(-time.Duration(i) * time.Minute)
		op := lifecycle.RecentOperation{OperationID: fmt.Sprintf("op-%02d", i), Verb: lifecycle.VerbKill, Status: lifecycle.KillClosed,
			CreatedAt: at, UpdatedAt: at, Completed: &at}
		if i == 0 {
			for k := range o.targets {
				op.Targets = append(op.Targets, lifecycle.OperationTarget{Index: k, Status: "retired",
					Result: json.RawMessage(fmt.Sprintf(`{"mail_name":"Agent%02d"}`, k))})
			}
		}
		out = append(out, op)
	}
	return out, nil
}

func (manyOps) RecentJobs(int) ([]runner.Entry, error) { return nil, nil }

// Recent operations fits the popup too: the list scrolls with the
// selection, the selected row shows beside a read error, and an
// operation's page scrolls to its last target and back.
func TestOperationsFit(t *testing.T) {
	reads := 0
	m := newMenu(ctxW, &fakeCore{})
	m.env.Operations = manyOps{n: 20, targets: 30, reads: &reads, fail: true}
	m.Update(tea.WindowSizeMsg{Width: 21, Height: 4})
	press(m, "o")
	check := func(what string, w, h int, want ...string) {
		t.Helper()
		v := m.View()
		lines := strings.Split(v, "\n")
		bad := len(lines) > h
		for _, l := range lines {
			bad = bad || lipgloss.Width(l) > w
		}
		for _, s := range want {
			bad = bad || !strings.Contains(v, s)
		}
		if bad {
			t.Fatalf("%s at %dx%d:\n%s", what, w, h, v)
		}
	}
	for range 19 {
		press(m, "down")
	}
	last := "▶ " + opsBase.Add(-19*time.Minute).Local().Format("15:04:05")
	check("the last row", 21, 4, last, "esc back")
	press(m, "r")
	check("a read error", 21, 4, last, "read failed")
	m.Update(tea.WindowSizeMsg{Width: 54, Height: 20})
	for range 19 {
		press(m, "up")
	}
	press(m, "enter")
	check("op-00's page", 54, 20, "▶ "+opsBase.Local().Format("15:04:05"), "    op-00", "Agent00", "esc: the list")
	for range 40 {
		press(m, "down")
	}
	check("op-00's page, scrolled", 54, 20, "Agent29")
	for range 40 {
		press(m, "up")
	}
	check("op-00's page, back up", 54, 20, "    op-00")
	press(m, "esc")
	check("the list again", 54, 20, "enter: targets")
}

// While the help shows, a row's key does nothing; after a resize the help
// scrolls back at once.
func TestHelpKeys(t *testing.T) {
	m := newMenu(ctxW, &fakeCore{})
	m.Update(tea.WindowSizeMsg{Width: 22, Height: 4})
	press(m, "?", "enter", "x")
	if m.open != nil || m.level != "" || !m.help {
		t.Fatalf("help: open %T, level %q, help %v", m.open, m.level, m.help)
	}
	for range m.helpLines() {
		press(m, "down")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 4})
	if m.helpTop != m.helpMax() {
		t.Fatalf("after a resize: help top %d, max %d", m.helpTop, m.helpMax())
	}
	before := m.helpTop
	press(m, "up")
	if m.helpTop != before-1 {
		t.Fatalf("up after a resize: help top %d, want %d", m.helpTop, before-1)
	}
}

// workspace › focus: down and enter on a list that is loading or empty
// do nothing (the review's M1: the cursor went to -1, and enter
// panicked); a long list scrolls with the cursor.
func TestWorkspacesScreen(t *testing.T) {
	a, _ := actions.ByID("workspace.attach")
	s := newWorkspacesScreen(a, (&fakeCore{}).core(), ctxW, nil)
	keys := func(ks ...tea.KeyType) tea.Cmd {
		var cmd tea.Cmd
		for _, k := range ks {
			cmd, _ = s.Update(tea.KeyMsg{Type: k})
		}
		return cmd
	}
	if cmd := keys(tea.KeyDown, tea.KeyEnter); cmd != nil || s.cursor != 0 {
		t.Fatalf("loading: cursor %d, enter gave a command", s.cursor)
	}
	s.Update(workspacesRead{request: s.request})
	if cmd := keys(tea.KeyDown, tea.KeyDown, tea.KeyEnter); cmd != nil || s.cursor != 0 ||
		!strings.Contains(s.View(54, 18), "herdr has no workspace") {
		t.Fatalf("empty: cursor %d, enter gave a command\n%s", s.cursor, s.View(54, 18))
	}
	var list []snapshot.Workspace
	for i := range 30 {
		list = append(list, snapshot.Workspace{ID: fmt.Sprintf("w%d", i+1), Label: fmt.Sprintf("ws%02d", i+1)})
	}
	s.Update(workspacesRead{request: s.request, list: list})
	for range 40 {
		keys(tea.KeyDown)
	}
	v := s.View(21, 3)
	if s.cursor != 29 || len(strings.Split(v, "\n")) > 3 || !strings.Contains(v, "▶ ws30") {
		t.Fatalf("30 workspaces at 21x3, cursor %d:\n%s", s.cursor, v)
	}
	if cmd := keys(tea.KeyEnter); cmd == nil {
		t.Fatal("enter on ws30 gave no command")
	}
}

// Outside text reaches the screen without terminal controls: a review's
// reason, a header label, an error (the review's L2).
func TestClipCleans(t *testing.T) {
	if got := clip("BlueLake: \x1b]0;pwned\x07it is \x1b[31mworking\u009b", 40); got != "BlueLake: it is working" {
		t.Fatalf("clip = %q", got)
	}
	// One line, in columns: a newline or a tab is a space, a wide
	// character takes two columns (the re-check's lows 5 and 8).
	if got := clip("proj\nevil\tx", 40); got != "proj evil x" {
		t.Fatalf("clip of a newline = %q", got)
	}
	if got := clip("ワークスペース", 7); lipgloss.Width(got) > 7 || !strings.HasSuffix(got, "…") {
		t.Fatalf("clip of wide characters = %q (%d columns)", got, lipgloss.Width(got))
	}
	// A last newline is no line of its own (the re-check's low 6).
	if got := fit("a\nb\nc\n", 20, 3); got != "a\nb\nc" {
		t.Fatalf("fit = %q", got)
	}
	c := ctxW
	c.WorkspaceLabel = "proj\x1b[2J-a"
	m := newMenu(c, &fakeCore{})
	if v := m.View(); strings.Contains(v, "\x1b[2J") {
		t.Fatalf("the header shows a control sequence: %q", v)
	}
	text := targetsText(actions.Result{Lines: []string{"its mail cannot be read: \x1b[2Jboom"},
		Targets: []actions.TargetResult{{Name: "Icy\x1b[1mKnoll", Status: "refused", Reason: "\x07it is working"}}}, 60)
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Fatalf("targets text = %q", text)
	}
}

// The menu looks for each surface once, not on every render (the
// review's L5); it builds the command again only to run it, and a surface
// gone by then is a note.
func TestSurfaceLookedUpOnce(t *testing.T) {
	m := newMenu(ctxW, &fakeCore{})
	calls, there := 0, true
	m.env.Surface = func(id string) *exec.Cmd {
		calls++
		if !there {
			return nil
		}
		return exec.Command("true")
	}
	for range 5 {
		_ = m.View()
		press(m, "down")
	}
	looked := calls
	if looked == 0 || looked > 4 {
		t.Fatalf("%d lookups for 5 renders", looked)
	}
	there = false
	press(m, "a")
	if m.open != nil || !strings.Contains(m.note, "not available") || calls != looked+1 {
		t.Fatalf("a gone surface: open %T, note %q, %d lookups", m.open, m.note, calls)
	}
}

func TestWindow(t *testing.T) {
	lines := []string{"0", "1", "2", "3", "4", "5"}
	for _, c := range []struct {
		first, last, n int
		want           string
	}{
		{0, 0, 3, "012"}, // at the top
		{4, 4, 3, "234"}, // the cursor at the bottom
		{2, 4, 3, "234"}, // a row and its details
		{1, 5, 3, "123"}, // too long: its first line stays
		{0, 0, 9, "012345"},
		{3, 3, 0, "3"}, // at least the cursor
	} {
		if got := strings.Join(window(lines, c.first, c.last, c.n), ""); got != c.want {
			t.Errorf("window(%d, %d, %d) = %q, want %q", c.first, c.last, c.n, got, c.want)
		}
	}
	if got := window(nil, 0, 0, 3); len(got) != 0 {
		t.Errorf("window(nil) = %q", got)
	}
}

func TestRecentOperations(t *testing.T) {
	m := newMenu(ctxW, &fakeCore{})
	press(m, "o")
	if _, ok := m.open.(*opsScreen); !ok || !strings.Contains(m.View(), "kill_workspace") {
		t.Fatalf("operations: %T\n%s", m.open, m.View())
	}
	press(m, "esc")
	if m.open != nil {
		t.Fatal("esc did not close recent operations")
	}
}
