// Package tabs moves an agent's pane into another tab of its workspace,
// into the tab's next free cell of the grid, where spawn would put a new
// agent (nhm-ble): nhm move --tab, and each move of nhm arrange. herdr
// keeps the pane's id within a workspace. The binding records the new
// tab and keeps its slot, which is never a layout coordinate (plan-v8
// 2.12).
package tabs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
)

// Herdr is what a move into a tab needs from herdr; *herdr.Client has it.
type Herdr interface {
	bindings.Herdr
	PaneLayout(ctx context.Context, paneID string) (herdr.PaneLayout, error)
	PaneMove(ctx context.Context, p herdr.PaneMoveParams) (herdr.PaneMoveResult, error)
	PaneSwap(ctx context.Context, p herdr.PaneSwapParams) (herdr.PaneSwapResult, error)
	TabRename(ctx context.Context, tabID, label string) (herdr.TabInfo, error)
}

// Env is what a move runs against.
type Env struct {
	Bindings *bindings.Store
	Herdr    Herdr
	// InstanceDir holds the bindings' mutation locks (send.LockTarget).
	InstanceDir string
	// LockWait bounds Join's wait for the binding's lock.
	LockWait time.Duration
}

// ErrRefused marks the refusals that leave everything as it was.
var ErrRefused = errors.New("refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// Cell is where a move puts a pane: the first free cell of the tab's
// grid (spawn.GridSplit). herdr splits Target in the direction Split,
// and Target keeps Ratio of its room.
type Cell struct {
	TabID    string `json:"tab_id"`
	TabLabel string `json:"tab_label"`
	// NewLabel is the label the tab takes after the move, or "" when
	// it keeps its own.
	NewLabel string `json:"new_tab_label,omitempty"`
	// FromTab is the tab herdr shows the pane in before the move.
	FromTab string `json:"from_tab_id"`
	// Agents is the number of agents in the tab before the move.
	Agents int `json:"agents"`
	// Cell is the cell's number in reading order, from 1, in the grid
	// of PerTab.
	Cell   int     `json:"cell"`
	PerTab int     `json:"per_tab"`
	Target string  `json:"target_pane_id"`
	Split  string  `json:"split"`
	Ratio  float64 `json:"ratio"`
	// Warnings are what Check could not prove.
	Warnings []string `json:"-"`
}

// Result is a move's outcome.
type Result struct {
	Cell Cell
	// Binding is the binding after the move.
	Binding bindings.Binding
	// ClosedTabID is the tab herdr closed, as the move left it empty.
	ClosedTabID string
	Actions     []string
	Warnings    []string
}

// FindTab finds the tab that spec names for a move within workspaceID:
// a herdr tab id (it holds a colon), the slot N of the agents that a tab
// holds now (a number, as nhm status shows the slots), or the label of
// one tab of the workspace. A move within a workspace keeps the slot, so
// after one the agents of slot N may be in several tabs: then spec names
// none. A tab id of another workspace is found, and Check refuses it.
func FindTab(ctx context.Context, env Env, workspaceID, spec string) (string, error) {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	spec = strings.TrimSpace(spec)
	var found []string
	switch n, nerr := strconv.Atoi(spec); {
	case strings.Contains(spec, ":"):
		for _, t := range snap.Tabs {
			if t.TabID == spec {
				return spec, nil
			}
		}
		return "", refuse("no tab %s", spec)
	case nerr == nil:
		paneTab := map[string]string{}
		for _, p := range snap.Panes {
			if p.WorkspaceID == workspaceID {
				paneTab[p.PaneID] = p.TabID
			}
		}
		list, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: workspaceID, Statuses: []bindings.Status{bindings.Active}})
		if err != nil {
			return "", err
		}
		for _, b := range list {
			s, err := bindings.ParseSlot(b.Slot)
			tab, live := paneTab[b.PaneID]
			if err == nil && s.Tab == n && live && !slices.Contains(found, tab) {
				found = append(found, tab)
			}
		}
		slices.Sort(found)
		switch len(found) {
		case 0:
			return "", refuse("no tab of workspace %s holds an agent of slot %d (nhm status shows the slots)", workspaceID, n)
		case 1:
			return found[0], nil
		}
		return "", refuse("the agents of slot %d are in %d tabs (%s); give the tab id", n, len(found), strings.Join(found, ", "))
	}
	for _, t := range snap.Tabs {
		if t.WorkspaceID == workspaceID && t.Label == spec {
			found = append(found, t.TabID)
		}
	}
	switch len(found) {
	case 0:
		return "", refuse("no tab of workspace %s is labelled %q", workspaceID, spec)
	case 1:
		return found[0], nil
	}
	return "", refuse("the label %q names %d tabs of workspace %s (%s); give the tab id", spec, len(found), workspaceID, strings.Join(found, ", "))
}

