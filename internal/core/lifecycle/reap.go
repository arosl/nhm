package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// IdleFile tracks since when nhm sees each binding between turns.
const IdleFile = "reap_idle.json"

// ReapRequest is one reap sweep.
type ReapRequest struct {
	Idle time.Duration
	// WorkspaceID limits the sweep; AllWorkspaces spans the instance.
	WorkspaceID   string
	AllWorkspaces bool
	ProjectKey    string
	Keep          []string
	DryRun        bool
	// Only limits the sweep to a reviewed set: the would_retire items of
	// a dry run (the menu). Reap then looks at no other binding, and
	// refuses one that is gone or at another generation; every other
	// check runs again. nil sweeps every binding in scope; an empty set
	// is refused.
	Only []ReapTarget
	// OperationID is the id of the record of a sweep over a reviewed set
	// (the menu's confirm). Reap records the operation and a pending
	// receipt per reviewed agent before the first retire, runs each retire
	// as its child <id>.<index>, and records each agent's outcome as it
	// happens. An id claimed before is refused. A dry run records nothing.
	OperationID string
}

// ReapTarget is one agent of a reviewed set: the binding and the
// generation the review saw.
type ReapTarget struct {
	BindingID  string `json:"binding_id"`
	Generation int64  `json:"generation"`
}

// ReapItem is one agent the sweep looked at.
type ReapItem struct {
	MailName  string `json:"mail_name"`
	BindingID string `json:"binding_id"`
	// Generation is the binding's generation as the sweep saw it.
	Generation int64         `json:"generation"`
	PaneID     string        `json:"pane_id,omitempty"`
	IdleFor    time.Duration `json:"idle_ns"`
	// Action: retired, would_retire, kept, refused; failed: the retire
	// failed after its claim and its checks, so the pane may be closed,
	// and its record (Retire.OperationID) says how far it got; skipped: a
	// stopped sweep did not reach it.
	Action string        `json:"action"`
	Reason string        `json:"reason,omitempty"`
	Retire *RetireResult `json:"retire,omitempty"`
}

// MailState says whether an agent has unread mail or pending acks.
type MailState func(ctx context.Context, project, agent string) (unread, acks int, err error)

// idleEntry is one binding's idle observation: the state_change_seq it
// was seen idle at, and since when. A new seq starts it over.
type idleEntry struct {
	Seq   uint64    `json:"seq"`
	Since time.Time `json:"since"`
}

