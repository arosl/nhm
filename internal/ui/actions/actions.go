// Package actions is nhm's one action table (plan-v4 section 2): every
// action the menu, the board and the palette offer, with its label, its
// scope, the selection it needs, why it is unavailable, its place on each
// surface and its handler. The surfaces lay the table out and collect
// input; they run an action only through Do, so every shortcut to an
// action shares one handler, one submission guard (Guard) and one
// operation id per review. A handler calls nhm's core through Core and
// nothing else: the UI holds no second send or lifecycle path.
package actions

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/arosl/nhm/internal/ui"
)

// Kind says what an action is.
type Kind string

// Kinds.
const (
	// KindGroup is a menu level; it runs nothing.
	KindGroup Kind = "group"
	// KindSurface starts another surface (palette, board).
	KindSurface Kind = "surface"
	// KindView is read only (plan-v4 section 4).
	KindView Kind = "view"
	// KindAction changes no agent and needs no review: focus, zoom.
	KindAction Kind = "action"
	// KindForm changes something: a review, then a confirm, then one
	// submission with one operation id.
	KindForm Kind = "form"
)

// Scope is what an action acts on.
type Scope string

// Scopes.
const (
	// ScopeNone: the nhm instance as a whole, or nothing.
	ScopeNone Scope = "none"
	// ScopeWorkspace: the captured workspace; unavailable without one.
	ScopeWorkspace Scope = "workspace"
	// ScopeAgents: the targets of the selection.
	ScopeAgents Scope = "agents"
)

// Selection is how many targets an action takes (plan-v4 section 4).
type Selection string

// Selections.
const (
	SelectNone Selection = "none"
	SelectOne  Selection = "one"
	SelectMany Selection = "many"
)

// Takes says which agents an agent action takes.
type Takes string

// What an agent action takes.
const (
	// TakesBound: bound agents only (the zero value): nhm acts on an agent
	// through its binding.
	TakesBound Takes = ""
	// TakesUnbound: agents with no binding only (adopt).
	TakesUnbound Takes = "unbound"
	// TakesAny: any agent (read, zoom).
	TakesAny Takes = "any"
)

// MenuPlace is an action's row in the menu: its level (Parent: a group's
// ID, "" the top level) and the key that picks it there.
type MenuPlace struct {
	Parent string
	Key    string
}

// BoardPlace is an action's place on the board: Keys are board keys, and
// those that ntm's dashboard has keep ntm's meaning; Space is its letter
// in the board's action menu (space), for nhm's per-agent actions.
type BoardPlace struct {
	Keys  []string
	Space string
}

// Action is one row of the table.
type Action struct {
	ID    string
	Label string
	// Help says what the action does, in one line.
	Help      string
	Kind      Kind
	Scope     Scope
	Selection Selection
	Takes     Takes
	Menu      MenuPlace
	Board     BoardPlace
	// Covers are the CLI leaves of evidence/verb-coverage.md that the
	// action makes interactive.
	Covers []string
	// run is the handler; nil means not built yet, and the surfaces do
	// not show the action (no dead keys). Only Do calls it.
	run Handler
	// needs says why the core cannot run the handler here; nil: nothing.
	needs func(Core) string
}

// Handler runs an action's reviewed request against the core.
type Handler func(ctx context.Context, core Core, req Request) (Result, error)

// Request is one reviewed request: the context, the frozen targets, the
// operation id from Guard.Submit (forms), and the form's values.
type Request struct {
	Context     ui.Context
	Targets     []ui.Target
	OperationID string
	Params      map[string]string
}

// Result is what the core answered, in its words.
type Result struct {
	OperationID string
	// Status is the operation's outcome: the core's own word ("done",
	// "refused", "partial", "failed"...).
	Status string
	// Targets are the per-target outcomes.
	Targets []TargetResult
	// Lines are what the core did or found, one per line.
	Lines    []string
	Warnings []string
}

// TargetResult is one target's outcome.
type TargetResult struct {
	Target ui.Target
	// Name is the agent's Mail name when the core gave one.
	Name   string
	Status string
	Reason string
}

// Errors of Do.
var (
	// ErrUnavailable: the action cannot run here; the error says why.
	ErrUnavailable = errors.New("actions: unavailable")
	// ErrNoOperationID: a form runs only with the id Guard.Submit gave.
	ErrNoOperationID = errors.New("actions: a form needs its operation id: submit it through the guard")
	// ErrTargets: the targets do not fit the action's selection.
	ErrTargets = errors.New("actions: wrong targets")
	// ErrNoAnswer: the core's answer did not come in (the CLI's lost job
	// matches it with errors.Is). The operation may run on; its record
	// says how it ends, and nothing submits it again (plan-v4 section 4).
	ErrNoAnswer = errors.New("actions: no answer from the core")
)

// StatusUnknown is the status of an operation, and of each of its
// targets, whose answer did not come in.
const StatusUnknown = "unknown"

// NoAnswer reports whether err means the core's answer did not come in:
// ErrNoAnswer, or the wait's deadline.
func NoAnswer(err error) bool {
	return errors.Is(err, ErrNoAnswer) || errors.Is(err, context.DeadlineExceeded)
}

// unknown is the result of a request whose answer did not come in: the
// operation and each target are unknown, never failed.
func unknown(req Request, opID string) Result {
	out := Result{OperationID: firstNonEmpty(opID, req.OperationID), Status: StatusUnknown,
		Lines: []string{"no answer from the core: the operation may run on; recent operations shows its record"}}
	for _, t := range req.Targets {
		out.Targets = append(out.Targets, TargetResult{Target: t, Status: StatusUnknown})
	}
	return out
}