// Check checks that b's pane may go into tabID, and gives the tab's next
// free cell of the grid of perTab. It changes nothing. It refuses
// (ErrRefused) when the tab is in another workspace or holds b already;
// when it has perTab panes or more, or lies off the grid (spawn.OnGrid);
// when it holds a pane with no agent, an agent with no active binding,
// or an agent of another project; and when an agent that would share the
// tab names itself in no variable that ACFS's Agent Mail hook reads,
// which then reads the tab label as the agent's name (nhm-4c4). An
// agent whose environment nhm cannot read gets a warning.
func Check(ctx context.Context, env Env, b bindings.Binding, tabID string, perTab int) (Cell, error) {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return Cell{}, err
	}
	var tab herdr.TabInfo
	for _, t := range snap.Tabs {
		if t.TabID == tabID {
			tab = t
		}
	}
	if tab.TabID == "" {
		return Cell{}, refuse("no tab %s", tabID)
	}
	if tab.WorkspaceID != b.WorkspaceID {
		return Cell{}, refuse("tab %s is in workspace %s, %s is in %s: nhm move --tab moves within a workspace, --workspace to another",
			tabID, tab.WorkspaceID, b.MailName, b.WorkspaceID)
	}
	c := Cell{TabID: tabID, TabLabel: tab.Label, PerTab: perTab}
	var panes []herdr.PaneInfo
	for _, p := range snap.Panes {
		if p.PaneID == b.PaneID {
			c.FromTab = p.TabID
		}
		if p.TabID == tabID {
			panes = append(panes, p)
		}
	}
	switch {
	case c.FromTab == tabID:
		return Cell{}, refuse("%s is in tab %s already", b.MailName, tabID)
	case len(panes) == 0:
		return Cell{}, refuse("tab %s has no pane", tabID)
	case len(panes) >= perTab:
		return Cell{}, refuse("tab %s is full: it holds %d panes, and --per-tab is %d", tabID, len(panes), perTab)
	}
	list, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: b.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return Cell{}, err
	}
	byPane := map[string]bindings.Binding{}
	for _, o := range list {
		byPane[o.PaneID] = o
	}
	sharers := []bindings.Binding{b}
	for _, p := range panes {
		o, bound := byPane[p.PaneID]
		switch {
		case p.Agent == "":
			return Cell{}, refuse("tab %s holds %s, a pane with no agent", tabID, p.PaneID)
		case !bound:
			return Cell{}, refuse("tab %s holds %s, a %s agent with no binding", tabID, p.PaneID, p.Agent)
		case o.MailProjectKey != b.MailProjectKey:
			return Cell{}, refuse("tab %s holds %s, an agent of another project (%s)", tabID, o.MailName, o.MailProjectKey)
		}
		sharers = append(sharers, o)
	}
	c.Agents = len(panes)
	label := bindings.ProjectTabLabel(b.MailProjectKey)
	for _, s := range sharers {
		w, err := Named(s)
		if err != nil {
			return Cell{}, err
		}
		if w != "" {
			c.Warnings = append(c.Warnings, w)
		}
		if tab.Label == s.MailName {
			c.NewLabel = label
		}
	}
	if numeric(tab.Label) {
		c.NewLabel = label
	}
	if c.NewLabel == tab.Label {
		c.NewLabel = ""
	}
	l, err := env.Herdr.PaneLayout(ctx, panes[0].PaneID)
	if err != nil {
		return Cell{}, fmt.Errorf("tabs: the layout of tab %s: %w", tabID, err)
	}
	if !spawn.OnGrid(l.Panes, perTab) {
		return Cell{}, refuse("tab %s is off the grid of %d: laid out by hand, for another --per-tab, or resized", tabID, perTab)
	}
	var ok bool
	if c.Cell, c.Target, c.Split, c.Ratio, ok = spawn.GridSplit(l.Panes, perTab); !ok {
		return Cell{}, refuse("tab %s is full: no cell of the grid of %d is free", tabID, perTab)
	}
	return c, nil
}

