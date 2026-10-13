package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// The menu's reap: a dry run, then exactly the reviewed set. A binding
// that changed or went away is refused by name; an agent the review did
// not show is never retired.
func TestReapRetiresOnlyTheReviewedSet(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i, name := range []string{"IcyKnoll", "BlueLake", "RedStone", "GreenPike"} {
		w.bind(t, name, "w1:p"+string(rune('1'+i)), herdr.StatusIdle)
	}
	req := ReapRequest{Idle: 10 * time.Minute, WorkspaceID: "w1", DryRun: true}
	if _, err := Reap(ctx, w.env, nil, req); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(11 * time.Minute)
	review, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	gen := map[string]ReapItem{}
	for _, it := range review {
		if it.Action != "would_retire" || it.Generation == 0 {
			t.Fatalf("review = %+v", it)
		}
		gen[it.MailName] = it
	}
	// IcyKnoll, the one agent that is retired, is last in the set: its
	// receipt and its retire's id follow the set's order, not the sweep's.
	var only []ReapTarget
	for _, name := range []string{"BlueLake", "RedStone", "IcyKnoll"} {
		only = append(only, ReapTarget{BindingID: gen[name].BindingID, Generation: gen[name].Generation})
	}
	// After the review: BlueLake is recycled (a new generation), RedStone
	// is retired by hand.
	blue, err := w.env.Bindings.Get(ctx, gen["BlueLake"].BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.env.Bindings.Occupy(ctx, blue.ID, blue.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: blue.TabID,
		PaneID: blue.PaneID, HerdrAlias: blue.HerdrAlias, Kind: blue.Kind, Model: blue.Model, NativeSession: blue.NativeSession}); err != nil {
		t.Fatal(err)
	}
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "RedStone"}); err != nil {
		t.Fatal(err)
	}
	req.DryRun, req.Only, req.OperationID = false, only, "op-reap-1"
	items, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ReapItem{}
	for _, it := range items {
		got[it.MailName] = it
	}
	if len(items) != 3 || got["IcyKnoll"].Action != "retired" {
		t.Fatalf("items = %+v", items)
	}
	if b := got["BlueLake"]; b.Action != "refused" || !strings.Contains(b.Reason, "changed since it was reviewed") {
		t.Fatalf("BlueLake = %+v", b)
	}
	if r := got["RedStone"]; r.Action != "refused" || !strings.Contains(r.Reason, "no longer an active binding") {
		t.Fatalf("RedStone = %+v", r)
	}
	if green, err := w.env.Bindings.Get(ctx, gen["GreenPike"].BindingID); err != nil || green.Status != bindings.Active {
		t.Fatalf("GreenPike, outside the review = %+v %v", green, err)
	}
	// The sweep has its record under the menu's id, with one receipt per
	// reviewed agent in the review's order, and IcyKnoll's retire is its
	// child (the review's M3).
	op, err := ReadOperation(ctx, w.env.DB, "op-reap-1")
	if err != nil || op.Verb != VerbReap || op.Status != ReapDone || op.Completed == nil || len(op.Targets) != 3 {
		t.Fatalf("record = %+v %v", op, err)
	}
	for i, want := range []string{"refused", "refused", "retired"} {
		if tg := op.Targets[i]; tg.Status != want || tg.BindingID != only[i].BindingID {
			t.Fatalf("receipt %d = %+v, want %s", i, tg, want)
		}
	}
	if !strings.Contains(string(op.Targets[2].Result), `"retire_operation":"op-reap-1.2"`) ||
		!strings.Contains(string(op.Targets[0].Result), "changed since it was reviewed") {
		t.Fatalf("receipts = %s | %s", op.Targets[2].Result, op.Targets[0].Result)
	}
	if child, err := ReadOperation(ctx, w.env.DB, "op-reap-1.2"); err != nil || child.Verb != "reap" || child.Completed == nil {
		t.Fatalf("IcyKnoll's retire = %+v %v", child, err)
	}
	// An id claimed before is refused before any retire: GreenPike, whom
	// the sweep could retire, stays.
	if _, err := w.env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: "op-taken", Verb: "other"}, w.clock); err != nil {
		t.Fatal(err)
	}
	taken := ReapRequest{Idle: req.Idle, WorkspaceID: "w1", OperationID: "op-taken",
		Only: []ReapTarget{{BindingID: gen["GreenPike"].BindingID, Generation: gen["GreenPike"].Generation}}}
	if _, err := Reap(ctx, w.env, nil, taken); !errors.Is(err, ErrRefused) {
		t.Fatalf("a sweep under a claimed id = %v", err)
	}
	if green, err := w.env.Bindings.Get(ctx, gen["GreenPike"].BindingID); err != nil || green.Status != bindings.Active {
		t.Fatalf("GreenPike, under a claimed id = %+v %v", green, err)
	}
	// A dry run records nothing; a set that names an agent twice is refused.
	dry := ReapRequest{Idle: req.Idle, WorkspaceID: "w1", DryRun: true, Only: only[:1], OperationID: "op-dry"}
	if _, err := Reap(ctx, w.env, nil, dry); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOperation(ctx, w.env.DB, "op-dry"); !errors.Is(err, ErrNoOperation) {
		t.Fatalf("a dry run's record = %v", err)
	}
	dry.Only = []ReapTarget{only[0], only[0]}
	if _, err := Reap(ctx, w.env, nil, dry); !errors.Is(err, ErrRefused) {
		t.Fatalf("a set with a duplicate = %v", err)
	}
	// An explicitly empty set is refused, never read as "all".
	req.Only, req.OperationID = []ReapTarget{}, ""
	if _, err := Reap(ctx, w.env, nil, req); !errors.Is(err, ErrRefused) {
		t.Fatalf("an empty set = %v", err)
	}
}

