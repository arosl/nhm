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
		rr, err := Retire(ctx, env, RetireRequest{Target: b.ID, KeepIdentity: false, DryRun: req.DryRun, Reason: "reap"})
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

// KillResult is a kill of a workspace.
type KillResult struct {
	WorkspaceID string         `json:"workspace_id"`
	Agents      []RetireResult `json:"agents"`
	Closed      bool           `json:"closed"`
}

// Kill closes a workspace. It runs retire's checks for every bound agent
// first and never bypasses them: one refusal refuses the kill, and
// nothing is closed. Without yes it only reports.
func Kill(ctx context.Context, env Env, workspaceID string, yes bool) (KillResult, error) {
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
		r, err := Retire(ctx, env, RetireRequest{Target: b.ID, DryRun: true, Reason: "kill"})
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
		return res, errors.New("kill closes the workspace and retires its agents: give --yes")
	}
	res.Agents = res.Agents[:0]
	for _, b := range list {
		r, err := Retire(ctx, env, RetireRequest{Target: b.ID, Reason: "kill"})
		res.Agents = append(res.Agents, r)
		if err != nil {
			return res, fmt.Errorf("%s: %w; the workspace stays open", b.MailName, err)
		}
	}
	// What started in the workspace while the agents were retired is not
	// closed unchecked either.
	unchecked, err = uncheckedPanes(ctx, env, workspaceID, nil)
	if err != nil || len(unchecked) > 0 {
		if err != nil {
			unchecked = append(unchecked, "the workspace's panes cannot be read: "+err.Error())
		}
		return res, fmt.Errorf("%w: the agents are retired, the workspace stays open: %s", ErrRefused, strings.Join(unchecked, " | "))
	}
	// herdr may have closed the workspace with its last pane.
	if err := env.Herdr.WorkspaceClose(ctx, workspaceID); err != nil && !strings.HasSuffix(herdr.ErrorCode(err), "not_found") {
		return res, fmt.Errorf("close workspace %s: %w", workspaceID, err)
	}
	res.Closed = true
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
