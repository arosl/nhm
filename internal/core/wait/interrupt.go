package wait

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// InterruptKeys returns a kind's interrupt key from the kind registry
// (plan-v8 2.6, 2.14), or nil when nhm has no checked key for the kind:
// then nhm refuses instead of guessing.
func InterruptKeys(kind string) []string { return kinds.Lookup(kind).InterruptKeys }

// Interrupt statuses.
const (
	InterruptSent    = "sent"
	InterruptRefused = "refused"
	InterruptFailed  = "failed"
	// InterruptSkipped: SkipResting left an agent at its prompt alone.
	InterruptSkipped = "skipped"
	// InterruptWouldSend: a dry run would send the key.
	InterruptWouldSend = "would_send"
)

// VerbInterrupt is the operation verb.
const VerbInterrupt = "interrupt"

// InterruptTarget is one agent's interrupt.
type InterruptTarget struct {
	BindingID   string            `json:"binding_id"`
	MailName    string            `json:"mail_name"`
	PaneID      string            `json:"pane_id"`
	Slot        string            `json:"slot,omitempty"`
	Kind        string            `json:"kind"`
	Generation  int64             `json:"generation"`
	Keys        []string          `json:"keys,omitempty"`
	StateBefore herdr.AgentStatus `json:"state_before,omitempty"`
	Status      string            `json:"status"`
	Reason      string            `json:"reason,omitempty"`
}

// InterruptResult is an interrupt operation.
type InterruptResult struct {
	OperationID string            `json:"operation_id"`
	Existing    bool              `json:"existing"`
	Status      string            `json:"status"`
	Targets     []InterruptTarget `json:"targets"`
}

// InterruptRequest names the targets. OperationID is --op-id.
type InterruptRequest struct {
	OperationID string
	Scope       any
	Targets     []bindings.Binding
	// Expect freezes the targets a caller reviewed: binding id to the
	// generation it saw. When it is set, a target whose binding is now at
	// another generation, or that is not in it, is refused before its key
	// is sent, and an expected binding missing from Targets gets a
	// refused entry. Nil keeps today's check (the generation of Targets).
	// Interrupt takes no target lock: it must reach an agent that a send
	// or a recycle holds; the check runs just before the key.
	Expect map[string]int64
	// SkipResting is ntm's robot interrupt without --force (nhm-hir): an
	// agent at its prompt (herdr's idle or done) gets no key, and its
	// status is skipped.
	SkipResting bool
	// DryRun checks each target as for an interrupt, but claims no
	// operation and sends no key: a target that would get its key is
	// would_send.
	DryRun bool
}

// Interrupt refusal reasons of Expect.
const (
	ReasonNotReviewed = "the agent was not in the reviewed set"
	ReasonNotSelected = "the agent is no longer among the targets"
)

// frozenTarget is one target as the claim records it; the claim's
// fingerprint covers the frozen targets.
type frozenTarget struct {
	BindingID  string `json:"binding_id"`
	Generation int64  `json:"generation"`
}

// Interrupt sends each target's interrupt key, once per operation id. A
// repeated operation id sends nothing and reports the stored status.
// Each target is checked again first: the same generation, an agent of
// the binding's kind in the pane, and a checked key for the kind.
func Interrupt(ctx context.Context, db *store.DB, h Herdr, bs Bindings, req InterruptRequest, now func() time.Time) (InterruptResult, error) {
	frozen := make([]frozenTarget, len(req.Targets))
	for i, b := range req.Targets {
		frozen[i] = frozenTarget{BindingID: b.ID, Generation: b.Generation}
	}
	request := map[string]any{"targets": frozen}
	if req.Expect != nil {
		request["expect"] = req.Expect
	}
	if req.SkipResting {
		request["skip_resting"] = true
	}
	if req.DryRun {
		res := InterruptResult{Status: "dry_run", Targets: []InterruptTarget{}}
		for _, b := range req.Targets {
			res.Targets = append(res.Targets, interruptOne(ctx, h, bs, b, req))
		}
		res.Targets = append(res.Targets, notSelected(req)...)
		return res, nil
	}
	claim, err := db.ClaimOperation(ctx, store.ClaimRequest{
		ID: req.OperationID, Verb: VerbInterrupt, Scope: req.Scope, Request: request,
	}, now())
	if err != nil {
		return InterruptResult{}, err
	}
	res := InterruptResult{OperationID: claim.Operation.ID, Targets: []InterruptTarget{}}
	if claim.Existing {
		res.Existing, res.Status = true, claim.Operation.Status
		return res, nil
	}
	// A skipped agent is as good as a sent one: it is at its prompt.
	ok, failed := 0, 0
	for _, b := range req.Targets {
		t := interruptOne(ctx, h, bs, b, req)
		switch t.Status {
		case InterruptSent, InterruptSkipped:
			ok++
		case InterruptFailed:
			failed++
		}
		res.Targets = append(res.Targets, t)
	}
	res.Targets = append(res.Targets, notSelected(req)...)
	switch {
	case ok == len(res.Targets):
		res.Status = "done"
	case ok > 0:
		res.Status = "partial"
	case failed > 0:
		res.Status = "failed"
	default:
		res.Status = "refused"
	}
	if err := db.SetOperationStatus(ctx, res.OperationID, res.Status, true, now()); err != nil {
		return res, err
	}
	return res, nil
}

