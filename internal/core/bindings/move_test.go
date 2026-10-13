package bindings

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// A move keeps the occupant: the generation stays; another workspace
// gives a new slot and keeps the old row (plan-v8 2.12).
func TestRelocateKeepsTheGenerationAndTakesANewSlot(t *testing.T) {
	db, s := openStore(t)
	ctx := context.Background()
	b, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, newBinding("BlueLake", "/p", "w2", "w2:t1", "w2:p1"))
	if err != nil {
		t.Fatal(err)
	}
	ev := Evidence{Kind: "move", Detail: "test"}
	if _, err := s.Relocate(ctx, b.ID, b.Generation, Location{"w2", "w2:t1", "w2:p1"}, ev); !errors.Is(err, ErrPaneBusy) {
		t.Fatalf("onto %s's pane: %v", other.MailName, err)
	}
	if _, err := s.Relocate(ctx, b.ID, b.Generation+1, Location{"w2", "w2:t2", "w2:p2"}, ev); !errors.Is(err, ErrStale) {
		t.Fatalf("stale generation: %v", err)
	}
	moved, err := s.Relocate(ctx, b.ID, b.Generation, Location{"w2", "w2:t2", "w2:p2"}, ev)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Generation != b.Generation || moved.Status != Active || moved.WorkspaceID != "w2" || moved.TabID != "w2:t2" ||
		moved.PaneID != "w2:p2" || moved.Slot != "1.1" || moved.Evidence[len(moved.Evidence)-1] != ev {
		t.Fatalf("moved = %+v", moved)
	}
	var rows int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM slots WHERE binding_id = ?`, b.ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("slot rows = %d %v, want the old and the new", rows, err)
	}
	// A move in the same workspace keeps the slot.
	again, err := s.Relocate(ctx, b.ID, moved.Generation, Location{"w2", "w2:t3", "w2:p3"}, ev)
	if err != nil || again.Slot != "1.1" {
		t.Fatalf("same workspace: %+v %v", again, err)
	}
	if _, err := s.Displace(ctx, b.ID, again.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Relocate(ctx, b.ID, again.Generation, Location{"w2", "w2:t4", "w2:p4"}, ev); !errors.Is(err, ErrStale) {
		t.Fatalf("a displaced binding moved: %v", err)
	}
}

// A moved agent's own commands carry its old HERDR_PANE_ID; herdr's pane
// calls answer it with the new pane (nhm-5qa). The caller's pane is the
// old id until nhm follows the move, then the new one.
func TestCallerPaneFollowsAMove(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	h := moveSession()
	h.moved = map[string]string{"w1:p1": "w2:p1"}
	b, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := CallerPane(ctx, s, h, "w1:p1"); got != "w1:p1" {
		t.Fatalf("before nhm follows: %q", got)
	}
	if _, err := s.Relocate(ctx, b.ID, b.Generation, Location{"w2", "w2:t1", "w2:p1"}, Evidence{Kind: "move"}); err != nil {
		t.Fatal(err)
	}
	if got := CallerPane(ctx, s, h, "w1:p1"); got != "w2:p1" {
		t.Fatalf("after: %q", got)
	}
	getenv := func(k string) string { return map[string]string{"HERDR_PANE_ID": "w1:p1"}[k] }
	r, err := Resolve(ctx, s, h, Query{Current: true}, getenv)
	if err != nil || r.Binding.ID != b.ID {
		t.Fatalf("resolve --current = %+v %v", r.Binding, err)
	}
	if got := CallerPane(ctx, s, nil, "w1:p1"); got != "w1:p1" {
		t.Fatalf("without herdr: %q", got)
	}
	if got := CallerPane(ctx, s, h, ""); got != "" {
		t.Fatalf("outside herdr: %q", got)
	}
}

// moveSession is IcyKnoll's agent (pid 300, session sess-9) after herdr
// moved its pane w1:p1 to workspace w2 as w2:p1, beside other agents.
func moveSession(extra ...herdr.AgentInfo) *fakeHerdr {
	f := &fakeHerdr{procs: map[string][]herdr.Process{}}
	agents := append([]herdr.AgentInfo{{PaneID: "w2:p1", TabID: "w2:t1", WorkspaceID: "w2", Agent: "claude",
		AgentSession: &herdr.AgentSession{Value: "sess-9"}}}, extra...)
	for _, a := range agents {
		f.snap.Panes = append(f.snap.Panes, herdr.PaneInfo{PaneID: a.PaneID, TabID: a.TabID, WorkspaceID: a.WorkspaceID, Agent: a.Agent})
		f.snap.Agents = append(f.snap.Agents, a)
	}
	f.procs["w2:p1"] = []herdr.Process{{PID: 300, Name: "claude"}}
	return f
}

func TestMovedFindsTheSameProcess(t *testing.T) {
	fakeProcStart(t, map[int64]int64{300: 77, 400: 88})
	_, s := openStore(t)
	ctx := context.Background()
	n := newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1")
	n.ForegroundPID, n.ForegroundStart = 300, 77
	n.NativeSession = &NativeSession{Value: "sess-9"}
	b, err := s.Create(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	// Another claude in another pane, and a resume of the same session in
	// a new process: neither is the occupant.
	h := moveSession(herdr.AgentInfo{PaneID: "w2:p2", TabID: "w2:t2", WorkspaceID: "w2", Agent: "claude",
		AgentSession: &herdr.AgentSession{Value: "sess-9"}})
	h.procs["w2:p2"] = []herdr.Process{{PID: 400, Name: "claude"}}
	a, ok, err := Moved(ctx, h, s, b)
	if err != nil || !ok || a.PaneID != "w2:p1" {
		t.Fatalf("Moved = %+v %v %v", a, ok, err)
	}
	// While the old pane is in the session, its agent left: no move.
	h.snap.Panes = append(h.snap.Panes, herdr.PaneInfo{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1"})
	if _, ok, err := Moved(ctx, h, s, b); ok || err != nil {
		t.Fatalf("old pane present: %v %v", ok, err)
	}
	// The process is gone (a new one runs the session): no move.
	h = moveSession()
	h.procs["w2:p1"] = []herdr.Process{{PID: 400, Name: "claude"}}
	if _, ok, err := Moved(ctx, h, s, b); ok || err != nil {
		t.Fatalf("new process: %v %v", ok, err)
	}
	// A pane another binding holds is never a candidate.
	if _, err := s.Create(ctx, newBinding("BlueLake", "/p", "w2", "w2:t1", "w2:p1")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Moved(ctx, moveSession(), s, b); ok || err != nil {
		t.Fatalf("bound pane: %v %v", ok, err)
	}
}

// Without a recorded process nothing proves a move: a pane with the
// binding's session may hold a resume in a new process (nhm-ceu), so it
// is named for adopt --handoff-from and the binding stays. Two panes
// with the binding's process are ambiguous.
func TestMovedNeedsTheProcess(t *testing.T) {
	fakeProcStart(t, map[int64]int64{300: 77})
	_, s := openStore(t)
	ctx := context.Background()
	n := newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1")
	n.NativeSession = &NativeSession{Value: "sess-9"}
	b, err := s.Create(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := Moved(ctx, moveSession(), s, b)
	if ok || !errors.Is(err, ErrUnprovenMove) || !strings.Contains(err.Error(), "w2:p1") || !strings.Contains(err.Error(), "--handoff-from "+b.ID) {
		t.Fatalf("session only: %v %v", ok, err)
	}
	b.NativeSession = nil
	if _, ok, err := Moved(ctx, moveSession(), s, b); ok || err != nil {
		t.Fatalf("no evidence: %v %v", ok, err)
	}
	b.ForegroundPID, b.ForegroundStart = 300, 77
	h := moveSession(herdr.AgentInfo{PaneID: "w3:p1", TabID: "w3:t1", WorkspaceID: "w3", Agent: "claude"})
	h.procs["w3:p1"] = []herdr.Process{{PID: 300, Name: "claude"}}
	if _, ok, err := Moved(ctx, h, s, b); ok || !errors.Is(err, ErrAmbiguousMove) {
		t.Fatalf("two candidates: %v %v", ok, err)
	}
}
