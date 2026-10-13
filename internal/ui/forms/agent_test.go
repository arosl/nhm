package forms

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
)

var (
	agentCtx = ui.Context{Version: ui.ContextVersion, SocketPath: "/s", WorkspaceID: "w1", WorkspaceLabel: "swarm", Origin: "board"}
	tBlue    = ui.Target{BindingID: "bnd-1", Generation: 3, PaneID: "w1:p1", Kind: "claude"}
	tRed     = ui.Target{BindingID: "bnd-2", Generation: 1, PaneID: "w1:p2", Kind: "codex"}
	tLoose   = ui.Target{PaneID: "w1:p5", Kind: "claude", Session: "id:s-5"}
	names    = map[string]string{tBlue.Key(): "BlueLake", tRed.Key(): "RedFox", tLoose.Key(): "loose"}
)

// agentCore records the core calls.
type agentCore struct {
	interrupts []string
	interrupt  wait.InterruptResult
	retires    []lifecycle.RetireRequest
	retireDry  lifecycle.RetireResult
	retire     lifecycle.RetireResult
	recycles   []lifecycle.RecycleRequest
	adopts     []bindings.AdoptRequest
	err        error
}

func (c *agentCore) core() actions.Core {
	return actions.Core{
		Interrupt: func(_ context.Context, e map[string]int64, op string) (wait.InterruptResult, error) {
			c.interrupts = append(c.interrupts, fmt.Sprintf("%s %v", op, e))
			r := c.interrupt
			r.OperationID = op
			return r, c.err
		},
		Retire: func(_ context.Context, r lifecycle.RetireRequest) (lifecycle.RetireResult, error) {
			c.retires = append(c.retires, r)
			if r.DryRun {
				return c.retireDry, nil
			}
			res := c.retire
			res.OperationID = r.OperationID
			return res, c.err
		},
		Recycle: func(_ context.Context, r lifecycle.RecycleRequest) (lifecycle.RecycleResult, error) {
			c.recycles = append(c.recycles, r)
			return lifecycle.RecycleResult{OperationID: r.OperationID, MailName: "BlueLake", Stage: lifecycle.StageDone}, c.err
		},
		Adopt: func(_ context.Context, r bindings.AdoptRequest) (bindings.AdoptResult, error) {
			c.adopts = append(c.adopts, r)
			return bindings.AdoptResult{OperationID: r.OperationID, Created: true,
				Binding: bindings.Binding{ID: "bnd-9", MailName: r.MailName, PaneID: r.PaneID}}, c.err
		},
		SendBase: func(context.Context) (send.Request, error) { return send.Request{}, nil },
		Send:     func(context.Context, send.Request) (send.Result, error) { return send.Result{}, nil },
	}
}

func newGuard() *actions.Guard {
	n := 0
	return actions.NewGuard(func() (string, error) { n++; return fmt.Sprintf("op-%d", n), nil })
}

func agentForm(t *testing.T, id string, core actions.Core, guard *actions.Guard, ts ...ui.Target) *AgentForm {
	t.Helper()
	a, ok := actions.ByID(id)
	if !ok {
		t.Fatalf("no action %s", id)
	}
	f, err := NewAgentForm(a, core, guard, agentCtx, ts, names)
	if err != nil {
		t.Fatalf("NewAgentForm(%s): %v", id, err)
	}
	f.step(f.Init())
	return f
}

// step runs a command and feeds the form's own answers back, until none
// is left.
func (f *AgentForm) step(cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		switch msg.(type) {
		case agentReviewed, agentRan:
			cmd = f.Update(msg)
		default:
			return
		}
	}
}

// press sends keys: "enter", "tab", "esc", "space", or text to type.
func (f *AgentForm) press(keys ...string) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		f.step(f.Update(msg))
	}
}

