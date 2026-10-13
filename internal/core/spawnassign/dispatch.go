package spawnassign

// nhm's topology, observation and delivery for the assignment loop, in
// place of ntm's tmux panes, session observer and dispatch service
// (nhm-i85, ACFS acfs-f2mc.2, port-boundary-v2 "Required
// assignment/transport attempt contract").

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/redaction"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

// DeliveryProtocol names nhm's delivery in a dispatch receipt id.
const DeliveryProtocol = "herdr.agent.prompt"

// Topology is the workspace's verified occupants and their dispatch-time
// observation.
type Topology struct {
	Bindings    *bindings.Store
	Herdr       send.Herdr
	WorkspaceID string
	ProjectKey  string
}

// ListOccupants lists the workspace's active bindings of the project.
func (t Topology) ListOccupants(ctx context.Context, _ string) ([]Occupant, error) {
	list, err := t.Bindings.List(ctx, bindings.Filter{WorkspaceID: t.WorkspaceID, ProjectKey: t.ProjectKey,
		Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	out := make([]Occupant, 0, len(list))
	for _, b := range list {
		out = append(out, Occupant{BindingID: b.ID, Generation: b.Generation, Slot: b.Slot, PaneID: b.PaneID,
			MailName: b.MailName, Kind: b.Kind})
	}
	return out, nil
}

// ObserveOccupant reports whether the occupant is safe to dispatch to now:
// its binding is still active at the same generation and pane in the
// workspace, and herdr's agent there passes send's delivery gate (ready).
// The send re-checks all of it under the target's lock before the prompt.
func (t Topology) ObserveOccupant(ctx context.Context, o Occupant) (bool, error) {
	cur, err := t.Bindings.Get(ctx, o.BindingID)
	if errors.Is(err, bindings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cur.Status != bindings.Active || cur.Generation != o.Generation || cur.PaneID != o.PaneID || cur.WorkspaceID != t.WorkspaceID {
		return false, nil
	}
	a, err := t.Herdr.AgentGet(ctx, cur.PaneID)
	if err != nil {
		if herdr.ErrorCode(err) != "" {
			return false, nil
		}
		return false, err
	}
	if a.PaneID != cur.PaneID || (a.WorkspaceID != "" && a.WorkspaceID != cur.WorkspaceID) {
		return false, nil
	}
	gate, _ := send.DeliveryGate(cur, &a)
	return gate == send.GateReady, nil
}

// SendPort delivers an assignment prompt through nhm's send core, the one
// send path: one operation per attempt ("assign:<key>:<attempt>"), the
// target frozen at its binding and generation (Expect), the gate and the
// target lock of every send, and its receipts as the proof of what
// happened. The send enriches nothing itself: the prompt is the recorded
// one, and a context prepared for it (Prepare) is recorded with it.
type SendPort struct {
	Env         send.Env
	Session     string
	WorkspaceID string
	ProjectRoot string
	ProjectKey  string
	// PreparedDir keeps the prepared work prompt contexts (prepared.go)
	// until their delivery; "" prepares none.
	PreparedDir string
}

// PreparedDir is the instance's directory of prepared work prompt contexts,
// beside its assignment ledger.
func PreparedDir(instanceID string) string {
	return filepath.Join(assignment.StorageDir(), instanceID, "prepared")
}

// OperationID is the send operation of one assignment attempt, in the
// store's operation id alphabet ([A-Za-z0-9._:-], at most 128 bytes; the
// key is 64 hex digits).
func OperationID(idempotencyKey string, attempt int) string {
	return fmt.Sprintf("assign:%s:%d", idempotencyKey, attempt)
}

// Preflight applies ntm's default redaction policy ("warn"): a secret in the
// title or a reservation path is refused, the prompt goes out as the caller
// composed it, and the ledger keeps redacted copies of prompt and title.
func (p SendPort) Preflight(_ context.Context, req assignment.DispatchRequest) (assignment.PromptPreflightResult, error) {
	for _, path := range req.RequestedPaths {
		if _, n := redaction.Redact(path); n > 0 {
			return assignment.PromptPreflightResult{}, fmt.Errorf("assignment reservation path blocked by redaction policy (%d findings)", n)
		}
	}
	durablePrompt, _ := redaction.Redact(req.Prompt)
	durableTitle, _ := redaction.Redact(req.BeadTitle)
	return assignment.PromptPreflightResult{DispatchPrompt: req.Prompt, DurablePrompt: durablePrompt, DurableTitle: durableTitle}, nil
}

// Dispatch sends one attempt and maps its receipt: submitted is delivery;
// refused and failed are written only when nothing was typed (the gate, the
// lock, a changed generation or pane, a dial failure), so they guarantee no
// actuation; every other state is unknown. A failed send guarantees no
// actuation only when the store has no operation of the attempt's id: the
// send types only after that operation is committed. Any other failure (a
// replay, a stored operation that does not read, a claim another process
// holds) is unknown. A nil error alone proves nothing.
func (p SendPort) Dispatch(ctx context.Context, req assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
	started := time.Now()
	receipt := assignment.DispatchReceipt{}
	bindingID, generation, err := ParseTarget(req.Target)
	if err != nil {
		receipt.Duration = time.Since(started)
		return receipt, assignment.GuaranteeNoActuation(err)
	}
	if req.Attempt < 1 {
		receipt.Duration = time.Since(started)
		return receipt, assignment.GuaranteeNoActuation(errors.New("assignment dispatch has no persisted attempt"))
	}
	opID := OperationID(req.IdempotencyKey, req.Attempt)
	// The CLI send's scope keys: feedback reads the bead in project_root.
	scope := map[string]any{"workspace_id": p.WorkspaceID, "project_root": p.ProjectRoot, "mail_project_key": p.ProjectKey,
		"assignment": map[string]any{"bead_id": req.BeadID, "idempotency_key": req.IdempotencyKey, "attempt": req.Attempt}}
	// The context prepared for this intent, if any, is recorded with the
	// delivery. One that cannot be read leaves the delivery without its
	// record (the prompt itself is fixed in the ledger), and the operation
	// says why.
	prepared, preparedErr := readPrepared(p.PreparedDir, req.IdempotencyKey, req.Prompt)
	if preparedErr != nil {
		scope["prepared_error"] = preparedErr.Error()
	}
	res, sendErr := send.Send(ctx, p.Env, send.Request{
		Session: p.Session, WorkspaceID: p.WorkspaceID, ProjectRoot: p.ProjectRoot, ProjectKey: p.ProjectKey,
		Selector: send.Selector{Bindings: []string{bindingID}}, Message: req.Prompt,
		OperationID: opID, Expect: map[string]int64{bindingID: generation},
		Source: history.SourceRobot, Scope: scope, Prepared: prepared,
	})
	receipt.Duration = time.Since(started)
	if sendErr != nil {
		if res.OperationID == "" && operationAbsent(ctx, p.Env.DB, opID) {
			return receipt, assignment.GuaranteeNoActuation(sendErr)
		}
		return receipt, fmt.Errorf("assignment send %s: %w", opID, sendErr)
	}
	if res.InProgress {
		return receipt, fmt.Errorf("assignment send %s has no final outcome yet", res.OperationID)
	}
	var target *send.Receipt
	for i := range res.Targets {
		if res.Targets[i].BindingID == bindingID {
			if target != nil {
				return receipt, fmt.Errorf("assignment send %s has two receipts for binding %s", res.OperationID, bindingID)
			}
			target = &res.Targets[i]
		}
	}
	if target == nil {
		return receipt, fmt.Errorf("assignment send %s has no receipt for binding %s", res.OperationID, bindingID)
	}
	switch target.Status {
	case send.StatusSubmitted:
		receipt.DeliveryID = assignment.DispatchDeliveryID(req.Target, DeliveryProtocol, req.IdempotencyKey)
		// The operation holds the exposure now, or says why it could not
		// (prepared_error); no later attempt sends this intent, so the kept
		// context goes either way (nhm-nca).
		removePrepared(p.PreparedDir, req.IdempotencyKey)
		return receipt, nil
	case send.StatusRefused, send.StatusFailed:
		return receipt, assignment.GuaranteeNoActuation(fmt.Errorf("%s (%s): %s", target.Status, target.ErrorCode, target.Result.Reason))
	}
	return receipt, fmt.Errorf("assignment send %s: submission %s is not known", res.OperationID, target.Status)
}

// operationAbsent reports whether the store has no operation id: read on a
// context that a cancelled send does not end, so a cancellation before the
// claim still proves that nothing was sent. A failed read is not absence.
func operationAbsent(ctx context.Context, db *store.DB, id string) bool {
	if db == nil {
		return false
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := db.Operation(lctx, id)
	return errors.Is(err, sql.ErrNoRows)
}
