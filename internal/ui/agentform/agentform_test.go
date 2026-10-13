package agentform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

// fakeCore records what the forms asked the core.
type fakeCore struct {
	mu         sync.Mutex
	interrupts []map[string]int64
	interOps   []string
	interErr   error
	retires    []lifecycle.RetireRequest
	refuse     bool
	recycles   []lifecycle.RecycleRequest
}

func (f *fakeCore) core() actions.Core {
	return actions.Core{
		Interrupt: func(_ context.Context, expect map[string]int64, opID string) (wait.InterruptResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.interrupts, f.interOps = append(f.interrupts, expect), append(f.interOps, opID)
			if f.interErr != nil {
				return wait.InterruptResult{}, f.interErr
			}
			res := wait.InterruptResult{OperationID: opID, Status: "sent"}
			for id := range expect {
				res.Targets = append(res.Targets, wait.InterruptTarget{BindingID: id, MailName: "Name-" + id, Status: wait.InterruptSent})
			}
			return res, nil
		},
		Retire: func(_ context.Context, req lifecycle.RetireRequest) (lifecycle.RetireResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.retires = append(f.retires, req)
			res := lifecycle.RetireResult{MailName: "IcyKnoll", DryRun: req.DryRun, OperationID: req.OperationID,
				Actions: []string{"close pane w1:p1"}}
			switch {
			case f.refuse:
				res.Refusals = []string{"it has a bead in progress"}
				return res, lifecycle.ErrRefused
			case !req.DryRun:
				res.Done = true
			}
			return res, nil
		},
		Recycle: func(_ context.Context, req lifecycle.RecycleRequest) (lifecycle.RecycleResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.recycles = append(f.recycles, req)
			return lifecycle.RecycleResult{OperationID: req.OperationID, MailName: "IcyKnoll", Stage: "done"}, nil
		},
	}
}

func newGuard() *actions.Guard {
	n := 0
	return actions.NewGuard(func() (string, error) {
		n++
		return "op-" + string(rune('0'+n)), nil
	})
}

var (
	ctx0 = ui.Context{Version: ui.ContextVersion, WorkspaceID: "w1", SocketPath: "/run/herdr.sock"}
	icy  = ui.Target{BindingID: "bnd-1", Generation: 3, PaneID: "w1:p1"}
	blue = ui.Target{BindingID: "bnd-2", Generation: 1, PaneID: "w1:p2"}
)

func action(t *testing.T, id string) actions.Action {
	t.Helper()
	a, ok := actions.ByID(id)
	if !ok {
		t.Fatalf("no action %s", id)
	}
	return a
}

func keys(f *Form, ks ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range ks {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		if cmd := f.Update(msg); cmd != nil {
			last = cmd
		}
	}
	return last
}

// run runs a command and hands its message back to the form.
func run(f *Form, cmd tea.Cmd) {
	if cmd != nil {
		f.Update(cmd())
	}
}

// Confirm starts on cancel: enter there closes the form and asks the
// core nothing.
func TestConfirmStartsOnCancel(t *testing.T) {
	fc := &fakeCore{}
	f, err := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, []ui.Target{icy, blue}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.View(80), "[ cancel ]") {
		t.Fatalf("the focus is not on cancel:\n%s", f.View(80))
	}
	if cmd := keys(f, "enter"); cmd != nil || !f.Done() || len(fc.interrupts) != 0 {
		t.Fatalf("enter on cancel: done %v, %d interrupts", f.Done(), len(fc.interrupts))
	}
}

// One submission: the frozen targets and their generations go to the
// core with the guard's id; a second enter while it runs submits nothing.
func TestInterruptSubmitsOnce(t *testing.T) {
	fc := &fakeCore{}
	f, err := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, []ui.Target{icy, blue},
		map[string]string{icy.Key(): "IcyKnoll", blue.Key(): "BlueLake"})
	if err != nil {
		t.Fatal(err)
	}
	if v := f.View(80); !strings.Contains(v, "IcyKnoll  (generation 3)") || !strings.Contains(v, "BlueLake") {
		t.Fatalf("review:\n%s", v)
	}
	cmd := keys(f, "right", "enter")
	if cmd == nil || !f.Running() {
		t.Fatal("confirm started nothing")
	}
	if again := keys(f, "enter", "enter"); again != nil {
		t.Fatal("a second enter submitted again")
	}
	run(f, cmd)
	if len(fc.interrupts) != 1 || fc.interrupts[0]["bnd-1"] != 3 || fc.interrupts[0]["bnd-2"] != 1 || fc.interOps[0] != "op-1" {
		t.Fatalf("interrupts = %v, ops %v", fc.interrupts, fc.interOps)
	}
	if v := f.View(80); !strings.Contains(v, "sent (op-1)") || !strings.Contains(v, "Name-bnd-1: sent") {
		t.Fatalf("result:\n%s", v)
	}
	keys(f, "x")
	if !f.Done() {
		t.Fatal("a key did not close the result")
	}
}

// No answer from the core is "unknown", with the operation id; the form
// does not submit again.
func TestLostAnswerIsUnknown(t *testing.T) {
	fc := &fakeCore{interErr: errors.New("runner: the job's result did not come")}
	f, _ := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil)
	run(f, keys(f, "right", "enter"))
	if v := f.View(80); !strings.Contains(v, "unknown:") || !strings.Contains(v, "op-1") || !strings.Contains(v, "does not submit it again") {
		t.Fatalf("view:\n%s", v)
	}
	if len(fc.interrupts) != 1 {
		t.Fatalf("%d interrupts", len(fc.interrupts))
	}
}

