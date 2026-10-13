package actions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/store/history"
	"github.com/arosl/nhm/internal/ui"
)

func TestTableIsWellFormed(t *testing.T) {
	ids := map[string]Action{}
	for _, a := range table {
		if a.ID == "" || a.Label == "" || a.Help == "" || a.Kind == "" {
			t.Errorf("%q: ID, label, help and kind are required", a.ID)
		}
		if _, dup := ids[a.ID]; dup {
			t.Errorf("%s: two rows", a.ID)
		}
		ids[a.ID] = a
		if a.Kind == KindGroup {
			if a.Menu.Parent != "" || a.run != nil || len(a.Covers) > 0 {
				t.Errorf("%s: a group sits at the top level, runs nothing and covers nothing", a.ID)
			}
			continue
		}
		if a.Scope == "" || len(a.Covers) == 0 {
			t.Errorf("%s: scope and covered leaves are required", a.ID)
		}
		agents := a.Scope == ScopeAgents
		if agents != (a.Selection == SelectOne || a.Selection == SelectMany) {
			t.Errorf("%s: an agent action, and only one, takes a selection (scope %s, selection %q)", a.ID, a.Scope, a.Selection)
		}
		if a.Takes != TakesBound && !agents {
			t.Errorf("%s: Takes is for agent actions", a.ID)
		}
		if a.run != nil && a.Kind == KindSurface {
			t.Errorf("%s: a surface has no handler; the surface code starts it", a.ID)
		}
		if a.Menu.Key == "" && a.Board.Space == "" && len(a.Board.Keys) == 0 {
			t.Errorf("%s: no surface shows it", a.ID)
		}
	}
	// At most two menu levels: a row's parent is a top-level group.
	for _, a := range table {
		if p := a.Menu.Parent; p != "" {
			if g, ok := ids[p]; !ok || g.Kind != KindGroup {
				t.Errorf("%s: parent %q is not a group", a.ID, p)
			}
		}
	}
}

// TestMenuKeysPerLevel: in each menu level a key picks one row, and no
// row takes a key the level keeps for itself.
func TestMenuKeysPerLevel(t *testing.T) {
	levels := map[string]map[string]string{}
	for _, a := range table {
		if a.Menu.Key == "" {
			continue
		}
		l := levels[a.Menu.Parent]
		if l == nil {
			l = map[string]string{}
			levels[a.Menu.Parent] = l
		}
		if other, ok := l[a.Menu.Key]; ok {
			t.Errorf("menu level %q: key %q picks %s and %s", a.Menu.Parent, a.Menu.Key, other, a.ID)
		}
		l[a.Menu.Key] = a.ID
		if slices.Contains(MenuReserved, a.Menu.Key) {
			t.Errorf("%s: menu key %q is reserved", a.ID, a.Menu.Key)
		}
	}
}

// TestBoardKeysKeepNTMMeaning: a board key does one thing; a key that
// ntm's dashboard has keeps ntm's meaning; the action menu's letters are
// unique and leave its own keys free.
func TestBoardKeysKeepNTMMeaning(t *testing.T) {
	board, space := map[string]string{}, map[string]string{}
	for _, a := range table {
		for _, k := range a.Board.Keys {
			if other, ok := board[k]; ok {
				t.Errorf("board key %q: %s and %s", k, other, a.ID)
			}
			board[k] = a.ID
			if ntm, ok := NTMBoardKeys[k]; ok && ntm != ntmMeaning[a.ID] {
				t.Errorf("%s: board key %q is ntm's %s", a.ID, k, ntm)
			}
			if slices.Contains(BoardReserved, k) {
				t.Errorf("%s: board key %q is the board's own", a.ID, k)
			}
		}
		if s := a.Board.Space; s != "" {
			if other, ok := space[s]; ok {
				t.Errorf("action menu key %q: %s and %s", s, other, a.ID)
			}
			space[s] = a.ID
			if slices.Contains(SpaceReserved, s) {
				t.Errorf("%s: action menu key %q is reserved", a.ID, s)
			}
		}
	}
	// Each ntm meaning the table claims is on all of ntm's keys for it.
	for id, meaning := range ntmMeaning {
		a, ok := ByID(id)
		if !ok {
			t.Fatalf("ntmMeaning names %s, which is not in the table", id)
		}
		for k, m := range NTMBoardKeys {
			if m == meaning && !slices.Contains(a.Board.Keys, k) {
				t.Errorf("%s stands for ntm's %s but lacks its key %q", id, meaning, k)
			}
		}
	}
}