// Reap retires the agents idle past the limit whose work is done
// (plan-v8 2.10): retire's checks for each, over the candidates. It skips
// the focused and the calling pane, the keep list, agents with unread mail
// or pending acks, agents with beads in progress, and any binding in an
// uncertain state. An idle time is how long nhm has seen the agent
// between turns at the same state_change_seq; a change starts it over.
func Reap(ctx context.Context, env Env, mail MailState, req ReapRequest) ([]ReapItem, error) {
	if !req.AllWorkspaces && req.WorkspaceID == "" {
		return nil, errors.New("name the session, or give --all-workspaces")
	}
	if req.Only != nil && len(req.Only) == 0 {
		return nil, fmt.Errorf("%w: the reviewed set is empty: nothing to reap", ErrRefused)
	}
	// index is each reviewed agent's receipt.
	index := map[string]int{}
	for i, t := range req.Only {
		if _, dup := index[t.BindingID]; dup {
			return nil, fmt.Errorf("%w: the reviewed set names %s twice", ErrRefused, t.BindingID)
		}
		index[t.BindingID] = i
	}
	record := req.OperationID != "" && !req.DryRun
	if record && req.Only == nil {
		return nil, errors.New("reap: a recorded sweep needs its reviewed set")
	}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("herdr: %w", err)
	}
	agents := map[string]herdr.AgentInfo{}
	for _, a := range snap.Agents {
		agents[a.PaneID] = a
	}
	f := bindings.Filter{ProjectKey: req.ProjectKey, Statuses: []bindings.Status{bindings.Active}}
	if !req.AllWorkspaces {
		f.WorkspaceID = req.WorkspaceID
	}
	list, err := env.Bindings.List(ctx, f)
	if err != nil {
		return nil, err
	}
	var items, gone []ReapItem
	reviewed := map[string]int64{}
	if req.Only != nil {
		for _, t := range req.Only {
			reviewed[t.BindingID] = t.Generation
		}
		var in []bindings.Binding
		for _, b := range list {
			if _, ok := reviewed[b.ID]; ok {
				in = append(in, b)
			}
		}
		// A reviewed binding the sweep no longer finds is refused by name,
		// never swapped for another.
		for _, t := range req.Only {
			if slices.ContainsFunc(in, func(b bindings.Binding) bool { return b.ID == t.BindingID }) {
				continue
			}
			item := ReapItem{BindingID: t.BindingID, Generation: t.Generation, Action: "refused",
				Reason: "it is no longer an active binding in this sweep's scope"}
			if b, err := env.Bindings.Get(ctx, t.BindingID); err == nil {
				item.MailName, item.PaneID = b.MailName, b.PaneID
			}
			gone = append(gone, item)
		}
		list = in
	}
	idlePath := filepath.Join(env.InstanceDir, IdleFile)
	idle := readIdle(idlePath)
	// The record's writes outlive a cancelled caller: a record left open
	// would read as still running.
	rctx := context.WithoutCancel(ctx)
	names := map[string]string{}
	for _, b := range list {
		names[b.ID] = b.MailName
	}
	for _, item := range gone {
		names[item.BindingID] = item.MailName
	}
	// add keeps an agent's outcome, and records it in a recorded sweep.
	reached := map[string]bool{}
	add := func(item ReapItem) error {
		items = append(items, item)
		reached[item.BindingID] = true
		if !record {
			return nil
		}
		return reapReceipt(rctx, env, req.OperationID, index[item.BindingID], item)
	}
	if record {
		if err := claimReap(rctx, env, req, names); err != nil {
			return nil, fmt.Errorf("reap: record the operation: %w", err)
		}
	}
	// stop ends a sweep that cannot go on. Each reviewed agent it did not
	// reach is "skipped", so every receipt ends final; the record ends
	// "stopped", and the idle file drops the agents the sweep retired.
	stop := func(err error) ([]ReapItem, error) {
		for _, t := range req.Only {
			if reached[t.BindingID] {
				continue
			}
			item := ReapItem{MailName: names[t.BindingID], BindingID: t.BindingID, Generation: t.Generation,
				Action: "skipped", Reason: "not tried: the sweep stopped first"}
			items = append(items, item)
			if record {
				_ = reapReceipt(rctx, env, req.OperationID, index[t.BindingID], item)
			}
		}
		if record {
			_ = env.DB.SetOperationStatus(rctx, req.OperationID, ReapStopped, true, env.now())
		}
		_ = writeIdle(idlePath, idle)
		sort.SliceStable(items, func(i, j int) bool { return items[i].MailName < items[j].MailName })
		return items, err
	}
	for _, item := range gone {
		if err := add(item); err != nil {
			return stop(fmt.Errorf("reap: record %s: %w", item.BindingID, err))
		}
	}
	now := env.now()
	seen := map[string]bool{}
	keep := map[string]bool{}
	for _, k := range req.Keep {
		keep[strings.ToLower(k)] = true
	}
	for _, b := range list {
		item := ReapItem{MailName: b.MailName, BindingID: b.ID, Generation: b.Generation, PaneID: b.PaneID, Action: "kept"}
		if want, ok := reviewed[b.ID]; ok && want != b.Generation {
			item.Action = "refused"
			item.Reason = fmt.Sprintf("it changed since it was reviewed: expected generation %d, the binding is at generation %d", want, b.Generation)
			if err := add(item); err != nil {
				return stop(fmt.Errorf("reap: record %s: %w", b.MailName, err))
			}
			continue
		}
		a, ok := agents[b.PaneID]
		switch {
		case !ok:
			item.Reason = "herdr has no agent in its pane (an uncertain state)"
		case a.AgentStatus != herdr.StatusIdle && a.AgentStatus != herdr.StatusDone:
			item.Reason = "it is " + string(a.AgentStatus)
		case a.StateChangeSeq == nil:
			item.Reason = "herdr gives no state_change_seq"
		}
		if item.Reason != "" {
			if err := add(item); err != nil {
				return stop(fmt.Errorf("reap: record %s: %w", b.MailName, err))
			}
			continue
		}
		seen[b.ID] = true
		e, had := idle[b.ID]
		if !had || e.Seq != *a.StateChangeSeq {
			e = idleEntry{Seq: *a.StateChangeSeq, Since: now}
			idle[b.ID] = e
		}
		item.IdleFor = now.Sub(e.Since)
		switch {
		case item.IdleFor < req.Idle:
			item.Reason = fmt.Sprintf("between turns for %s, under %s", item.IdleFor.Round(time.Second), req.Idle)
		case a.Focused:
			item.Reason = "its pane is focused"
		case env.Caller != "" && b.PaneID == env.Caller:
			item.Reason = "it runs the reap"
		case keep[strings.ToLower(b.MailName)]:
			item.Reason = "the keep list holds it"
		}
		if item.Reason == "" && mail != nil {
			unread, acks, err := mail(ctx, b.MailProjectKey, b.MailName)
			switch {
			case err != nil:
				item.Reason = "its mail cannot be read: " + err.Error()
			case unread > 0 || acks > 0:
				item.Reason = fmt.Sprintf("it has %d unread messages and %d pending acks", unread, acks)
			}
		}
		if item.Reason == "" {
			beads, err := env.Facts.Assigned(ctx, b.MailProjectKey, b.MailName)
			switch {
			case err != nil:
				item.Reason = "its beads cannot be read: " + err.Error()
			case len(beads) > 0:
				item.Reason = "it has work in progress: " + beads[0].ID
			}
		}
		if item.Reason != "" {
			if err := add(item); err != nil {
				return stop(fmt.Errorf("reap: record %s: %w", b.MailName, err))
			}
			continue
		}
		// A reviewer works no beads: only an agent spawned with --role
		// reviewer may have none in the databases searched. Retire checks
		// the generation the sweep saw under its lock. In a recorded sweep
		// each retire is the sweep's child.
		rreq := RetireRequest{Target: b.ID, KeepIdentity: false, DryRun: req.DryRun, Reason: "reap",
			NoBeads: b.Role == spawn.RoleReviewer, ExpectedGeneration: b.Generation}
		if record {
			rreq.OperationID = store.ChildOperationID(req.OperationID, index[b.ID])
		}
		rr, err := Retire(ctx, env, rreq)
		r := rr
		item.Retire = &r
		switch {
		case err != nil && rr.Done:
			// The retire is done; only its record's last write failed.
			item.Action, item.Reason = "retired", "its record: "+err.Error()
			delete(idle, b.ID)
		case errors.Is(err, ErrRefused):
			item.Action, item.Reason = "refused", strings.Join(rr.Refusals, "; ")
		case err != nil && rr.OperationID != "":
			// It failed after its claim and its checks: the pane may be
			// closed.
			item.Action, item.Reason = "failed", err.Error()
		case err != nil:
			item.Action, item.Reason = "refused", err.Error()
		case req.DryRun:
			item.Action = "would_retire"
		default:
			item.Action = "retired"
			delete(idle, b.ID)
		}
		if err := add(item); err != nil {
			return stop(fmt.Errorf("reap: record %s: %w", b.MailName, err))
		}
		// A recorded sweep stops at a failed retire, as kill does: its
		// record and the receipts say how far it got.
		if record && item.Action == "failed" {
			return stop(fmt.Errorf("reap: %s: %w; the sweep stops", b.MailName, err))
		}
	}
	// Keep the entries of other workspaces; drop those not seen idle.
	for id := range idle {
		if !seen[id] && slices.ContainsFunc(list, func(b bindings.Binding) bool { return b.ID == id }) {
			delete(idle, id)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].MailName < items[j].MailName })
	// Every receipt is final: the record is done, whatever the idle file
	// does.
	var doneErr error
	if record {
		if err := env.DB.SetOperationStatus(rctx, req.OperationID, ReapDone, true, env.now()); err != nil {
			doneErr = fmt.Errorf("reap: the sweep is done, but its record is not: %w", err)
		}
	}
	if err := writeIdle(idlePath, idle); err != nil {
		return items, errors.Join(doneErr, err)
	}
	return items, doneErr
}