// Retire runs its checks under the review's own id with a dry run, then
// retires that generation with the same id; a refusal blocks the confirm.
func TestRetireChecksFirst(t *testing.T) {
	fc := &fakeCore{}
	f, err := New(action(t, "agent.retire"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := f.View(80); !strings.Contains(v, "retire's checks") {
		t.Fatalf("view:\n%s", v)
	}
	run(f, f.Init())
	if len(fc.retires) != 1 || !fc.retires[0].DryRun || fc.retires[0].OperationID != "" {
		t.Fatalf("the check = %+v", fc.retires)
	}
	if v := f.View(80); !strings.Contains(v, "would close pane w1:p1") {
		t.Fatalf("checks not shown:\n%s", v)
	}
	run(f, keys(f, "tab", "tab", "tab", "enter"))
	if len(fc.retires) != 2 {
		t.Fatalf("retires = %+v", fc.retires)
	}
	r := fc.retires[1]
	if r.DryRun || r.Target != "bnd-1" || r.ExpectedGeneration != 3 || r.OperationID != "op-1" {
		t.Fatalf("retire = %+v", r)
	}
	if v := f.View(80); !strings.Contains(v, "done") || !strings.Contains(v, "retired") {
		t.Fatalf("result:\n%s", v)
	}
}

func TestRetireRefusedCannotConfirm(t *testing.T) {
	fc := &fakeCore{refuse: true}
	f, _ := New(action(t, "agent.retire"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil)
	run(f, f.Init())
	if cmd := keys(f, "tab", "tab", "tab", "enter"); cmd != nil {
		t.Fatal("a refused retire was submitted")
	}
	if v := f.View(80); !strings.Contains(v, "bead in progress") || !strings.Contains(v, "checks refuse") {
		t.Fatalf("view:\n%s", v)
	}
	if len(fc.retires) != 1 {
		t.Fatalf("retires = %d", len(fc.retires))
	}
}

// Changing an option runs the checks again; a stale check's answer is
// dropped, and a submit before the new answer waits.
func TestRetireOptionRechecks(t *testing.T) {
	fc := &fakeCore{}
	f, _ := New(action(t, "agent.retire"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil)
	first := f.Init()
	keys(f, "up", "up") // focus the first option
	second := keys(f, "space")
	if second == nil {
		t.Fatal("a toggle did not run the checks again")
	}
	run(f, first) // the stale answer
	if !f.checking {
		t.Fatal("a stale check was taken")
	}
	if cmd := keys(f, "tab", "tab", "tab", "enter"); cmd != nil || !strings.Contains(f.View(80), "still running") {
		t.Fatalf("submitted before the check:\n%s", f.View(80))
	}
	run(f, second)
	run(f, keys(f, "enter"))
	last := fc.retires[len(fc.retires)-1]
	if last.DryRun || !last.KeepIdentity {
		t.Fatalf("retire = %+v", last)
	}
}

func TestRecycleTakesItsKey(t *testing.T) {
	fc := &fakeCore{}
	f, _ := New(action(t, "agent.recycle"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil)
	keys(f, "up", "up", "f", "r", "e", "s", "h")
	run(f, keys(f, "tab", "tab", "tab", "enter"))
	if len(fc.recycles) != 1 || fc.recycles[0].Template != "fresh" || fc.recycles[0].ExpectedGeneration != 3 ||
		fc.recycles[0].OperationID == "" {
		t.Fatalf("recycles = %+v", fc.recycles)
	}
}

// New refuses what the action does not take, and an action the core
// cannot run here; the upgrade freeze refuses the submission.
func TestRefusals(t *testing.T) {
	fc := &fakeCore{}
	if _, err := New(action(t, "agent.retire"), fc.core(), newGuard(), ctx0, []ui.Target{icy, blue}, nil); err == nil {
		t.Error("retire took two targets")
	}
	if _, err := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, []ui.Target{{PaneID: "w1:p3"}}, nil); err == nil {
		t.Error("interrupt took an unbound agent")
	}
	if _, err := New(action(t, "agent.interrupt"), actions.Core{}, newGuard(), ctx0, []ui.Target{icy}, nil); !errors.Is(err, actions.ErrUnavailable) {
		t.Errorf("no core: %v", err)
	}
	if _, err := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, nil, nil); err == nil {
		t.Error("an empty selection was taken")
	}
	// send needs the palette's composer, not this form.
	if _, err := New(action(t, "send"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil); err == nil {
		t.Error("the form took send")
	}
	frozen := errors.New("nhm was upgraded on disk")
	f, _ := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, []ui.Target{icy}, nil, WithFrozen(func() error { return frozen }))
	if cmd := keys(f, "right", "enter"); cmd != nil || len(fc.interrupts) != 0 || !strings.Contains(f.View(80), "upgraded") {
		t.Fatalf("a frozen form submitted:\n%s", f.View(80))
	}
}

// A name with terminal controls is shown without them.
func TestNamesAreCleaned(t *testing.T) {
	fc := &fakeCore{}
	f, _ := New(action(t, "agent.interrupt"), fc.core(), newGuard(), ctx0, []ui.Target{icy},
		map[string]string{icy.Key(): "Icy\x1b[31mKnoll\x1b]0;evil\x07"})
	if v := f.View(80); strings.Contains(v, "\x1b") || strings.Contains(v, "evil") || !strings.Contains(v, "IcyKnoll") {
		t.Fatalf("view: %q", v)
	}
}