// TestInterruptConfirmDefaultsToCancel: the review lists the frozen
// targets; enter on the default cancels and calls nothing; tab, then
// enter, submits once, and each target's outcome is shown.
func TestInterruptConfirmDefaultsToCancel(t *testing.T) {
	c := &agentCore{interrupt: wait.InterruptResult{Status: "done", Targets: []wait.InterruptTarget{
		{BindingID: "bnd-1", MailName: "BlueLake", Status: wait.InterruptSent, Keys: []string{"esc"}},
		{BindingID: "bnd-2", MailName: "RedFox", Status: wait.InterruptSent, Keys: []string{"esc"}}}}}
	guard := newGuard()
	f := agentForm(t, "agent.interrupt", c.core(), guard, tBlue, tRed)
	v := f.View(80)
	if f.Phase() != Reviewed || !strings.Contains(v, "interrupt 2 agents · workspace swarm") ||
		!strings.Contains(v, "BlueLake  (generation 3, pane w1:p1)") || !strings.Contains(v, "[ cancel ]") {
		t.Fatalf("review frame:\n%s", v)
	}
	f.press("enter")
	if !f.Done() || len(c.interrupts) != 0 {
		t.Fatalf("enter on cancel: done %v, calls %v", f.Done(), c.interrupts)
	}
	f = agentForm(t, "agent.interrupt", c.core(), guard, tBlue, tRed)
	f.press("tab")
	if !strings.Contains(f.View(80), "[ confirm ]") {
		t.Fatalf("confirm frame:\n%s", f.View(80))
	}
	// The review was not submitted, so the same request keeps its id.
	f.press("enter")
	if f.Phase() != Done || len(c.interrupts) != 1 || c.interrupts[0] != "op-1 map[bnd-1:3 bnd-2:1]" {
		t.Fatalf("phase %s, calls %v", f.Phase(), c.interrupts)
	}
	v = f.View(80)
	if !strings.Contains(v, "interrupt: done · operation op-1") || !strings.Contains(v, "RedFox: sent: esc") {
		t.Fatalf("result frame:\n%s", v)
	}
	f.press("enter")
	if !f.Done() {
		t.Fatal("enter on the result does not close")
	}
}

// TestNoDoubleSubmit: two shortcuts to the same reviewed request (the
// board and the menu, on one guard) submit once.
func TestNoDoubleSubmit(t *testing.T) {
	c := &agentCore{interrupt: wait.InterruptResult{Status: "done"}}
	guard := newGuard()
	one := agentForm(t, "agent.interrupt", c.core(), guard, tBlue)
	two := agentForm(t, "agent.interrupt", c.core(), guard, tBlue)
	one.press("tab", "enter")
	two.press("tab", "enter")
	if len(c.interrupts) != 1 || two.Phase() == Running || !strings.Contains(two.View(80), "already submitted") {
		t.Fatalf("calls %v, second form %s:\n%s", c.interrupts, two.Phase(), two.View(80))
	}
	// Keys after the submission change nothing.
	one.press("tab", "enter")
	if len(c.interrupts) != 1 {
		t.Fatalf("calls %v", c.interrupts)
	}
}

// TestStaleGenerationShownPerTarget: the core refuses a target whose
// binding moved on; the form shows that target's refusal in the core's
// words, next to the one that went through.
func TestStaleGenerationShownPerTarget(t *testing.T) {
	c := &agentCore{interrupt: wait.InterruptResult{Status: "partial", Targets: []wait.InterruptTarget{
		{BindingID: "bnd-1", MailName: "BlueLake", Status: wait.InterruptSent, Keys: []string{"esc"}},
		{BindingID: "bnd-2", MailName: "RedFox", Status: wait.InterruptRefused,
			Reason: "RedFox changed since it was reviewed: expected generation 1, the binding is at generation 2"}}}}
	f := agentForm(t, "agent.interrupt", c.core(), newGuard(), tBlue, tRed)
	f.press("tab", "enter")
	v := f.View(120)
	if f.Phase() != Done || !strings.Contains(v, "interrupt: partial") ||
		!strings.Contains(v, "RedFox: refused: RedFox changed since it was reviewed: expected generation 1, the binding is at generation 2") ||
		!strings.Contains(v, "BlueLake: sent: esc") {
		t.Fatalf("result frame:\n%s", v)
	}
}

