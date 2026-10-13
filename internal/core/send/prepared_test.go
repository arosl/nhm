package send

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/herdr"
)

// A send with a prepared enrichment (an assignment's work prompt, its block
// already in the message) retrieves nothing, records the exposure under its
// own operation with the bead, and the submitted receipt makes its items
// given in the target's context (nhm-i85).
func TestSendRecordsAPreparedEnrichment(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	f := &memCM{rules: twoRules}
	w.env.CM = f
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	retr := enrich.NewRetriever(f, nil, w.env.Budgets, time.Now()).Retrieve(ctx,
		enrich.Request{Query: "Fix the login bug.", CM: enrich.BestEffort})
	cg := enrich.ContextGeneration(b)
	plan := enrich.PlanFor(retr, nil, 0)
	ex := enrich.Exposure{BindingID: b.ID, Generation: b.Generation, ContextGeneration: cg, BeadID: "bd-1", Plan: plan, Retrieval: retr}
	calls := f.calls
	message := enrich.Frame(plan.Block, "Work on bead bd-1")
	req := base(Request{Message: message, OperationID: "assign:k:1", Selector: Selector{Bindings: []string{b.ID}},
		Expect: map[string]int64{b.ID: b.Generation}, Prepared: &ex,
		Scope: map[string]any{"project_root": "/p"}})
	res, err := Send(ctx, w.env, req)
	if err != nil || len(res.Targets) != 1 || res.Targets[0].Status != StatusSubmitted {
		t.Fatalf("send = %+v %v", res, err)
	}
	if f.calls != calls {
		t.Fatalf("the send queried CM again: %d calls", f.calls-calls)
	}
	if got := w.fake.prompts; len(got) != 1 || got[0].pane != "w1:p1" || got[0].text != message {
		t.Fatalf("prompts = %+v, want the prepared message as it is", got)
	}
	if !res.MemoryInjection.Enabled || len(res.Injections) != 1 || len(res.Injections[0].Items) != 2 {
		t.Fatalf("injections = %+v %+v", res.MemoryInjection, res.Injections)
	}
	got, err := enrich.List(ctx, w.env.DB, enrich.Filter{SubmittedOnly: true, BeadID: "bd-1"})
	if err != nil || len(got) != 1 || got[0].OperationID != "assign:k:1" || got[0].BindingID != b.ID || got[0].ProjectRoot != "/p" ||
		strings.Join(got[0].RuleIDs(), ",") != "b-1,b-2" {
		t.Fatalf("received = %+v %v", got, err)
	}
	given, err := enrich.Given(ctx, w.env.DB, b.ID, cg)
	if err != nil || len(given) != 2 {
		t.Fatalf("given = %v %v", given, err)
	}
}

// A prepared enrichment is for its one binding at its generation, and
// takes no enrichment of the send's own: anything else refuses before the
// claim, so nothing is sent.
func TestSendRefusesAMismatchedPreparedEnrichment(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	other := w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	ex := enrich.Exposure{BindingID: b.ID, Generation: b.Generation, BeadID: "bd-1"}
	for name, req := range map[string]Request{
		"other binding": base(Request{Message: "m", OperationID: "assign:k:1", Selector: Selector{Bindings: []string{other.ID}}, Prepared: &ex}),
		"two targets":   base(Request{Message: "m", OperationID: "assign:k:2", Selector: Selector{Bindings: []string{b.ID, other.ID}}, Prepared: &ex}),
		"own enrichment": base(Request{Message: "m", OperationID: "assign:k:3", Selector: Selector{Bindings: []string{b.ID}}, Prepared: &ex,
			Enrich: Enrichment{CM: enrich.BestEffort}}),
	} {
		res, err := Send(ctx, w.env, req)
		if err == nil || res.OperationID != "" {
			t.Fatalf("%s: send = %+v %v", name, res, err)
		}
		if _, err := w.env.DB.Operation(ctx, req.OperationID); err == nil {
			t.Fatalf("%s: the operation was claimed", name)
		}
	}
	stale := ex
	stale.Generation++
	if _, err := Send(ctx, w.env, base(Request{Message: "m", OperationID: "assign:k:4", Selector: Selector{Bindings: []string{b.ID}},
		Prepared: &stale})); err == nil {
		t.Fatal("a prepared enrichment of another generation was accepted")
	}
	if n := w.fake.promptCount(); n != 0 {
		t.Fatalf("%d prompts were typed", n)
	}
}