// Named checks that b's agent may share a tab: its process names it in a
// variable that ACFS's Agent Mail hook reads before the tab label. In a
// shared tab the label is the project's, so an agent that the label
// names would act as the project (nhm-4c4). It refuses (ErrRefused) when
// the environment names nobody, and warns when nhm cannot read it.
func Named(b bindings.Binding) (warning string, err error) {
	key, _, ok := bindings.MailAgentVar(bindings.Occupant{ForegroundPID: b.ForegroundPID, ForegroundStart: b.ForegroundStart})
	switch {
	case !ok:
		return fmt.Sprintf("nhm cannot read the environment of %s's agent: unless it sets AGENT_MAIL_AGENT, "+
			"ACFS's mail hook reads the label of the shared tab as its name", b.MailName), nil
	case key == "":
		return "", refuse("%s's agent sets no AGENT_MAIL_AGENT: in a shared tab, ACFS's mail hook would read the tab label as its name", b.MailName)
	}
	return "", nil
}

// recheck reads b again under its lock, as the writer that held the lock
// may have changed it, and verifies it in its pane.
func recheck(ctx context.Context, env Env, b bindings.Binding) (bindings.Binding, error) {
	cur, err := env.Bindings.Get(ctx, b.ID)
	if err != nil {
		return bindings.Binding{}, err
	}
	if cur.Status != bindings.Active || cur.Generation != b.Generation || cur.PaneID != b.PaneID {
		return bindings.Binding{}, refuse("%s's binding changed while nhm waited for it (%s generation %d in %s, now %s generation %d in %s, %s); try again",
			b.MailName, b.ID, b.Generation, b.PaneID, cur.ID, cur.Generation, cur.PaneID, cur.Status)
	}
	v, err := bindings.Verify(ctx, env.Herdr, cur)
	if err != nil {
		return bindings.Binding{}, err
	}
	if v.Verdict != bindings.Verified {
		return bindings.Binding{}, refuse("%s is %s in %s, not verified (nhm resolve %s says why)", cur.MailName, v.Verdict, cur.PaneID, cur.MailName)
	}
	return cur, nil
}

// follow records that herdr moved cur's pane from tab from to tab to of
// its workspace, where the pane keeps its id. The slot stays. The
// reconcile follows such a move too (nhm-bdc), and may have recorded it
// already.
func follow(ctx context.Context, env Env, cur bindings.Binding, from, to string) (bindings.Binding, error) {
	nb, err := env.Bindings.Get(ctx, cur.ID)
	if err == nil && nb.TabID != to {
		nb, err = env.Bindings.Relocate(ctx, cur.ID, cur.Generation, bindings.Location{WorkspaceID: cur.WorkspaceID, TabID: to,
			PaneID: cur.PaneID}, bindings.Evidence{Kind: "move", Detail: fmt.Sprintf("nhm move %s from tab %s to tab %s", cur.PaneID, from, to)})
	}
	if err != nil {
		return bindings.Binding{}, fmt.Errorf("herdr moved %s to tab %s, but the binding did not follow (%w); nhm maintain follows it on its next pass",
			cur.PaneID, to, err)
	}
	return nb, nil
}