// TestRetireReviewsWithADryRun: the fields, then retire's checks as a dry
// run under the review's id (nothing claimed), then the same id at the
// confirm; an edit after the review needs a new one.
func TestRetireReviewsWithADryRun(t *testing.T) {
	c := &agentCore{retireDry: lifecycle.RetireResult{MailName: "BlueLake", DryRun: true, Actions: []string{"close pane w1:p1"}},
		retire: lifecycle.RetireResult{MailName: "BlueLake", Done: true, Actions: []string{"closed pane w1:p1"}}}
	f := agentForm(t, "agent.retire", c.core(), newGuard(), tBlue)
	if f.Phase() != Editing || !strings.Contains(f.View(80), "> keep the Agent Mail identity: [ ]") {
		t.Fatalf("fields frame:\n%s", f.View(80))
	}
	f.press("tab", "space", "enter")
	dry := c.retires[0]
	if f.Phase() != Reviewed || !dry.DryRun || dry.OperationID != "" || !dry.NoBeads || dry.Target != "bnd-1" || dry.ExpectedGeneration != 3 {
		t.Fatalf("phase %s, dry run %+v", f.Phase(), dry)
	}
	if v := f.View(80); !strings.Contains(v, "retire will:") || !strings.Contains(v, "close pane w1:p1") {
		t.Fatalf("review frame:\n%s", v)
	}
	reviewed := f.OperationID()
	f.press("tab", "enter")
	real := c.retires[1]
	if f.Phase() != Done || real.DryRun || real.OperationID != reviewed || !real.NoBeads {
		t.Fatalf("phase %s, retire %+v (review id %s)", f.Phase(), real, reviewed)
	}
	if v := f.View(80); !strings.Contains(v, "BlueLake: retired") || !strings.Contains(v, "closed pane w1:p1") {
		t.Fatalf("result frame:\n%s", v)
	}
}

func TestRetireRefusedAtReview(t *testing.T) {
	c := &agentCore{retireDry: lifecycle.RetireResult{MailName: "BlueLake", DryRun: true,
		Refusals: []string{"BlueLake is working; retire it once its turn is done"}}}
	f := agentForm(t, "agent.retire", c.core(), newGuard(), tBlue)
	f.press("enter")
	v := f.View(80)
	if f.Phase() != Refused || !strings.Contains(v, "retire refuses:") || !strings.Contains(v, "once its turn is done") ||
		strings.Contains(v, "confirm") {
		t.Fatalf("refused frame (%s):\n%s", f.Phase(), v)
	}
	f.press("esc")
	if f.Phase() != Editing || len(c.retires) != 1 {
		t.Fatalf("back: %s, calls %d", f.Phase(), len(c.retires))
	}
}

func TestRecycleFields(t *testing.T) {
	c := &agentCore{}
	f := agentForm(t, "agent.recycle", c.core(), newGuard(), tBlue)
	f.press("fresh_review", "tab", "go on", "enter")
	if f.Phase() != Editing || !strings.Contains(f.View(80), "not both") || len(c.recycles) != 0 {
		t.Fatalf("template and prompt: %s\n%s", f.Phase(), f.View(80))
	}
	f = agentForm(t, "agent.recycle", c.core(), newGuard(), tBlue)
	f.press("fresh_review", "tab", "tab", "space", "enter", "tab", "enter")
	r := c.recycles[0]
	if f.Phase() != Done || r.Template != "fresh_review" || r.Prompt != "" || !r.NoHandoff || r.Target != "bnd-1" || r.ExpectedGeneration != 3 {
		t.Fatalf("phase %s, recycle %+v", f.Phase(), r)
	}
}