// A recorded sweep writes each agent's receipt as it goes, and stops at a
// retire that fails after its claim: the agent before it is retired, the
// failed one says so, the one after stays pending, and the record ends
// stopped (the review's M3).
func TestReapRecordStopsAtAFailedRetire(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i, name := range []string{"IcyKnoll", "BlueLake", "RedStone"} {
		w.bind(t, name, "w1:p"+string(rune('1'+i)), herdr.StatusIdle)
	}
	req := ReapRequest{Idle: 10 * time.Minute, WorkspaceID: "w1", DryRun: true}
	if _, err := Reap(ctx, w.env, nil, req); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(11 * time.Minute)
	review, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	var only []ReapTarget
	for _, it := range review {
		only = append(only, ReapTarget{BindingID: it.BindingID, Generation: it.Generation})
	}
	// The second pane to close has its binding changed under it, so its
	// retire fails after its claim.
	h := &hookHerdr{fakeHerdr: w.h}
	w.env.Herdr = h
	var closed []string
	h.onClose = func(pane string) {
		closed = append(closed, pane)
		if len(closed) != 2 {
			return
		}
		list, err := w.env.Bindings.List(ctx, bindings.Filter{WorkspaceID: "w1", Statuses: []bindings.Status{bindings.Active}})
		if err != nil {
			t.Error(err)
			return
		}
		for _, b := range list {
			if b.PaneID == pane {
				if _, err := w.env.Bindings.Occupy(ctx, b.ID, b.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: b.TabID,
					PaneID: b.PaneID, HerdrAlias: b.HerdrAlias, Kind: b.Kind, Model: b.Model, NativeSession: b.NativeSession}); err != nil {
					t.Error(err)
				}
			}
		}
	}
	req.DryRun, req.Only, req.OperationID = false, only, "op-reap-f"
	items, err := Reap(ctx, w.env, nil, req)
	if err == nil || len(closed) != 2 {
		t.Fatalf("a sweep with a failed retire = %+v %v (closed %v)", items, err, closed)
	}
	op, err := ReadOperation(ctx, w.env.DB, "op-reap-f")
	if err != nil || op.Status != ReapStopped || op.Completed == nil || len(op.Targets) != 3 {
		t.Fatalf("record = %+v %v", op, err)
	}
	byPane := map[string]string{}
	for _, it := range review {
		byPane[it.PaneID] = it.BindingID
	}
	status := map[string]string{}
	for _, tg := range op.Targets {
		status[tg.BindingID] = tg.Status
	}
	if status[byPane[closed[0]]] != "retired" || status[byPane[closed[1]]] != "failed" {
		t.Fatalf("receipts = %v (closed %v)", status, closed)
	}
	// The agent the sweep did not reach is skipped: every receipt ends
	// final, and the items name it too.
	skipped := 0
	for _, s := range status {
		if s == "skipped" {
			skipped++
		}
	}
	if skipped != 1 || len(items) != 3 {
		t.Fatalf("receipts = %v, items = %+v: want one skipped", status, items)
	}
}