// numeric reports whether a tab label is herdr's default, the number the
// tab had when herdr made it, as the reconcile's keepTabLabel reads it.
func numeric(label string) bool {
	for _, r := range label {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Join moves b's pane into tabID's next free cell of the grid of perTab
// (Check) under b's mutation lock, which send, recycle and retire take
// too: none of them acts on the agent while its pane moves.
func Join(ctx context.Context, env Env, b bindings.Binding, tabID string, perTab int) (Result, error) {
	lctx, cancel := context.WithTimeout(ctx, env.LockWait)
	unlock, err := send.LockTarget(lctx, env.InstanceDir, b.ID)
	cancel()
	if errors.Is(err, send.ErrTargetBusy) {
		return Result{}, refuse("%s is being sent to, recycled, retired or moved; try again", b.MailName)
	}
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	return JoinLocked(ctx, env, b, tabID, perTab)
}

// JoinLocked is Join for a caller that holds b's mutation lock, as nhm
// arrange holds the locks of all its agents. It reads the binding again,
// since the writer that held the lock may have changed it, and verifies
// it. Then it checks the tab (Check), moves the pane with herdr's
// pane.move, records the new tab in the binding (the slot stays), and
// gives the tab the project's label when its label is herdr's number or
// an agent's name: a shared tab never carries one agent's name.
func JoinLocked(ctx context.Context, env Env, b bindings.Binding, tabID string, perTab int) (Result, error) {
	res := Result{Actions: []string{}, Warnings: []string{}}
	cur, err := recheck(ctx, env, b)
	if err != nil {
		return res, err
	}
	c, err := Check(ctx, env, cur, tabID, perTab)
	if err != nil {
		return res, err
	}
	res.Cell, res.Binding = c, cur
	res.Warnings = append(res.Warnings, c.Warnings...)
	mv, err := env.Herdr.PaneMove(ctx, herdr.PaneMoveParams{PaneID: cur.PaneID, Destination: herdr.IntoTab(c.TabID, c.Split, c.Target, c.Ratio)})
	if err == nil && !mv.Changed {
		err = errors.New("herdr says nothing changed")
	}
	if err != nil {
		return res, fmt.Errorf("herdr did not move %s: %w", cur.PaneID, err)
	}
	if mv.ClosedTabID != "" {
		res.ClosedTabID = mv.ClosedTabID
		res.Actions = append(res.Actions, fmt.Sprintf("herdr closed tab %s: its last pane moved", mv.ClosedTabID))
	}
	to := mv.Pane
	if to.PaneID == "" {
		to.PaneID = cur.PaneID
	}
	if to.TabID == "" {
		to.TabID = c.TabID
	}
	if to.PaneID != cur.PaneID || to.TabID != c.TabID {
		return res, fmt.Errorf("herdr moved %s to %s in tab %s, not into tab %s; nhm maintain follows it on its next pass",
			cur.PaneID, to.PaneID, to.TabID, c.TabID)
	}
	nb, err := follow(ctx, env, cur, c.FromTab, c.TabID)
	if err != nil {
		return res, err
	}
	res.Binding = nb
	res.Actions = append(res.Actions, fmt.Sprintf("%s moved from tab %s to tab %s, cell %d, slot %s", nb.MailName, c.FromTab, c.TabID, c.Cell, nb.Slot))
	if c.NewLabel != "" {
		if _, err := env.Herdr.TabRename(ctx, c.TabID, c.NewLabel); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("tab label %s not applied to %s: %v", c.NewLabel, c.TabID, err))
		} else {
			res.Actions = append(res.Actions, fmt.Sprintf("labelled tab %s %s", c.TabID, c.NewLabel))
		}
	}
	return res, nil
}