// mvpLeaves are the MVP rows of plan-v4 section 3, as
// evidence/verb-coverage.md names them: each needs an action.
var mvpLeaves = []string{
	"status", "list", "snapshot", "activity", "health", "attention", "compatibility status", "read", "watch", "zoom",
	"attach", "send", "send -t", "interrupt", "retire", "recycle", "adopt", "mail inbox", "memory", "quota", "limits",
	"spawn", "add", "new", "reap", "kill", "wake", "doctor", "capabilities", "version", "modes list", "modes explain",
	"ensemble presets", "ensemble runs", "ensemble status", "ensemble stop", "feedback list", "recent operations",
	"admin recover inspect", "palette", "board",
}

func TestEveryMVPLeafHasAnAction(t *testing.T) {
	covered := map[string]string{}
	for _, a := range table {
		for _, c := range a.Covers {
			if other, ok := covered[c]; ok {
				t.Errorf("%q: covered by %s and %s", c, other, a.ID)
			}
			covered[c] = a.ID
		}
	}
	for _, leaf := range mvpLeaves {
		if covered[leaf] == "" {
			t.Errorf("MVP leaf %q has no action", leaf)
		}
	}
}

func TestLevelAndSpace(t *testing.T) {
	var top []string
	for _, a := range Level("") {
		top = append(top, a.ID)
	}
	if !slices.Equal(top, []string{"palette", "board", "spawn.add", "send", "operations", "agent", "workspace", "views", "diagnostics"}) {
		t.Fatalf("top level = %v", top)
	}
	var space []string
	for _, a := range Space() {
		space = append(space, a.Board.Space+" "+a.ID)
	}
	want := []string{"a palette", "s send", "r agent.read", "x agent.interrupt", "t agent.retire", "c agent.recycle", "p agent.adopt"}
	if !slices.Equal(space, want) {
		t.Fatalf("action menu = %v", space)
	}
	if Table()[0].ID != "palette" || len(Table()) != len(table) {
		t.Fatal("Table")
	}
}

var (
	bound1 = ui.Target{BindingID: "bnd-1", Generation: 3, PaneID: "w1:p1", Kind: "claude"}
	bound2 = ui.Target{BindingID: "bnd-2", Generation: 1, PaneID: "w1:p2", Kind: "codex"}
	loose  = ui.Target{PaneID: "w1:p5", Session: "s-5", Kind: "claude"}
	ctxWS  = ui.Context{Version: ui.ContextVersion, SocketPath: "/s", WorkspaceID: "w1"}
)

func mustAction(t *testing.T, id string) Action {
	t.Helper()
	a, ok := ByID(id)
	if !ok {
		t.Fatalf("no action %s", id)
	}
	return a
}

