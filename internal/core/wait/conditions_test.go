package wait

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

func TestNTMMeets(t *testing.T) {
	for _, c := range []struct {
		conds []string
		st    herdr.AgentStatus
		want  bool
	}{
		{[]string{"idle"}, herdr.StatusIdle, true},
		{[]string{"idle"}, herdr.StatusDone, true},
		{[]string{"complete"}, herdr.StatusDone, true},
		{[]string{"idle"}, herdr.StatusWorking, false},
		{[]string{"done"}, herdr.StatusIdle, false},
		{[]string{"generating"}, herdr.StatusWorking, true},
		{[]string{"healthy"}, herdr.StatusUnknown, true},
		{[]string{"healthy"}, herdr.StatusBlocked, false},
		{[]string{"idle", "healthy"}, herdr.StatusIdle, true},
		{[]string{"idle", "generating"}, herdr.StatusIdle, false},
		{[]string{"idle", "generating"}, herdr.StatusWorking, false},
	} {
		if got := NTMMeets(c.conds, c.st); got != c.want {
			t.Errorf("NTMMeets(%v, %s) = %v", c.conds, c.st, got)
		}
	}
}

// pollHerdr answers agent.get from a script of states per pane: each read
// takes the next state, and the last one stays.
type pollHerdr struct {
	Herdr
	mu     sync.Mutex
	states map[string][]herdr.AgentStatus
	kind   map[string]string
	errs   map[string]error
}

func (h *pollHerdr) AgentGet(_ context.Context, pane string) (herdr.AgentInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.errs[pane]; err != nil {
		return herdr.AgentInfo{}, err
	}
	s := h.states[pane]
	st := s[0]
	if len(s) > 1 {
		h.states[pane] = s[1:]
	}
	kind := h.kind[pane]
	if kind == "" {
		kind = "claude"
	}
	return herdr.AgentInfo{PaneID: pane, Agent: kind, AgentStatus: st}, nil
}

func fixed(bs ...bindings.Binding) func(context.Context) ([]bindings.Binding, error) {
	return func(context.Context) ([]bindings.Binding, error) { return bs, nil }
}

var (
	bindA = bindings.Binding{ID: "a", PaneID: "w1:p1", Kind: "claude"}
	bindB = bindings.Binding{ID: "b", PaneID: "w1:p2", Kind: "claude"}
)

// ntm's wait checks every agent in one poll: an agent that was idle at the
// first poll and works at the second does not count (the reviewer's case:
// a send, then a wait, and one agent starts its turn late).
func TestNTMWaitNeedsEveryAgentInOnePoll(t *testing.T) {
	h := &pollHerdr{states: map[string][]herdr.AgentStatus{
		"w1:p1": {herdr.StatusIdle, herdr.StatusWorking, herdr.StatusIdle},
		"w1:p2": {herdr.StatusWorking, herdr.StatusIdle, herdr.StatusIdle},
	}}
	res, err := NTMWait(context.Background(), h, fixed(bindA, bindB), NTMRequest{Conds: []string{"idle"}, Poll: time.Millisecond})
	if err != nil || len(res.Met) != 2 {
		t.Fatalf("= %+v %v", res, err)
	}
	if left := h.states["w1:p1"]; len(left) != 1 {
		t.Fatalf("met before the third poll: %v", left)
	}
}

// --any: Count agents in one poll, and each of them is listed; a count of
// 0 is met at the first poll, as ntm's.
func TestNTMWaitAny(t *testing.T) {
	h := &pollHerdr{states: map[string][]herdr.AgentStatus{"w1:p1": {herdr.StatusIdle}, "w1:p2": {herdr.StatusIdle}}}
	res, err := NTMWait(context.Background(), h, fixed(bindA, bindB), NTMRequest{Conds: []string{"idle"}, Any: true, Count: 1, Poll: time.Millisecond})
	if err != nil || len(res.Met) != 2 {
		t.Fatalf("--any = %+v %v", res, err)
	}
	h = &pollHerdr{states: map[string][]herdr.AgentStatus{"w1:p1": {herdr.StatusWorking}}}
	res, err = NTMWait(context.Background(), h, fixed(bindA), NTMRequest{Conds: []string{"idle"}, Any: true, Count: 0, Poll: time.Millisecond})
	if err != nil || res.Met != nil || res.NoPanes {
		t.Fatalf("--count 0 = %+v %v", res, err)
	}
}

// A gone agent drops out, as a closed pane drops out of ntm's poll; with
// --exit-on-error it ends the wait. One that holds another kind is gone
// too. With no agent left: no panes.
func TestNTMWaitGoneAgents(t *testing.T) {
	notFound := &herdr.Error{Method: "agent.get", Code: herdr.CodeAgentNotFound}
	h := &pollHerdr{states: map[string][]herdr.AgentStatus{"w1:p1": {herdr.StatusWorking}, "w1:p2": {herdr.StatusIdle}},
		errs: map[string]error{"w1:p1": notFound}}
	res, err := NTMWait(context.Background(), h, fixed(bindA, bindB), NTMRequest{Conds: []string{"idle"}, Poll: time.Millisecond})
	if err != nil || len(res.Met) != 1 || res.Met[0].PaneID != "w1:p2" {
		t.Fatalf("the rest = %+v %v", res, err)
	}
	res, err = NTMWait(context.Background(), h, fixed(bindA, bindB), NTMRequest{Conds: []string{"idle"}, ExitOnError: true, Poll: time.Millisecond})
	if err != nil || res.Lost != "w1:p1" {
		t.Fatalf("--exit-on-error = %+v %v", res, err)
	}
	h = &pollHerdr{states: map[string][]herdr.AgentStatus{"w1:p1": {herdr.StatusIdle}}, kind: map[string]string{"w1:p1": "pi"}}
	res, err = NTMWait(context.Background(), h, fixed(bindA), NTMRequest{Conds: []string{"idle"}, Poll: time.Millisecond})
	if err != nil || !res.NoPanes {
		t.Fatalf("another kind = %+v %v", res, err)
	}
}

// A read that fails otherwise leaves the agent out of that poll; a list
// that no state meets runs to the deadline; a selection error ends it.
func TestNTMWaitTimeoutAndErrors(t *testing.T) {
	h := &pollHerdr{states: map[string][]herdr.AgentStatus{"w1:p1": {herdr.StatusIdle}},
		errs: map[string]error{"w1:p1": errors.New("socket closed")}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := NTMWait(ctx, h, fixed(bindA), NTMRequest{Conds: []string{"idle"}, Poll: time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unread = %v", err)
	}
	h = &pollHerdr{states: map[string][]herdr.AgentStatus{"w1:p1": {herdr.StatusIdle}}}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err := NTMWait(ctx2, h, fixed(bindA), NTMRequest{Conds: []string{"idle", "generating"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no state = %v", err)
	}
	boom := errors.New("store closed")
	sel := func(context.Context) ([]bindings.Binding, error) { return nil, boom }
	if _, err := NTMWait(context.Background(), h, sel, NTMRequest{Conds: []string{"idle"}}); !errors.Is(err, boom) {
		t.Fatalf("selection = %v", err)
	}
}