// Unavailable says why the action cannot run in context c with core;
// "" when it can. Surfaces show the reason, or hide an action that is not
// built yet (Visible).
func (a Action) Unavailable(c ui.Context, core Core) string {
	switch {
	case a.Kind == KindGroup:
		return ""
	case a.Scope == ScopeWorkspace && c.WorkspaceID == "":
		return "no workspace: open it from a workspace"
	case a.Kind == KindSurface:
		return ""
	case a.run == nil:
		return "not built yet"
	case a.needs != nil:
		return a.needs(core)
	}
	return ""
}

// Visible reports whether a surface shows the action: groups, surfaces
// (the surface code starts them) and built actions are shown, the rest
// is not.
func (a Action) Visible() bool { return a.Kind == KindGroup || a.Kind == KindSurface || a.run != nil }

// Targets returns the targets of context c for the action: pick is true
// when the action needs agents and c gives none (open the agent picker);
// ErrTargets when c's explicit selection does not fit (an empty one is
// ui.ErrEmptySelection). It never widens a selection.
func (a Action) Targets(c ui.Context) (ts []ui.Target, pick bool, err error) {
	if a.Selection == SelectNone || a.Selection == "" {
		return nil, false, nil
	}
	ts, pick, err = c.Selection()
	if pick || err != nil {
		return nil, pick, err
	}
	return ts, false, a.checkTargets(ts)
}

// checkTargets refuses targets that do not fit the selection.
func (a Action) checkTargets(ts []ui.Target) error {
	switch a.Selection {
	case SelectNone, "":
		if len(ts) > 0 {
			return fmt.Errorf("%w: %s takes no agent", ErrTargets, a.ID)
		}
		return nil
	case SelectOne:
		if len(ts) != 1 {
			return fmt.Errorf("%w: %s takes exactly one agent, not %d", ErrTargets, a.ID, len(ts))
		}
	case SelectMany:
		if len(ts) == 0 {
			return ui.ErrEmptySelection
		}
	}
	seen := map[string]bool{}
	for _, t := range ts {
		switch {
		case t.Bound() && t.Generation < 1:
			return fmt.Errorf("%w: %s has no generation", ErrTargets, t.BindingID)
		case !t.Bound() && t.PaneID == "":
			return fmt.Errorf("%w: a target names neither a binding nor a pane", ErrTargets)
		case a.Takes == TakesUnbound && t.Bound():
			return fmt.Errorf("%w: %s takes an agent with no binding; %s is bound", ErrTargets, a.ID, t.BindingID)
		case a.Takes == TakesUnbound && t.Session == "":
			// The session is what tells the reviewed agent from one that
			// starts in the pane later (bindings.AdoptRequest.ExpectedSession).
			return fmt.Errorf("%w: herdr reports no agent session in pane %s, so nhm cannot tell this agent from a later one there; "+
				"adopt it by hand: nhm adopt <Name> --pane %s", ErrTargets, t.PaneID, t.PaneID)
		case a.Takes == TakesBound && !t.Bound():
			return fmt.Errorf("%w: pane %s has no binding: nhm %s acts on bound agents only (adopt binds one)", ErrTargets, t.PaneID, a.Label)
		case seen[t.Key()]:
			return fmt.Errorf("%w: %s is given twice", ErrTargets, t.Key())
		}
		seen[t.Key()] = true
	}
	return nil
}

// Do runs the action's handler on a reviewed request, after the checks
// every surface shares: the action is available, a form has the
// operation id its guard gave, and the targets fit the selection.
func (a Action) Do(ctx context.Context, core Core, req Request) (Result, error) {
	if why := a.Unavailable(req.Context, core); why != "" {
		return Result{}, fmt.Errorf("%w: %s: %s", ErrUnavailable, a.ID, why)
	}
	if a.Kind == KindGroup || a.Kind == KindSurface {
		return Result{}, fmt.Errorf("%w: %s is a %s, not something to run", ErrUnavailable, a.ID, a.Kind)
	}
	if a.Kind == KindForm && req.OperationID == "" {
		return Result{}, ErrNoOperationID
	}
	if err := a.checkTargets(req.Targets); err != nil {
		return Result{}, err
	}
	res, err := a.run(ctx, core, req)
	if NoAnswer(err) {
		// The answer did not come in: unknown, never failed, under the
		// operation id the core named, else the request's.
		return unknown(req, res.OperationID), err
	}
	return res, err
}

// ByID returns the action with an ID.
func ByID(id string) (Action, bool) {
	i := slices.IndexFunc(table, func(a Action) bool { return a.ID == id })
	if i < 0 {
		return Action{}, false
	}
	return table[i], true
}

// Table returns the table, in menu order.
func Table() []Action { return slices.Clone(table) }

// Level returns the actions of a menu level ("" the top level, else a
// group's ID), in order, built or not; Visible filters.
func Level(parent string) []Action {
	var out []Action
	for _, a := range table {
		if a.Menu.Key != "" && a.Menu.Parent == parent {
			out = append(out, a)
		}
	}
	return out
}

// Space returns the board's action menu (space), in order, built or not.
func Space() []Action {
	var out []Action
	for _, a := range table {
		if a.Board.Space != "" {
			out = append(out, a)
		}
	}
	return out
}