// TestTargets: no selection opens the picker, an empty one is refused,
// and nothing widens: a one-agent action refuses two, a bound-only action
// refuses an unbound agent, adopt refuses a bound one.
func TestTargets(t *testing.T) {
	interrupt, retire, adopt, read := mustAction(t, "agent.interrupt"), mustAction(t, "agent.retire"), mustAction(t, "agent.adopt"), mustAction(t, "agent.read")
	if ts, pick, err := interrupt.Targets(ctxWS); !pick || err != nil || ts != nil {
		t.Fatalf("no selection: %v %v %v", ts, pick, err)
	}
	if _, pick, err := interrupt.Targets(ctxWS.WithTargets("board", nil)); pick || !errors.Is(err, ui.ErrEmptySelection) {
		t.Fatalf("empty selection: %v %v", pick, err)
	}
	if ts, _, err := interrupt.Targets(ctxWS.WithTargets("board", []ui.Target{bound1, bound2})); err != nil || len(ts) != 2 {
		t.Fatalf("two to interrupt: %v %v", ts, err)
	}
	for name, tc := range map[string]struct {
		a  Action
		ts []ui.Target
	}{
		"retire two":        {retire, []ui.Target{bound1, bound2}},
		"retire an unbound": {retire, []ui.Target{loose}},
		"adopt a bound one": {adopt, []ui.Target{bound1}},
	} {
		if _, _, err := tc.a.Targets(ctxWS.WithTargets("board", tc.ts)); !errors.Is(err, ErrTargets) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, ts := range [][]ui.Target{{bound1}, {loose}} {
		if _, _, err := read.Targets(ctxWS.WithTargets("board", ts)); err != nil {
			t.Errorf("read %v: %v", ts, err)
		}
	}
	if ts, pick, err := mustAction(t, "view.status").Targets(ctxWS.WithTargets("board", []ui.Target{bound1})); ts != nil || pick || err != nil {
		t.Errorf("a view ignores the selection: %v %v %v", ts, pick, err)
	}
}

// fakeCore records what the handlers asked the core for.
type fakeCore struct {
	base      send.Request
	sent      []send.Request
	sendRes   send.Result
	expect    map[string]int64
	opID      string
	interrupt wait.InterruptResult
	retired   []lifecycle.RetireRequest
	retireRes lifecycle.RetireResult
	recycled  []lifecycle.RecycleRequest
	adopted   []bindings.AdoptRequest
	adoptRes  bindings.AdoptResult
	err       error
}

func (f *fakeCore) core() Core {
	return Core{
		SendBase: func(context.Context) (send.Request, error) { return f.base, nil },
		Send: func(_ context.Context, r send.Request) (send.Result, error) {
			f.sent = append(f.sent, r)
			return f.sendRes, f.err
		},
		Interrupt: func(_ context.Context, e map[string]int64, op string) (wait.InterruptResult, error) {
			f.expect, f.opID = e, op
			return f.interrupt, f.err
		},
		Retire: func(_ context.Context, r lifecycle.RetireRequest) (lifecycle.RetireResult, error) {
			f.retired = append(f.retired, r)
			return f.retireRes, f.err
		},
		Recycle: func(_ context.Context, r lifecycle.RecycleRequest) (lifecycle.RecycleResult, error) {
			f.recycled = append(f.recycled, r)
			return lifecycle.RecycleResult{OperationID: r.OperationID, MailName: "Blue", Stage: lifecycle.StageDone}, f.err
		},
		Adopt: func(_ context.Context, r bindings.AdoptRequest) (bindings.AdoptResult, error) {
			f.adopted = append(f.adopted, r)
			return f.adoptRes, f.err
		},
	}
}

// TestDoChecks: every surface goes through the same checks before a
// handler runs.
func TestDoChecks(t *testing.T) {
	f := &fakeCore{}
	interrupt := mustAction(t, "agent.interrupt")
	req := Request{Context: ctxWS, Targets: []ui.Target{bound1}, OperationID: "op-1"}
	if _, err := interrupt.Do(context.Background(), Core{}, req); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no core: %v", err)
	}
	noOp := req
	noOp.OperationID = ""
	if _, err := interrupt.Do(context.Background(), f.core(), noOp); !errors.Is(err, ErrNoOperationID) {
		t.Errorf("no operation id: %v", err)
	}
	empty := req
	empty.Targets = nil
	if _, err := interrupt.Do(context.Background(), f.core(), empty); !errors.Is(err, ui.ErrEmptySelection) {
		t.Errorf("no targets: %v", err)
	}
	twice := req
	twice.Targets = []ui.Target{bound1, bound1}
	if _, err := interrupt.Do(context.Background(), f.core(), twice); !errors.Is(err, ErrTargets) {
		t.Errorf("the same target twice: %v", err)
	}
	if f.expect != nil {
		t.Fatalf("the core was called: %v", f.expect)
	}
	for _, id := range []string{"agent", "palette", "agent.read"} {
		a := mustAction(t, id)
		if _, err := a.Do(context.Background(), f.core(), Request{Context: ctxWS, Targets: []ui.Target{loose}, OperationID: "op-1"}); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s ran: %v", id, err)
		}
	}
	kill := mustAction(t, "workspace.kill")
	if why := kill.Unavailable(ui.Context{}, f.core()); why == "" || !kill.Visible() {
		t.Errorf("kill without a workspace: %q", why)
	}
	if why := kill.Unavailable(ctxWS, f.core()); why != "this surface cannot kill" {
		t.Errorf("kill without its core seam: %q", why)
	}
	if why := mustAction(t, "agent.read").Unavailable(ctxWS, f.core()); why != "not built yet" || mustAction(t, "agent.read").Visible() {
		t.Errorf("read: no handler, so unavailable and hidden (%q)", why)
	}
	noAdopt := f.core()
	noAdopt.Adopt = nil
	if why := mustAction(t, "agent.adopt").Unavailable(ctxWS, noAdopt); why != "this surface cannot adopt" || !mustAction(t, "agent.adopt").Visible() {
		t.Errorf("adopt without its core seam: %q", why)
	}
	if why := mustAction(t, "palette").Unavailable(ui.Context{}, f.core()); why != "" || !mustAction(t, "palette").Visible() {
		t.Errorf("palette: %q", why)
	}
}

