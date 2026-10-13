package wait

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
)

// Selector picks the agents of a workspace that wait and interrupt act
// on. Each list is a union; the lists are an intersection; empty lists
// match everything.
type Selector struct {
	// Names are Agent Mail names or herdr names.
	Names []string
	// Slots are ntm's pane numbers, "N.M".
	Slots []string
	// Kinds are agent kinds ("claude", "codex").
	Kinds []string
	// Exclude is a pane id to leave out (the caller), unless named.
	Exclude string
}

// ErrNoTargets means the selection is empty.
var ErrNoTargets = errors.New("no agent matches the selection")

// Lister lists bindings; *bindings.Store has it.
type Lister interface {
	List(ctx context.Context, f bindings.Filter) ([]bindings.Binding, error)
}

// Select returns the active bindings of a workspace that the selector
// matches, in slot order. A named agent that has no active binding there
// is an error, so a typo never shrinks the set without a word. A name
// that matches two agents is an error too: a workspace can hold agents of
// several projects, and an interrupt must not reach both (nhm-y2e).
func Select(ctx context.Context, l Lister, workspaceID, projectKey string, sel Selector) ([]bindings.Binding, error) {
	all, err := l.List(ctx, bindings.Filter{WorkspaceID: workspaceID, ProjectKey: projectKey, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	// The other lists narrow a name first: a name with a slot or a kind
	// that leaves one agent names that agent.
	narrow := Selector{Slots: sel.Slots, Kinds: sel.Kinds}
	for _, n := range sel.Names {
		found := false
		var hits []string
		for _, b := range all {
			if !nameMatches(b, n) {
				continue
			}
			found = true
			if len(Filter([]bindings.Binding{b}, narrow)) == 1 {
				hits = append(hits, fmt.Sprintf("%s (slot %s, %s)", b.MailName, b.Slot, b.MailProjectKey))
			}
		}
		switch {
		case !found:
			return nil, fmt.Errorf("%w: %s has no active binding in workspace %s", ErrNoTargets, n, workspaceID)
		case len(hits) > 1:
			return nil, fmt.Errorf("%w: %s in workspace %s: %s; name one by its slot", bindings.ErrAmbiguous, n, workspaceID, strings.Join(hits, ", "))
		}
	}
	out := Filter(all, sel)
	if len(out) == 0 {
		return nil, fmt.Errorf("%w in workspace %s", ErrNoTargets, workspaceID)
	}
	SortBySlot(out)
	return out, nil
}

// Filter keeps the bindings that the selector matches.
func Filter(list []bindings.Binding, sel Selector) []bindings.Binding {
	var out []bindings.Binding
	for _, b := range list {
		named := len(sel.Names) > 0 && slices.ContainsFunc(sel.Names, func(n string) bool { return nameMatches(b, n) })
		if len(sel.Names) > 0 && !named {
			continue
		}
		if len(sel.Slots) > 0 && !slices.Contains(sel.Slots, b.Slot) {
			continue
		}
		if len(sel.Kinds) > 0 && !slices.Contains(sel.Kinds, b.Kind) {
			continue
		}
		if sel.Exclude != "" && b.PaneID == sel.Exclude && !named {
			continue
		}
		out = append(out, b)
	}
	return out
}

func nameMatches(b bindings.Binding, n string) bool {
	return strings.EqualFold(b.MailName, n) || (b.HerdrAlias != "" && b.HerdrAlias == strings.ToLower(n))
}

// SortBySlot orders bindings by slot ("0.2" before "0.10"), then by mail
// name; a binding without a slot comes last.
func SortBySlot(list []bindings.Binding) {
	slices.SortStableFunc(list, func(a, b bindings.Binding) int {
		sa, ea := bindings.ParseSlot(a.Slot)
		sb, eb := bindings.ParseSlot(b.Slot)
		switch {
		case ea != nil && eb != nil:
			return strings.Compare(a.MailName, b.MailName)
		case ea != nil:
			return 1
		case eb != nil:
			return -1
		case sa.Tab != sb.Tab:
			return sa.Tab - sb.Tab
		case sa.Pane != sb.Pane:
			return sa.Pane - sb.Pane
		}
		return strings.Compare(a.MailName, b.MailName)
	})
}