func readIdle(path string) map[string]idleEntry {
	m := map[string]idleEntry{}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

func writeIdle(path string, m map[string]idleEntry) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, data, 0o600)
}

// LoadKeep reads the reap keep list: [reap] keep in nhm's config, and in
// ACFS's agents.toml (with its old key, protected) during the migration.
// A file that cannot be read, or holds another key under [reap], fails,
// so that reap retires nobody.
func LoadKeep(paths ...string) ([]string, error) {
	var names []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var f struct {
			Reap map[string]any `toml:"reap"`
		}
		if _, err := toml.Decode(string(data), &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for key, v := range f.Reap {
			if key != "keep" && key != "protected" {
				return nil, fmt.Errorf("%s: [reap] takes only keep, not %s", p, key)
			}
			list, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: reap.%s must be a list of names", p, key)
			}
			for _, n := range list {
				s, ok := n.(string)
				if !ok {
					return nil, fmt.Errorf("%s: reap.%s must be a list of names", p, key)
				}
				names = append(names, s)
			}
		}
	}
	return names, nil
}

// LoadBeadDBs reads [lifecycle] bead_dbs from nhm's config: the bead
// databases retire and reap search besides a project's own, for agents
// whose beads live in another repository. A missing file has none; an
// unknown [lifecycle] key or a relative path fails.
func LoadBeadDBs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Lifecycle map[string]any `toml:"lifecycle"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var dbs []string
	for key, v := range f.Lifecycle {
		list, ok := v.([]any)
		if key != "bead_dbs" || !ok {
			return nil, fmt.Errorf("%s: [lifecycle] takes only bead_dbs, a list of paths", path)
		}
		for _, x := range list {
			s, ok := x.(string)
			if !ok || !filepath.IsAbs(s) {
				return nil, fmt.Errorf("%s: lifecycle.bead_dbs takes absolute paths", path)
			}
			dbs = append(dbs, s)
		}
	}
	return dbs, nil
}

// KillResult is a kill of a workspace.
type KillResult struct {
	// OperationID is the kill's operation record, claimed after the
	// checks and before the first effect; empty when nothing ran.
	OperationID string         `json:"operation_id,omitempty"`
	WorkspaceID string         `json:"workspace_id"`
	Agents      []RetireResult `json:"agents"`
	// Refusals are the checks' refusals; empty when the checks pass. A
	// kill without yes that passes its checks has none.
	Refusals []string `json:"refusals"`
	Closed   bool     `json:"closed"`
}

// KillRequest is one kill of a workspace.
type KillRequest struct {
	WorkspaceID string
	// Yes closes the workspace; without it kill only reports.
	Yes bool
	// NoBeads passes retire's --no-beads to every agent: the workspace
	// holds agents that worked no beads (reviewers).
	NoBeads bool
	// OperationID is the id of the kill's record (--op-id); "" is a new
	// one. A caller that starts the kill in another process (the menu)
	// picks it first, to find the record. An id claimed before is
	// refused: kill never runs twice under one id.
	OperationID string
}

// VerbKill is the operation verb of a workspace kill; each agent's
// retire is its own operation, with verb "kill" and the id
// <kill operation>.<index>.
const VerbKill = "kill_workspace"

// Kill operation and receipt statuses. A kill that stops while
// "dispatching" or "closing" died partway: its receipts say how far it
// got (RecentOperations, admin recover inspect).
const (
	KillDispatching = "dispatching"
	KillClosing     = "closing"
	KillClosed      = "closed"
	KillStopped     = "stopped"
	KillRetired     = "retired"
	KillRefused     = "refused"
	KillFailed      = "failed"
)

// VerbReap is the operation verb of a recorded sweep over a reviewed set;
// each agent's retire is its own operation, with verb "reap" and the id
// <reap operation>.<index>.
const VerbReap = "reap_set"

// Reap operation statuses. A sweep that stops while "dispatching" died
// partway: its receipts say how far it got. A receipt's status is its
// agent's action: retired, refused, kept, failed (the retire failed after
// its claim and its checks), or skipped (a stopped sweep did not reach
// it), and pending until then.
const (
	ReapDispatching = "dispatching"
	ReapDone        = "done"
	ReapStopped     = "stopped"
)

// claimReap records a sweep over a reviewed set, and one pending receipt
// per reviewed agent, before any effect. An id claimed before refuses:
// its record shows how that sweep went.
func claimReap(ctx context.Context, env Env, req ReapRequest, names map[string]string) error {
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: VerbReap,
		Scope:   map[string]string{"workspace_id": req.WorkspaceID},
		Request: map[string]any{"workspace_id": req.WorkspaceID, "idle_ns": req.Idle, "targets": req.Only}}, env.now())
	if errors.Is(err, store.ErrOperationIDConflict) {
		return fmt.Errorf("%w: %w; reap does not run it again", ErrRefused, err)
	}
	if err != nil {
		return err
	}
	if claim.Existing {
		return fmt.Errorf("%w: operation %s exists (%s); reap does not run it again", ErrRefused, claim.Operation.ID, claim.Operation.Status)
	}
	// A new record that cannot get its receipts ends stopped: nothing ran.
	stop := func(err error) error {
		_ = env.DB.SetOperationStatus(ctx, req.OperationID, ReapStopped, true, env.now())
		return err
	}
	now := store.FormatTime(env.now())
	for i, t := range req.Only {
		js, err := json.Marshal(map[string]string{"mail_name": names[t.BindingID]})
		if err != nil {
			return stop(err)
		}
		if _, err := env.DB.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation,
			status, result_json, updated_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)`, req.OperationID, i, t.BindingID, t.Generation, string(js), now); err != nil {
			return stop(err)
		}
	}
	if err := env.DB.SetOperationStatus(ctx, req.OperationID, ReapDispatching, false, env.now()); err != nil {
		return stop(err)
	}
	return nil
}

