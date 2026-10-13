package menu

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/ensemble"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
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
}

func (f *fakeCore) core() actions.Core {
	return actions.Core{
		Kill: func(_ context.Context, r lifecycle.KillRequest) (lifecycle.KillResult, error) {
			f.kills = append(f.kills, r)
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
