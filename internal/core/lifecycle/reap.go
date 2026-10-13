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
}

// ReapItem is one agent the sweep looked at.
type ReapItem struct {
	MailName  string        `json:"mail_name"`
	BindingID string        `json:"binding_id"`
	PaneID    string        `json:"pane_id,omitempty"`
	IdleFor   time.Duration `json:"idle_ns"`
	// Action: retired, would_retire, kept, refused.
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
	idlePath := filepath.Join(env.InstanceDir, IdleFile)
	idle := readIdle(idlePath)
	now := env.now()
	seen := map[string]bool{}
	var items []ReapItem
	keep := map[string]bool{}
	for _, k := range req.Keep {
		keep[strings.ToLower(k)] = true
	}
	for _, b := range list {
		item := ReapItem{MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID, Action: "kept"}
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
			items = append(items, item)
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
			items = append(items, item)
			continue
		}
		// A reviewer works no beads: only an agent spawned with --role
		// reviewer may have none in the databases searched.
		rr, err := Retire(ctx, env, RetireRequest{Target: b.ID, KeepIdentity: false, DryRun: req.DryRun, Reason: "reap",
			NoBeads: b.Role == spawn.RoleReviewer})
		r := rr
		item.Retire = &r
		switch {
		case errors.Is(err, ErrRefused):
			item.Action, item.Reason = "refused", strings.Join(rr.Refusals, "; ")
		case err != nil:
			item.Action, item.Reason = "refused", err.Error()
		case req.DryRun:
			item.Action = "would_retire"
		default:
			item.Action = "retired"
			delete(idle, b.ID)
		}
		items = append(items, item)
	}
	// Keep the entries of other workspaces; drop those not seen idle.
	for id := range idle {
		if !seen[id] && slices.ContainsFunc(list, func(b bindings.Binding) bool { return b.ID == id }) {
			delete(idle, id)
		}
	}
	if err := writeIdle(idlePath, idle); err != nil {
		return items, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].MailName < items[j].MailName })
	return items, nil
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
	Closed      bool           `json:"closed"`
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
// any effect, so the record outlives the process that runs it.
func claimKill(ctx context.Context, env Env, workspaceID string, list []bindings.Binding, noBeads bool) (string, error) {
	type target struct {
		BindingID  string `json:"binding_id"`
		Generation int64  `json:"generation"`
	}
	targets := make([]target, len(list))
	for i, b := range list {
		targets[i] = target{b.ID, b.Generation}
	}
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{Verb: VerbKill,
		Scope:   map[string]string{"workspace_id": workspaceID},
		Request: map[string]any{"workspace_id": workspaceID, "targets": targets, "no_beads": noBeads}}, env.now())
	if err != nil {
		return "", err
	}
	id := claim.Operation.ID
	now := store.FormatTime(env.now())
	for i, b := range list {
		js, err := json.Marshal(map[string]string{"mail_name": b.MailName})
		if err != nil {
			return id, err
		}
		if _, err := env.DB.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation,
			status, result_json, updated_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)`, id, i, b.ID, b.Generation, string(js), now); err != nil {
			return id, err
		}
	}
	return id, env.DB.SetOperationStatus(ctx, id, KillDispatching, false, env.now())
}

// Kill closes a workspace. It runs retire's checks for every bound agent
// first and never bypasses them: one refusal refuses the kill, and
// nothing is closed. Without yes it only reports.
// noBeads passes retire's --no-beads to every agent: the workspace holds
// agents that worked no beads (reviewers).
func Kill(ctx context.Context, env Env, workspaceID string, yes, noBeads bool) (KillResult, error) {
	res := KillResult{WorkspaceID: workspaceID, Agents: []RetireResult{}}
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
		return res, fmt.Errorf("%w: the workspace's panes cannot be read: %v", ErrRefused, err)
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
		return res, fmt.Errorf("%w: %s", ErrRefused, strings.Join(refusals, " | "))
	}
	if !yes {
		return res, fmt.Errorf("%w: kill closes the workspace and retires its agents: give --yes", ErrRefused)
	}
	opID, err := claimKill(ctx, env, workspaceID, list, noBeads)
	res.OperationID = opID
	if err != nil {
		return res, fmt.Errorf("kill: record the operation: %w", err)
	}
	stop := func(err error) (KillResult, error) {
		_ = env.DB.SetOperationStatus(ctx, opID, KillStopped, true, env.now())
		return res, err
	}
	res.Agents = res.Agents[:0]
	for i, b := range list {
		// The generation the checks saw: an agent recycled since is
		// refused, never retired unchecked.
		r, err := Retire(ctx, env, RetireRequest{Target: b.ID, Reason: "kill", NoBeads: noBeads || b.Role == spawn.RoleReviewer,
			OperationID: fmt.Sprintf("%s.%d", opID, i), ExpectedGeneration: b.Generation})
		res.Agents = append(res.Agents, r)
		status := KillRetired
		switch {
		case errors.Is(err, ErrRefused):
			status = KillRefused
		case err != nil:
			status = KillFailed
		}
		if werr := killReceipt(ctx, env, opID, i, status, r, err); werr != nil && err == nil {
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
	if err := env.DB.SetOperationStatus(ctx, opID, KillClosing, false, env.now()); err != nil {
		return stop(fmt.Errorf("kill: record the close: %w", err))
	}
	// herdr may have closed the workspace with its last pane.
	if err := env.Herdr.WorkspaceClose(ctx, workspaceID); err != nil && !strings.HasSuffix(herdr.ErrorCode(err), "not_found") {
		return stop(fmt.Errorf("close workspace %s: %w", workspaceID, err))
	}
	res.Closed = true
	if err := env.DB.SetOperationStatus(ctx, opID, KillClosed, true, env.now()); err != nil {
		return res, fmt.Errorf("kill: the workspace is closed, but its record is not: %w", err)
	}
	return res, nil
}

// uncheckedPanes names the workspace's panes, other than the checked
// ones, that hold an agent or a job in the foreground: closing the
// workspace would end them with no retire check.
func uncheckedPanes(ctx context.Context, env Env, workspaceID string, checked map[string]bool) ([]string, error) {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	agents := map[string]herdr.AgentInfo{}
	for _, a := range snap.Agents {
		agents[a.PaneID] = a
	}
	var out []string
	for _, p := range snap.Panes {
		if p.WorkspaceID != workspaceID || checked[p.PaneID] {
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