// reapReceipt records one reviewed agent's outcome as it happens.
func reapReceipt(ctx context.Context, env Env, opID string, i int, item ReapItem) error {
	detail := map[string]any{"mail_name": item.MailName}
	if item.Reason != "" {
		detail["reason"] = item.Reason
	}
	if item.Retire != nil && item.Retire.OperationID != "" {
		detail["retire_operation"] = item.Retire.OperationID
	}
	js, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = env.DB.SQL().ExecContext(ctx, `UPDATE receipts SET status = ?, result_json = ?, updated_at = ?
		WHERE operation_id = ? AND target_index = ?`, item.Action, string(js), store.FormatTime(env.now()), opID, i)
	return err
}

// killReceipt records one agent's outcome as it happens.
func killReceipt(ctx context.Context, env Env, opID string, i int, status string, r RetireResult, rerr error) error {
	detail := map[string]any{"mail_name": r.MailName, "retire_operation": r.OperationID}
	if len(r.Refusals) > 0 {
		detail["refusals"] = r.Refusals
	}
	if rerr != nil {
		detail["error"] = rerr.Error()
	}
	js, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = env.DB.SQL().ExecContext(ctx, `UPDATE receipts SET status = ?, result_json = ?, updated_at = ?
		WHERE operation_id = ? AND target_index = ?`, status, string(js), store.FormatTime(env.now()), opID, i)
	return err
}