// notSelected refuses each expected binding that is not a target.
func notSelected(req InterruptRequest) []InterruptTarget {
	var out []InterruptTarget
	for _, id := range missingExpected(req.Expect, req.Targets) {
		out = append(out, InterruptTarget{BindingID: id, Generation: req.Expect[id],
			Status: InterruptRefused, Reason: ReasonNotSelected})
	}
	return out
}

// missingExpected lists, sorted, the expected bindings not in targets.
func missingExpected(expect map[string]int64, targets []bindings.Binding) []string {
	var out []string
	for id := range expect {
		if !slices.ContainsFunc(targets, func(b bindings.Binding) bool { return b.ID == id }) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func interruptOne(ctx context.Context, h Herdr, bs Bindings, b bindings.Binding, req InterruptRequest) InterruptTarget {
	t := InterruptTarget{BindingID: b.ID, MailName: b.MailName, PaneID: b.PaneID, Slot: b.Slot, Kind: b.Kind, Generation: b.Generation}
	refuse := func(reason string) InterruptTarget {
		t.Status, t.Reason = InterruptRefused, reason
		return t
	}
	cur, err := bs.Get(ctx, b.ID)
	if err != nil {
		t.Status, t.Reason = InterruptFailed, err.Error()
		return t
	}
	if cur.Generation != b.Generation || cur.Status != bindings.Active || cur.PaneID != b.PaneID {
		return refuse(fmt.Sprintf("the binding changed since selection (generation %d, %s, pane %s)", cur.Generation, cur.Status, cur.PaneID))
	}
	if req.Expect != nil {
		want, ok := req.Expect[cur.ID]
		switch {
		case !ok:
			return refuse(ReasonNotReviewed)
		case want != cur.Generation:
			return refuse(fmt.Sprintf("expected generation %d, the binding is at generation %d", want, cur.Generation))
		}
	}
	t.Keys = InterruptKeys(cur.Kind)
	if t.Keys == nil {
		return refuse("nhm has no checked interrupt key for kind " + cur.Kind)
	}
	a, err := h.AgentGet(ctx, cur.PaneID)
	if err != nil {
		if code := herdr.ErrorCode(err); code != "" {
			return refuse("herdr: " + code)
		}
		t.Status, t.Reason = InterruptFailed, err.Error()
		return t
	}
	t.StateBefore = a.AgentStatus
	if a.Agent != "" && a.Agent != cur.Kind {
		return refuse(fmt.Sprintf("the pane holds a %s agent; the binding is %s", a.Agent, cur.Kind))
	}
	if req.SkipResting && (a.AgentStatus == herdr.StatusIdle || a.AgentStatus == herdr.StatusDone) {
		t.Keys, t.Status, t.Reason = nil, InterruptSkipped, "the agent is at its prompt"
		return t
	}
	if req.DryRun {
		t.Status = InterruptWouldSend
		return t
	}
	if err := h.AgentSendKeys(ctx, cur.PaneID, t.Keys...); err != nil {
		// A transport error after the write may still have delivered the
		// key; an interrupt is safe to repeat by hand.
		t.Status, t.Reason = InterruptFailed, err.Error()
		return t
	}
	t.Status = InterruptSent
	return t
}
