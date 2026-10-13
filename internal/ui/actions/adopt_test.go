package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/ui"
)

// TestAdoptNamesTheReviewedAgent: adopt takes one unbound agent and
// passes its pane, kind and session, so the core refuses another agent
// that started in the pane since the review.
func TestAdoptNamesTheReviewedAgent(t *testing.T) {
	f := &fakeCore{adoptRes: bindings.AdoptResult{OperationID: "op-a", Created: true,
		Binding: bindings.Binding{ID: "bnd-7", MailName: "GreenHill", PaneID: "w1:p5"}}}
	a := mustAction(t, "agent.adopt")
	req := Request{Context: ctxWS, Targets: []ui.Target{loose}, OperationID: "op-a", Params: map[string]string{ParamMailName: " GreenHill "}}
	res, err := a.Do(context.Background(), f.core(), req)
	got := f.adopted[0]
	if err != nil || got.MailName != "GreenHill" || got.PaneID != "w1:p5" || got.Kind != "claude" || got.ExpectedSession != "s-5" ||
		got.OperationID != "op-a" || got.HandoffFrom != "" {
		t.Fatalf("adopt request = %+v (%v)", got, err)
	}
	if res.Status != "done" || res.Targets[0].Status != "adopted" || res.Targets[0].Target != loose || res.Lines[0] != "GreenHill bound to binding bnd-7 in pane w1:p5" {
		t.Fatalf("result = %+v", res)
	}
	// No name and no hand-over: refused before the core.
	req.Params = map[string]string{}
	if _, err := a.Do(context.Background(), f.core(), req); !errors.Is(err, ErrNoMailName) || len(f.adopted) != 1 {
		t.Fatalf("no name: %v", err)
	}
	// The core's refusal is the target's, in its words.
	f.adoptRes = bindings.AdoptResult{OperationID: "op-b"}
	f.err = fmt.Errorf("%w: the agent in w1:p5 has session s-6, the review saw s-5", bindings.ErrAgentChanged)
	req.Params, req.OperationID = map[string]string{ParamMailName: "GreenHill"}, "op-b"
	res, err = a.Do(context.Background(), f.core(), req)
	if !errors.Is(err, bindings.ErrAgentChanged) || res.Status != "refused" || res.Targets[0].Status != "refused" {
		t.Fatalf("changed agent = %+v %v", res, err)
	}
	// A bound agent is not adopt's.
	req.Targets = []ui.Target{bound1}
	if _, err := a.Do(context.Background(), f.core(), req); !errors.Is(err, ErrTargets) {
		t.Fatalf("a bound agent: %v", err)
	}
	// Neither is an agent herdr reports no session for: nothing would tell
	// it from another agent that starts in its pane before the adopt.
	req.Targets = []ui.Target{{PaneID: "w1:p6", Kind: "claude"}}
	if _, err := a.Do(context.Background(), f.core(), req); !errors.Is(err, ErrTargets) || !strings.Contains(err.Error(), "nhm adopt") ||
		len(f.adopted) != 2 {
		t.Fatalf("an agent without a session: %v (adopts %d)", err, len(f.adopted))
	}
}