// TestAdoptForm: adopt takes the unbound agent, needs a name, and passes
// the pane and session the review saw; the core's refusal of another
// agent in the pane is shown.
func TestAdoptForm(t *testing.T) {
	c := &agentCore{}
	f := agentForm(t, "agent.adopt", c.core(), newGuard(), tLoose)
	if !strings.Contains(f.View(80), "loose  (no binding, claude agent in pane w1:p5)") {
		t.Fatalf("frame:\n%s", f.View(80))
	}
	f.press("enter")
	if f.Phase() != Editing || !strings.Contains(f.View(80), "give the Agent Mail name") {
		t.Fatalf("no name: %s", f.Phase())
	}
	f.press("GreenHill", "enter", "tab", "enter")
	a := c.adopts[0]
	if f.Phase() != Done || a.MailName != "GreenHill" || a.PaneID != "w1:p5" || a.ExpectedSession != "id:s-5" || a.Kind != "claude" {
		t.Fatalf("phase %s, adopt %+v", f.Phase(), a)
	}
	c.err = fmt.Errorf("%w: the agent in w1:p5 has session id:s-6, the review saw id:s-5", bindings.ErrAgentChanged)
	f = agentForm(t, "agent.adopt", c.core(), newGuard(), tLoose)
	f.press("GreenHill", "enter", "tab", "enter")
	if f.Phase() != Failed || !strings.Contains(f.View(120), "refused: bindings: the agent in the pane is not the one reviewed") {
		t.Fatalf("changed agent (%s):\n%s", f.Phase(), f.View(120))
	}
}

// TestNoAnswerIsUnknown: an answer that never came is "unknown", with the
// way to find it; closing a running form leaves the job running.
func TestNoAnswerIsUnknown(t *testing.T) {
	c := &agentCore{err: context.DeadlineExceeded}
	f := agentForm(t, "agent.interrupt", c.core(), newGuard(), tBlue)
	f.press("tab", "enter")
	if v := f.View(80); f.Phase() != Failed || !strings.Contains(v, "unknown; see recent operations") {
		t.Fatalf("no answer (%s):\n%s", f.Phase(), v)
	}
	g := agentForm(t, "agent.interrupt", c.core(), newGuard(), tBlue)
	g.press("tab")
	cmd := g.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if g.Phase() != Running || !strings.Contains(g.View(80), "closing this leaves it running") {
		t.Fatalf("running frame:\n%s", g.View(80))
	}
	g.press("esc")
	if !g.Done() {
		t.Fatal("esc does not close a running form")
	}
	// The answer that comes after the close changes nothing.
	g.Update(cmd())
	if g.Phase() != Running {
		t.Fatalf("a closed form took an answer: %s", g.Phase())
	}
}

func TestNewAgentFormRefuses(t *testing.T) {
	c := &agentCore{}
	retire, _ := actions.ByID("agent.retire")
	adopt, _ := actions.ByID("agent.adopt")
	send, _ := actions.ByID("send")
	for name, tc := range map[string]struct {
		a    actions.Action
		core actions.Core
		ts   []ui.Target
		want error
	}{
		"not an agent form": {send, c.core(), []ui.Target{tBlue}, nil},
		"two to retire":     {retire, c.core(), []ui.Target{tBlue, tRed}, actions.ErrTargets},
		"none":              {retire, c.core(), nil, ui.ErrEmptySelection},
		"adopt a bound one": {adopt, c.core(), []ui.Target{tBlue}, actions.ErrTargets},
		"no core seam":      {retire, actions.Core{}, []ui.Target{tBlue}, actions.ErrUnavailable},
	} {
		_, err := NewAgentForm(tc.a, tc.core, newGuard(), agentCtx, tc.ts, names)
		if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewAgentForm(retire, c.core(), nil, agentCtx, []ui.Target{tBlue}, names); err == nil {
		t.Error("no guard: accepted")
	}
}

func TestHostileNamesAreCleaned(t *testing.T) {
	c := &agentCore{interrupt: wait.InterruptResult{Status: "done", Targets: []wait.InterruptTarget{
		{BindingID: "bnd-1", MailName: "Blue\x1b[2J", Status: "refused", Reason: "x\x1b]0;owned\x07y"}}}}
	evil := map[string]string{tBlue.Key(): "Blue\x1b]0;t\x07Lake"}
	a, _ := actions.ByID("agent.interrupt")
	f, err := NewAgentForm(a, c.core(), newGuard(), agentCtx, []ui.Target{tBlue}, evil)
	if err != nil {
		t.Fatal(err)
	}
	f.step(f.Init())
	f.press("tab", "enter")
	if v := f.View(80); strings.ContainsAny(v, "\x1b\x07") || !strings.Contains(v, "BlueLake") || !strings.Contains(v, "refused: xy") {
		t.Fatalf("frame keeps controls: %q", v)
	}
}