// wakeHerdr wakes an agent between retire's checks and its close: the
// second agent.get of that pane says it works.
type wakeHerdr struct {
	*fakeHerdr
	pane  string
	reads int
	// fail makes that second read fail instead (herdr went away).
	fail error
}

func (h *wakeHerdr) AgentGet(ctx context.Context, pane string) (herdr.AgentInfo, error) {
	a, err := h.fakeHerdr.AgentGet(ctx, pane)
	if pane == h.pane {
		if h.reads++; h.reads == 2 {
			if h.fail != nil {
				return a, h.fail
			}
			a.AgentStatus = herdr.StatusWorking
		}
	}
	return a, err
}

// An agent that wakes between retire's checks and its close is refused,
// with nothing closed, and the sweep goes on to the others (the re-check's
// A: it stopped the sweep as "failed").
func TestReapGoesOnPastAnAgentThatWoke(t *testing.T) {
	t.Run("woke", func(t *testing.T) { reapPastBlueLake(t, nil, "changed since the checks") })
	// A herdr read that fails before the close refuses too: nothing ran
	// (PearlKnoll's re-check, low 1).
	t.Run("read failed", func(t *testing.T) {
		reapPastBlueLake(t, errors.New("herdr: socket closed"), "cannot be read again before the close")
	})
}

// reapPastBlueLake confirms a reviewed reap of three agents in which
// BlueLake's last read before its close says it works (fail nil) or
// fails: BlueLake is refused with reason, and the others are retired.
func reapPastBlueLake(t *testing.T, fail error, reason string) {
	w := newWorld(t)
	ctx := context.Background()
	for i, name := range []string{"IcyKnoll", "BlueLake", "RedStone"} {
		w.bind(t, name, "w1:p"+string(rune('1'+i)), herdr.StatusIdle)
	}
	req := ReapRequest{Idle: 10 * time.Minute, WorkspaceID: "w1", DryRun: true}
	if _, err := Reap(ctx, w.env, nil, req); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(11 * time.Minute)
	review, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	var only []ReapTarget
	blue := ""
	for _, it := range review {
		only = append(only, ReapTarget{BindingID: it.BindingID, Generation: it.Generation})
		if it.MailName == "BlueLake" {
			blue = it.BindingID
		}
	}
	w.env.Herdr = &wakeHerdr{fakeHerdr: w.h, pane: "w1:p2", fail: fail}
	req.DryRun, req.Only, req.OperationID = false, only, "op-reap-w"
	items, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatalf("a sweep past BlueLake = %v", err)
	}
	got := map[string]ReapItem{}
	for _, it := range items {
		got[it.MailName] = it
	}
	if b := got["BlueLake"]; b.Action != "refused" || !strings.Contains(b.Reason, reason) ||
		got["IcyKnoll"].Action != "retired" || got["RedStone"].Action != "retired" {
		t.Fatalf("items = %+v", items)
	}
	op, err := ReadOperation(ctx, w.env.DB, "op-reap-w")
	if err != nil || op.Status != ReapDone {
		t.Fatalf("record = %+v %v", op, err)
	}
	if child, err := ReadOperation(ctx, w.env.DB, got["BlueLake"].Retire.OperationID); err != nil || child.Status != "refused" {
		t.Fatalf("BlueLake's retire record = %+v %v", child, err)
	}
	if b, err := w.env.Bindings.Get(ctx, blue); err != nil || b.Status != bindings.Active {
		t.Fatalf("BlueLake = %+v %v", b, err)
	}
}

// A retry under an id that another verb holds is refused: it is not that
// operation's outcome (the re-check's low 2).
func TestRetireRefusesAnIDOfAnotherVerb(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	if _, err := w.env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: "op-x", Verb: "send"}, w.clock); err != nil {
		t.Fatal(err)
	}
	res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", OperationID: "op-x"})
	if !errors.Is(err, ErrRefused) || res.Done || !strings.Contains(err.Error(), "the id is taken") {
		t.Fatalf("retire under a send's id = %+v %v", res, err)
	}
}