// TestSendFreezesItsTargets: the send names the frozen targets' panes and
// expects their bindings at the generations reviewed; it keeps the base's
// scope and sends the text as typed, never a template.
func TestSendFreezesItsTargets(t *testing.T) {
	f := &fakeCore{base: send.Request{Session: "w1", WorkspaceID: "w1", ProjectKey: "/p", Template: "stale.md",
		Selector: send.Selector{All: true}}}
	f.sendRes = send.Result{OperationID: "op-7", Targets: []send.Receipt{
		{BindingID: "bnd-1", MailName: "Blue", PaneID: "w1:p1", Status: "submitted"},
		{BindingID: "bnd-2", MailName: "Red", PaneID: "w1:p2", Status: "refused", ErrorCode: send.CodeGenerationChanged,
			Result: send.ReceiptResult{Reason: "expected generation 1, the binding is at generation 2"}},
	}}
	a := mustAction(t, "send")
	res, err := a.Do(context.Background(), f.core(), Request{Context: ctxWS, Targets: []ui.Target{bound1, bound2},
		OperationID: "op-7", Params: map[string]string{ParamText: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	got := f.sent[0]
	if !reflect.DeepEqual(got.Selector, send.Selector{Panes: []string{"w1:p1", "w1:p2"}}) ||
		!reflect.DeepEqual(got.Expect, map[string]int64{"bnd-1": 3, "bnd-2": 1}) {
		t.Fatalf("selector %+v, expect %v", got.Selector, got.Expect)
	}
	if got.Message != "hello" || got.Template != "" || got.OperationID != "op-7" || got.Source != history.SourcePalette ||
		got.Session != "w1" || got.ProjectKey != "/p" {
		t.Fatalf("request = %+v", got)
	}
	if res.Status != "partial" || len(res.Targets) != 2 || res.Targets[1].Target != bound2 || res.Targets[1].Status != "refused" ||
		!strings.Contains(res.Targets[1].Reason, "generation 2") {
		t.Fatalf("result = %+v", res)
	}
	for _, text := range []string{"", "  \n"} {
		_, err := a.Do(context.Background(), f.core(), Request{Context: ctxWS, Targets: []ui.Target{bound1}, OperationID: "op-8",
			Params: map[string]string{ParamText: text}})
		if !errors.Is(err, ErrEmptyText) {
			t.Errorf("text %q: %v", text, err)
		}
	}
	if len(f.sent) != 1 {
		t.Fatalf("an empty text was sent: %d sends", len(f.sent))
	}
	// A target whose pane is gone stays in Expect (the core refuses it);
	// with no pane at all, nothing goes out: an empty selector is "all".
	gone := bound2
	gone.PaneID = ""
	if _, err := a.Do(context.Background(), f.core(), Request{Context: ctxWS, Targets: []ui.Target{bound1, gone}, OperationID: "op-9",
		Params: map[string]string{ParamText: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.sent[1]; !slices.Equal(got.Selector.Panes, []string{"w1:p1"}) || got.Expect["bnd-2"] != 1 {
		t.Fatalf("selector %+v, expect %v", got.Selector, got.Expect)
	}
	if _, err := a.Do(context.Background(), f.core(), Request{Context: ctxWS, Targets: []ui.Target{gone}, OperationID: "op-10",
		Params: map[string]string{ParamText: "hi"}}); !errors.Is(err, ErrNoPane) || len(f.sent) != 2 {
		t.Fatalf("no pane: %v, %d sends", err, len(f.sent))
	}
}

func TestInterruptExpectsTheReviewedGenerations(t *testing.T) {
	f := &fakeCore{interrupt: wait.InterruptResult{OperationID: "op-2", Status: "partial", Targets: []wait.InterruptTarget{
		{BindingID: "bnd-1", MailName: "Blue", Status: wait.InterruptSent, Keys: []string{"esc"}},
		{BindingID: "bnd-2", MailName: "Red", Status: wait.InterruptRefused, Reason: wait.ReasonNotSelected},
	}}}
	res, err := mustAction(t, "agent.interrupt").Do(context.Background(), f.core(),
		Request{Context: ctxWS, Targets: []ui.Target{bound1, bound2}, OperationID: "op-2"})
	if err != nil || f.opID != "op-2" || !reflect.DeepEqual(f.expect, map[string]int64{"bnd-1": 3, "bnd-2": 1}) {
		t.Fatalf("core got %v %q (%v)", f.expect, f.opID, err)
	}
	if res.Status != "partial" || res.Targets[0].Reason != "esc" || res.Targets[1].Target != bound2 ||
		res.Targets[1].Reason != wait.ReasonNotSelected {
		t.Fatalf("result = %+v", res)
	}
}

// TestRetireAndRecycleNameTheBinding: one target, by binding id at the
// reviewed generation, never by pane (where another agent may be now).
func TestRetireAndRecycleNameTheBinding(t *testing.T) {
	f := &fakeCore{retireRes: lifecycle.RetireResult{MailName: "Blue", DryRun: true, Actions: []string{"close pane w1:p1"}}}
	res, err := mustAction(t, "agent.retire").Do(context.Background(), f.core(),
		Request{Context: ctxWS, Targets: []ui.Target{bound1}, OperationID: "op-3", Params: map[string]string{ParamDryRun: "1"}})
	r := f.retired[0]
	if err != nil || r.Target != "bnd-1" || r.ExpectedGeneration != 3 || !r.DryRun || r.OperationID != "" || r.Reason != "retire" {
		t.Fatalf("dry run request = %+v (%v)", r, err)
	}
	if res.Status != "checked" || res.Lines[0] != "close pane w1:p1" {
		t.Fatalf("dry run result = %+v", res)
	}
	f.retireRes = lifecycle.RetireResult{MailName: "Blue", Refusals: []string{"Blue changed since it was reviewed"}}
	f.err = lifecycle.ErrRefused
	res, err = mustAction(t, "agent.retire").Do(context.Background(), f.core(),
		Request{Context: ctxWS, Targets: []ui.Target{bound1}, OperationID: "op-4"})
	if !errors.Is(err, lifecycle.ErrRefused) || f.retired[1].OperationID != "op-4" || res.Status != "refused" ||
		res.Targets[0].Reason != "Blue changed since it was reviewed" {
		t.Fatalf("refused retire = %+v %v", res, err)
	}
	f.err = nil
	res, err = mustAction(t, "agent.recycle").Do(context.Background(), f.core(),
		Request{Context: ctxWS, Targets: []ui.Target{bound1}, OperationID: "op-5", Params: map[string]string{ParamTemplate: "fresh_review"}})
	c := f.recycled[0]
	if err != nil || c.Target != "bnd-1" || c.ExpectedGeneration != 3 || c.Template != "fresh_review" || c.OperationID != "op-5" ||
		res.Status != lifecycle.StageDone || res.Targets[0].Name != "Blue" {
		t.Fatalf("recycle = %+v %+v %v", c, res, err)
	}
}

func TestGuardSubmitsOnce(t *testing.T) {
	n := 0
	g := NewGuard(func() (string, error) { n++; return fmt.Sprintf("op-%d", n), nil })
	req := Request{Context: ctxWS, Targets: []ui.Target{bound1, bound2}, Params: map[string]string{ParamText: "hi"}}
	key := ReviewKey("send", req)
	if _, err := g.Submit(key); !errors.Is(err, ErrNotReviewed) {
		t.Fatalf("submit before review: %v", err)
	}
	id, _ := g.Review(key)
	if again, _ := g.Review(key); again != id {
		t.Fatalf("a redraw changed the id: %s %s", id, again)
	}
	edited := req
	edited.Params = map[string]string{ParamText: "hi!"}
	if _, err := g.Submit(ReviewKey("send", edited)); !errors.Is(err, ErrNotReviewed) {
		t.Fatalf("submit of an edited request: %v", err)
	}
	if got, err := g.Submit(key); err != nil || got != id {
		t.Fatalf("submit = %s %v", got, err)
	}
	if _, err := g.Submit(key); !errors.Is(err, ErrSubmitted) {
		t.Fatalf("second submit: %v", err)
	}
	// The review's pattern of the board's composer (review, submit; review,
	// submit) must not send twice: a held request has no new review.
	if again, err := g.Review(key); !errors.Is(err, ErrSubmitted) || again != "" {
		t.Fatalf("review of a held request = %q %v", again, err)
	}
	// Another request may run meanwhile; the held one stays held.
	other, err := g.Review(ReviewKey("send", edited))
	if err != nil || other == id {
		t.Fatalf("another request = %q %v", other, err)
	}
	if _, err := g.Review(key); !errors.Is(err, ErrSubmitted) {
		t.Fatalf("the held request after another review: %v", err)
	}
	// A lost answer keeps the hold; an answer ends it, and the same text is
	// then a new operation.
	g.Settle(key, ErrNoAnswer)
	if _, err := g.Review(key); !errors.Is(err, ErrSubmitted) {
		t.Fatalf("after a lost answer: %v", err)
	}
	g.Settle(key, nil)
	if next, err := g.Review(key); err != nil || next == id || next == other {
		t.Fatalf("after the answer = %q %v", next, err)
	}
	g2 := NewGuard(func() (string, error) { return "", errors.New("no clock") })
	if _, err := g2.Review(key); err == nil {
		t.Fatal("a failed id was used")
	}
}

func TestReviewKey(t *testing.T) {
	req := Request{Targets: []ui.Target{bound1, bound2}, Params: map[string]string{"a": "1", "b": "2"}}
	swapped := Request{Targets: []ui.Target{bound2, bound1}, Params: map[string]string{"b": "2", "a": "1"}}
	if ReviewKey("send", req) != ReviewKey("send", swapped) {
		t.Fatal("the order of targets or params changed the key")
	}
	other := bound1
	other.Generation = 4
	for name, r := range map[string]Request{
		"another generation": {Targets: []ui.Target{other, bound2}, Params: req.Params},
		"fewer targets":      {Targets: []ui.Target{bound1}, Params: req.Params},
		"another value":      {Targets: req.Targets, Params: map[string]string{"a": "1", "b": "3"}},
		"a split value":      {Targets: req.Targets, Params: map[string]string{"a": "1b=2"}},
	} {
		if ReviewKey("send", r) == ReviewKey("send", req) {
			t.Errorf("%s: same key", name)
		}
	}
	if ReviewKey("send", req) == ReviewKey("agent.interrupt", req) {
		t.Fatal("two actions share a key")
	}
}