// claimKill records the kill and one pending receipt per agent before
// any effect, so the record outlives the process that runs it. An id
// claimed before refuses: its record shows how that kill went.
func claimKill(ctx context.Context, env Env, req KillRequest, list []bindings.Binding) (string, error) {
	type target struct {
		BindingID  string `json:"binding_id"`
		Generation int64  `json:"generation"`
	}
	targets := make([]target, len(list))
	for i, b := range list {
		targets[i] = target{b.ID, b.Generation}
	}
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: VerbKill,
		Scope:   map[string]string{"workspace_id": req.WorkspaceID},
		Request: map[string]any{"workspace_id": req.WorkspaceID, "targets": targets, "no_beads": req.NoBeads}}, env.now())
	if errors.Is(err, store.ErrOperationIDConflict) {
		return claim.Operation.ID, fmt.Errorf("%w: %w; kill does not run it again", ErrRefused, err)
	}
	if err != nil {
		return "", err
	}
	id := claim.Operation.ID
	if claim.Existing {
		return id, fmt.Errorf("%w: operation %s exists (%s); kill does not run it again", ErrRefused, id, claim.Operation.Status)
	}
	// A new record that cannot get its receipts ends stopped: nothing ran.
	stop := func(err error) (string, error) {
		_ = env.DB.SetOperationStatus(ctx, id, KillStopped, true, env.now())
		return id, err
	}
	now := store.FormatTime(env.now())
	for i, b := range list {
		js, err := json.Marshal(map[string]string{"mail_name": b.MailName})
		if err != nil {
			return stop(err)
		}
		if _, err := env.DB.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation,
			status, result_json, updated_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)`, id, i, b.ID, b.Generation, string(js), now); err != nil {
			return stop(err)
		}
	}
	if err := env.DB.SetOperationStatus(ctx, id, KillDispatching, false, env.now()); err != nil {
		return stop(err)
	}
	return id, nil
}

// Kill closes a workspace: KillWith with a new operation id.
func Kill(ctx context.Context, env Env, workspaceID string, yes, noBeads bool) (KillResult, error) {
	return KillWith(ctx, env, KillRequest{WorkspaceID: workspaceID, Yes: yes, NoBeads: noBeads})
}

// KillWith closes a workspace. It runs retire's checks for every bound
// agent first and never bypasses them: one refusal refuses the kill, and
// nothing is closed. Without Yes it only reports.
func KillWith(ctx context.Context, env Env, req KillRequest) (KillResult, error) {
	workspaceID, noBeads := req.WorkspaceID, req.NoBeads
	res := KillResult{WorkspaceID: workspaceID, Agents: []RetireResult{}, Refusals: []string{}}
	list, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: workspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return res, err
	}
	displaced, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: workspaceID, Statuses: []bindings.Status{bindings.Displaced}})
	if err != nil {
		return res, err
	}
	checked := map[string]bool{}
	for _, b := range list {
		checked[b.PaneID] = true
	}
	var refusals []string
	for _, b := range displaced {
		refusals = append(refusals, b.MailName+": its binding is displaced, so retire cannot check it; adopt or retire it first")
	}
	unchecked, err := uncheckedPanes(ctx, env, workspaceID, checked)
	if err != nil {
		res.Refusals = []string{"the workspace's panes cannot be read: " + err.Error()}
		return res, fmt.Errorf("%w: %s", ErrRefused, res.Refusals[0])
	}
	refusals = append(refusals, unchecked...)
	for _, b := range list {
		r, err := Retire(ctx, env, RetireRequest{Target: b.ID, DryRun: true, Reason: "kill", NoBeads: noBeads || b.Role == spawn.RoleReviewer})
		res.Agents = append(res.Agents, r)
		switch {
		case errors.Is(err, ErrRefused):
			refusals = append(refusals, b.MailName+": "+strings.Join(r.Refusals, "; "))
		case err != nil:
			refusals = append(refusals, b.MailName+": "+err.Error())
		}
	}
	if len(refusals) > 0 {
		res.Refusals = refusals
		return res, fmt.Errorf("%w: %s", ErrRefused, strings.Join(refusals, " | "))
	}
	if !req.Yes {
		return res, fmt.Errorf("%w: kill closes the workspace and retires its agents: give --yes", ErrRefused)
	}
	// The record's writes outlive a cancelled caller: a record left open
	// would read as still running.
	rctx := context.WithoutCancel(ctx)
	opID, err := claimKill(rctx, env, req, list)
	res.OperationID = opID
	if err != nil {
		return res, fmt.Errorf("kill: record the operation: %w", err)
	}
	stop := func(err error) (KillResult, error) {
		_ = env.DB.SetOperationStatus(rctx, opID, KillStopped, true, env.now())
		return res, err
	}
	res.Agents = res.Agents[:0]
	for i, b := range list {
		// The generation the checks saw: an agent recycled since is
		// refused, never retired unchecked.
		r, err := Retire(ctx, env, RetireRequest{Target: b.ID, Reason: "kill", NoBeads: noBeads || b.Role == spawn.RoleReviewer,
			OperationID: store.ChildOperationID(opID, i), ExpectedGeneration: b.Generation})
		res.Agents = append(res.Agents, r)
		status := KillRetired
		switch {
		case errors.Is(err, ErrRefused):
			status = KillRefused
		case err != nil:
			status = KillFailed
		}
		if werr := killReceipt(rctx, env, opID, i, status, r, err); werr != nil && err == nil {
			err = fmt.Errorf("record the outcome: %w", werr)
		}
		if err != nil {
			return stop(fmt.Errorf("%s: %w; the workspace stays open", b.MailName, err))
		}
	}
	// What started in the workspace while the agents were retired is not
	// closed unchecked either.
	unchecked, err = uncheckedPanes(ctx, env, workspaceID, nil)
	if err != nil || len(unchecked) > 0 {
		if err != nil {
			unchecked = append(unchecked, "the workspace's panes cannot be read: "+err.Error())
		}
		return stop(fmt.Errorf("%w: the agents are retired, the workspace stays open: %s", ErrRefused, strings.Join(unchecked, " | ")))
	}
	// Closing may end the process that runs the kill (a UI in the
	// workspace): the record says "closing" until the close returns.
	if err := env.DB.SetOperationStatus(rctx, opID, KillClosing, false, env.now()); err != nil {
		return stop(fmt.Errorf("kill: record the close: %w", err))
	}
	// herdr may have closed the workspace with its last pane.
	if err := env.Herdr.WorkspaceClose(ctx, workspaceID); err != nil && !strings.HasSuffix(herdr.ErrorCode(err), "not_found") {
		return stop(fmt.Errorf("close workspace %s: %w", workspaceID, err))
	}
	res.Closed = true
	if err := env.DB.SetOperationStatus(rctx, opID, KillClosed, true, env.now()); err != nil {
		return res, fmt.Errorf("kill: the workspace is closed, but its record is not: %w", err)
	}
	return res, nil
}

// uncheckedPanes names the workspace's panes, other than the checked
// ones and nhm's live board, that hold an agent or a job in the
// foreground: closing the workspace would end them with no retire check.
func uncheckedPanes(ctx context.Context, env Env, workspaceID string, checked map[string]bool) ([]string, error) {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	agents := map[string]herdr.AgentInfo{}
	for _, a := range snap.Agents {
		agents[a.PaneID] = a
	}
	boards := liveBoards(ctx, env, workspaceID)
	var out []string
	for _, p := range snap.Panes {
		if p.WorkspaceID != workspaceID || checked[p.PaneID] || boards[p.PaneID] {
			continue
		}
		if a, ok := agents[p.PaneID]; ok {
			out = append(out, fmt.Sprintf("pane %s holds %s agent %q that nhm has no binding for", p.PaneID, a.Agent, a.Name))
			continue
		}
		info, err := env.Herdr.PaneProcessInfo(ctx, p.PaneID)
		if err != nil {
			return nil, err
		}
		if info.ShellPID == nil || info.ForegroundProcessGroupID == nil || *info.ForegroundProcessGroupID != *info.ShellPID {
			out = append(out, fmt.Sprintf("pane %s runs a job in the foreground", p.PaneID))
		}
	}
	return out, nil
}

// liveBoards are the workspace's live nhm boards by pane id, by their
// verified launch records (uilaunch.Boards), never by label or pane
// kind: nhm's own panes, which kill closes with the workspace. A board
// that cannot be checked is not left out: kill checks its pane as it
// checks any other.
func liveBoards(ctx context.Context, env Env, workspaceID string) map[string]bool {
	out := map[string]bool{}
	if env.InstanceDir == "" {
		return out
	}
	found, _ := uilaunch.Boards(ctx, uilaunch.Env{InstanceDir: env.InstanceDir, Now: env.Now}, env.Herdr, workspaceID)
	for pane := range found {
		out[pane] = true
	}
	return out
}
