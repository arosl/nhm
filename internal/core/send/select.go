// Package send is nhm's send (plan-v8 2.3): one path for the CLI, the
// robot flags, the palette, wake prompts and kickoffs. An operation is
// claimed before any prompt goes out; each target gets a receipt with its
// pre-submission baseline; nhm never resends on its own.
package send

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// Selector picks the targets (plan-v8 2.3, "Selection rules"). Names,
// Panes and Bindings are a union; Kinds are a union among themselves; the
// kinds, States and the workspace are an intersection. With neither
// names, panes nor bindings, every bound agent of the workspace is
// selected (--all, or a prompt with no selector). The caller is left out
// unless IncludeSelf or named.
type Selector struct {
	All   bool     `json:"all,omitempty"`
	Names []string `json:"names,omitempty"`
	Panes []string `json:"panes,omitempty"`
	// Bindings are binding ids (send --if-generation's pins). One that is
	// not active in the workspace selects nothing; with Request.Expect it
	// gets a refused receipt (not_selected), never another agent.
	Bindings    []string            `json:"bindings,omitempty"`
	Kinds       []string            `json:"kinds,omitempty"`
	States      []herdr.AgentStatus `json:"states,omitempty"`
	Limit       int                 `json:"limit,omitempty"`
	IncludeSelf bool                `json:"include_self,omitempty"`
}

// Target is one selected agent: its binding, frozen at selection, and
// herdr's agent in its pane (nil when the pane has none).
type Target struct {
	Binding bindings.Binding
	Agent   *herdr.AgentInfo
	// Key is the target's name in robot output: the slot "N.M", or the
	// pane id when the binding has no slot.
	Key string
}

// Candidate is a named herdr agent that has no binding: send never binds
// by name, so it is refused with the command that would bind it.
type Candidate struct {
	Name   string `json:"name"`
	PaneID string `json:"pane_id"`
	Hint   string `json:"hint"`
}

// ErrNoTargets means the selection is empty.
var ErrNoTargets = errors.New("no agent matches the selection")

// Lister lists bindings; *bindings.Store has it.
type Lister interface {
	List(ctx context.Context, f bindings.Filter) ([]bindings.Binding, error)
}

// Select resolves the selector in a workspace against the bindings and
// herdr's agents. caller is the calling pane (HERDR_PANE_ID).
func Select(ctx context.Context, l Lister, agents []herdr.AgentInfo, workspaceID, projectKey, caller string, sel Selector) ([]Target, []Candidate, error) {
	list, err := l.List(ctx, bindings.Filter{WorkspaceID: workspaceID, ProjectKey: projectKey, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, nil, err
	}
	byPane := map[string]*herdr.AgentInfo{}
	for i := range agents {
		if agents[i].WorkspaceID == workspaceID {
			byPane[agents[i].PaneID] = &agents[i]
		}
	}
	named := map[string]bool{}
	var candidates []Candidate
	for _, n := range sel.Names {
		// A workspace can hold agents of several projects: a name that
		// matches two of them names neither (nhm-ktm).
		var hits []string
		for _, b := range list {
			if nameMatches(b, n) {
				hits = append(hits, b.ID+" ("+b.MailProjectKey+")")
				named[b.ID] = true
			}
		}
		if len(hits) > 1 {
			hint := ""
			if projectKey == "" {
				hint = "; give --project"
			}
			return nil, nil, fmt.Errorf("%w: %s in workspace %s: %s%s", bindings.ErrAmbiguous, n, workspaceID, strings.Join(hits, ", "), hint)
		}
		if len(hits) == 1 {
			continue
		}
		c, ok := candidate(byPane, n)
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s has no binding and no herdr agent in workspace %s", ErrNoTargets, n, workspaceID)
		}
		candidates = append(candidates, c)
	}
	for _, p := range sel.Panes {
		i := slices.IndexFunc(list, func(b bindings.Binding) bool { return b.Slot == p || b.PaneID == p })
		if i < 0 {
			return nil, nil, fmt.Errorf("%w: pane %s has no bound agent in workspace %s", ErrNoTargets, p, workspaceID)
		}
		named[list[i].ID] = true
	}
	for _, id := range sel.Bindings {
		if slices.ContainsFunc(list, func(b bindings.Binding) bool { return b.ID == id }) {
			named[id] = true
		}
	}
	explicit := len(sel.Names) > 0 || len(sel.Panes) > 0 || len(sel.Bindings) > 0
	wantDone := slices.Contains(sel.States, herdr.StatusDone)
	var out []Target
	for _, b := range list {
		if explicit && !named[b.ID] {
			continue
		}
		a := byPane[b.PaneID]
		if len(sel.Kinds) > 0 && !slices.Contains(sel.Kinds, b.Kind) {
			continue
		}
		if len(sel.States) > 0 && (a == nil || !slices.Contains(sel.States, a.AgentStatus)) {
			continue
		}
		if wantDone && !named[b.ID] {
			// The registry's code_review: false leaves the kind out of a
			// "--state done" selection unless named (Codex: plan reviews only).
			if !kinds.Lookup(b.Kind).CodeReview {
				continue
			}
		}
		if caller != "" && b.PaneID == caller && !sel.IncludeSelf && !named[b.ID] {
			continue
		}
		out = append(out, Target{Binding: b, Agent: a, Key: key(b)})
	}
	sortTargets(out)
	if sel.Limit > 0 && len(out) > sel.Limit {
		out = out[:sel.Limit]
	}
	if len(out) == 0 && len(candidates) == 0 {
		return nil, nil, fmt.Errorf("%w in workspace %s", ErrNoTargets, workspaceID)
	}
	return out, candidates, nil
}

func candidate(byPane map[string]*herdr.AgentInfo, name string) (Candidate, bool) {
	alias := strings.ToLower(name)
	for _, a := range byPane {
		if a.Name == alias {
			return Candidate{Name: name, PaneID: a.PaneID,
				Hint: fmt.Sprintf("bind it first: nhm adopt %s --pane %s, or nhm admin recover reconcile --import-only", name, a.PaneID)}, true
		}
	}
	return Candidate{}, false
}

func nameMatches(b bindings.Binding, n string) bool {
	return strings.EqualFold(b.MailName, n) || (b.HerdrAlias != "" && b.HerdrAlias == strings.ToLower(n))
}

func key(b bindings.Binding) string {
	if b.Slot != "" {
		return b.Slot
	}
	return b.PaneID
}

// sortTargets puts targets in slot order.
func sortTargets(ts []Target) {
	list := make([]bindings.Binding, len(ts))
	for i, t := range ts {
		list[i] = t.Binding
	}
	wait.SortBySlot(list)
	pos := map[string]int{}
	for i, b := range list {
		pos[b.ID] = i
	}
	slices.SortFunc(ts, func(a, b Target) int { return pos[a.Binding.ID] - pos[b.Binding.ID] })
}
