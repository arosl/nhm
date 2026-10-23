package arrange

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/tabs"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// fpane is one pane of the fake herdr session.
type fpane struct {
	id, tab    string
	kind, name string // the agent herdr sees, "" for none, and its alias
	pid        int64
	rect       herdr.LayoutRect
}

// world is a fake herdr workspace, w1. Its pane.move splits the target
// pane as herdr does, opens a new tab, gives the pane left alone in a
// tab the whole tab, and closes a tab left empty.
type world struct {
	mu     sync.Mutex
	panes  []*fpane
	tabs   []string          // the tab ids, in herdr's order
	labels map[string]string // tab id: label
	moves  []herdr.PaneMoveParams
	swaps  []herdr.PaneSwapParams
	failAt int // the move or swap, from 1, that herdr refuses; 0 for none
	// misreport is the move, from 1, that herdr makes but answers with
	// another tab than the pane's; 0 for none.
	misreport int
	newTabs   int
}

func (w *world) find(id string) *fpane {
	for _, p := range w.panes {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (w *world) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var s herdr.SessionSnapshot
	for _, tb := range w.tabs {
		s.Tabs = append(s.Tabs, herdr.TabInfo{TabID: tb, WorkspaceID: "w1", Label: w.labels[tb]})
	}
	for _, p := range w.panes {
		s.Panes = append(s.Panes, herdr.PaneInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: "w1", Agent: p.kind})
		if p.kind != "" {
			s.Agents = append(s.Agents, herdr.AgentInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: "w1", Agent: p.kind, Name: p.name})
		}
	}
	return s, nil
}

func (w *world) PaneProcessInfo(_ context.Context, id string) (herdr.ProcessInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	info := herdr.ProcessInfo{PaneID: id}
	if p := w.find(id); p != nil && p.pid != 0 {
		info.ForegroundProcesses = []herdr.Process{{PID: uint32(p.pid)}}
	}
	return info, nil
}

func (w *world) AgentRename(context.Context, string, string) (herdr.AgentInfo, error) {
	return herdr.AgentInfo{}, errors.New("not in this fake")
}

func (w *world) PaneLayout(_ context.Context, id string) (herdr.PaneLayout, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.find(id)
	if p == nil {
		return herdr.PaneLayout{}, errors.New("pane not found")
	}
	l := herdr.PaneLayout{WorkspaceID: "w1", TabID: p.tab}
	for _, q := range w.panes {
		if q.tab == p.tab {
			l.Panes = append(l.Panes, herdr.LayoutPane{PaneID: q.id, Rect: q.rect})
		}
	}
	return l, nil
}

func (w *world) PaneMove(_ context.Context, m herdr.PaneMoveParams) (herdr.PaneMoveResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moves = append(w.moves, m)
	if len(w.moves)+len(w.swaps) == w.failAt {
		return herdr.PaneMoveResult{}, errors.New("herdr: pane_move_failed")
	}
	p := w.find(m.PaneID)
	if p == nil {
		return herdr.PaneMoveResult{}, fmt.Errorf("bad move %+v", m)
	}
	from := p.tab
	res := herdr.PaneMoveResult{Changed: true, PreviousPaneID: p.id, PreviousTabID: from}
	switch d := m.Destination; d.Type {
	case "new_tab":
		w.newTabs++
		tb := fmt.Sprintf("w1:tn%d", w.newTabs)
		w.tabs, w.labels[tb] = append(w.tabs, tb), d.Label
		p.tab, p.rect = tb, herdr.LayoutRect{Width: 240, Height: 60}
		res.CreatedTab = &herdr.TabInfo{TabID: tb, WorkspaceID: "w1", Label: d.Label}
	case "tab":
		t := w.find(d.TargetPaneID)
		if t == nil || t.tab != d.TabID || d.Ratio == nil {
			return herdr.PaneMoveResult{}, fmt.Errorf("bad move %+v", m)
		}
		r := t.rect
		if d.Split == "right" {
			keep := uint16(math.Round(float64(r.Width) * *d.Ratio))
			t.rect.Width, p.rect = keep, herdr.LayoutRect{X: r.X + keep, Y: r.Y, Width: r.Width - keep, Height: r.Height}
		} else {
			keep := uint16(math.Round(float64(r.Height) * *d.Ratio))
			t.rect.Height, p.rect = keep, herdr.LayoutRect{X: r.X, Y: r.Y + keep, Width: r.Width, Height: r.Height - keep}
		}
		p.tab = t.tab
	default:
		return herdr.PaneMoveResult{}, fmt.Errorf("bad move %+v", m)
	}
	res.Pane = herdr.PaneInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: "w1"}
	if len(w.moves) == w.misreport {
		res.Pane.TabID = "w1:tX"
	}
	var left []*fpane
	for _, q := range w.panes {
		if q.tab == from {
			left = append(left, q)
		}
	}
	switch len(left) {
	case 0:
		w.tabs = slices.DeleteFunc(w.tabs, func(tb string) bool { return tb == from })
		delete(w.labels, from)
		res.ClosedTabID = from
	case 1:
		left[0].rect = herdr.LayoutRect{Width: 240, Height: 60}
	}
	return res, nil
}

// PaneSwap exchanges two panes' rects, as herdr does within a tab, and
// changes nothing across two tabs (reason cross_tab). It counts as a move
// for failAt.
func (w *world) PaneSwap(_ context.Context, s herdr.PaneSwapParams) (herdr.PaneSwapResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.swaps = append(w.swaps, s)
	if len(w.moves)+len(w.swaps) == w.failAt {
		return herdr.PaneSwapResult{}, errors.New("herdr: pane_swap_failed")
	}
	a, b := w.find(s.SourcePaneID), w.find(s.TargetPaneID)
	if a == nil || b == nil {
		return herdr.PaneSwapResult{SourcePaneID: s.SourcePaneID, Reason: "not_found"}, nil
	}
	res := herdr.PaneSwapResult{SourcePaneID: a.id, TargetPaneID: b.id, Reason: "cross_tab"}
	if a.tab == b.tab {
		a.rect, b.rect = b.rect, a.rect
		res.Changed, res.Reason = true, ""
	}
	res.Layout = herdr.PaneLayout{WorkspaceID: "w1", TabID: a.tab}
	for _, q := range w.panes {
		if q.tab == a.tab {
			res.Layout.Panes = append(res.Layout.Panes, herdr.LayoutPane{PaneID: q.id, Rect: q.rect})
		}
	}
	return res, nil
}

func (w *world) TabRename(_ context.Context, tab, label string) (herdr.TabInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.labels[tab] = label
	return herdr.TabInfo{TabID: tab, Label: label}, nil
}

// layout lists each tab, its label and its panes in GridOrder, with "!"
// after a tab that is not clean: "w1:tA proj [w1:pA1 w1:pA2]".
func (w *world) layout(perTab int) string {
	var out []string
	for _, tb := range w.tabs {
		l, _ := w.PaneLayout(context.Background(), w.firstIn(tb))
		order, clean := spawn.GridOrder(l.Panes, perTab)
		mark := ""
		if !clean {
			mark = "!"
		}
		out = append(out, fmt.Sprintf("%s %s %v%s", tb, w.labels[tb], order, mark))
	}
	return strings.Join(out, "; ")
}

func (w *world) firstIn(tab string) string {
	for _, p := range w.panes {
		if p.tab == tab {
			return p.id
		}
	}
	return ""
}

// harness is a fake herdr workspace and an nhm store whose bindings are
// its agents.
type harness struct {
	t       *testing.T
	world   *world
	env     tabs.Env
	named   map[int64]string // pid: the AGENT_MAIL_AGENT its process sets
	byName  map[string]bindings.Binding
	project string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{t: t, world: &world{labels: map[string]string{}}, named: map[int64]string{},
		byName: map[string]bindings.Binding{}, project: proj}
	h.env = tabs.Env{Bindings: bindings.NewStore(db), Herdr: h.world, InstanceDir: inst.Dir, LockWait: 100 * time.Millisecond}
	old := bindings.ProcEnviron
	bindings.ProcEnviron = func(pid int64) (map[string]string, error) {
		if name, ok := h.named[pid]; ok {
			return map[string]string{"AGENT_MAIL_AGENT": name}, nil
		}
		return map[string]string{"PATH": "/usr/bin"}, nil
	}
	t.Cleanup(func() { bindings.ProcEnviron = old })
	return h
}

// tab adds tab id, labelled label, with the agents named (their panes
// "w1:p<name>") placed by GridSplit on the grid of perTab, in the order
// given. A name that starts with "-" is a pane with no agent. Each
// agent gets its binding and the tab's next slot.
func (h *harness) tab(id, label string, perTab int, names ...string) {
	h.t.Helper()
	h.world.tabs, h.world.labels[id] = append(h.world.tabs, id), label
	var panes []herdr.LayoutPane
	for _, n := range names {
		panes = join(panes, "w1:p"+n, perTab)
	}
	for i, n := range names {
		p := &fpane{id: panes[i].PaneID, tab: id, rect: panes[i].Rect}
		h.world.panes = append(h.world.panes, p)
		if strings.HasPrefix(n, "-") {
			continue
		}
		p.kind, p.name, p.pid = "claude", strings.ToLower(n), int64(100+len(h.world.panes))
		h.named[p.pid] = n
		b, err := h.env.Bindings.Create(context.Background(), bindings.New{MailName: n, MailProjectKey: h.project, ProjectID: "p",
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: id, PaneID: p.id,
				HerdrAlias: p.name, Kind: "claude", ForegroundPID: p.pid}})
		if err != nil {
			h.t.Fatal(err)
		}
		h.byName[n] = b
	}
}

func (h *harness) binding(name string) bindings.Binding {
	h.t.Helper()
	b, err := h.env.Bindings.Get(context.Background(), h.byName[name].ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

// The bead's case on a fake herdr: 7 agents in 4 tabs of 1, 1, 2 and 3,
// beside a pusher's own tab and a tab with a shell pane. --dry-run moves
// nothing. The run fills the first agent's tab with the first 6 in slot
// order, and leaves the seventh alone in its tab; herdr closes the tabs
// left empty; each binding records its new tab and keeps its slot. A
// second run moves nothing.
func TestRunArrangesSevenAgents(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:tA", "A1", 6, "A1")
	h.tab("w1:tB", "A2", 6, "A2")
	h.tab("w1:tC", "proj", 6, "A3", "A4")
	h.tab("w1:tD", "proj", 6, "A5", "A6", "A7")
	h.tab("w1:tP", "MistyDesert (pusher)", 6, "MistyDesert")
	h.tab("w1:tS", "proj", 6, "S1", "-shell")
	slots := map[string]string{}
	for n := range h.byName {
		slots[n] = h.binding(n).Slot
	}
	ctx := context.Background()

	res, err := Run(ctx, h.env, Request{WorkspaceID: "w1", PerTab: 6, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.world.moves) != 0 || len(res.Done) != 0 {
		t.Fatalf("--dry-run moved: %+v", h.world.moves)
	}
	if got, want := moves(res.Plan), "A2→w1:tA:2 A3→w1:tA:3 A4→w1:tA:4 A5→w1:tA:5 A6→w1:tA:6"; got != want {
		t.Errorf("--dry-run: moves %s, want %s", got, want)
	}
	if len(res.Plan.Left) != 2 {
		t.Errorf("--dry-run: left out %+v, want the pusher's tab and the shell's", res.Plan.Left)
	}

	res, err = Run(ctx, h.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Done) != 5 || len(h.world.moves) != 5 {
		t.Fatalf("done %+v, herdr moves %d; want 5", res.Done, len(h.world.moves))
	}
	if got, want := h.world.layout(6), "w1:tA proj [w1:pA1 w1:pA2 w1:pA3 w1:pA4 w1:pA5 w1:pA6]; w1:tD proj [w1:pA7]; "+
		"w1:tP MistyDesert (pusher) [w1:pMistyDesert]; w1:tS proj [w1:pS1 w1:p-shell]"; got != want {
		t.Errorf("layout\n %s\nwant\n %s", got, want)
	}
	for _, n := range []string{"A2", "A3", "A4", "A5", "A6"} {
		if b := h.binding(n); b.TabID != "w1:tA" || b.Slot != slots[n] || b.Generation != h.byName[n].Generation {
			t.Errorf("%s: tab %s slot %s generation %d; want w1:tA, %s, %d", n, b.TabID, b.Slot, b.Generation, slots[n], h.byName[n].Generation)
		}
	}
	if !slices.ContainsFunc(res.Done, func(d Done) bool { return d.ClosedTab == "w1:tB" }) {
		t.Errorf("done %+v: no closed w1:tB", res.Done)
	}

	again, err := Run(ctx, h.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err != nil || len(again.Plan.Moves) != 0 || len(h.world.moves) != 5 {
		t.Errorf("a second run: %v, moves %s", err, moves(again.Plan))
	}
}

// A tab on the grid with its agents out of slot order is sorted in place
// with pane.swap: no pane leaves it, A4 joins it after the three, the
// bindings keep their tab and slot, and a second run moves nothing
// (nhm-her).
func TestRunSortsATabInPlace(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:tA", "proj", 6, "A1", "A2", "A3")
	h.tab("w1:tB", "A4", 6, "A4")
	// A1, the first slot, has the middle cell: herdr's order is A2 A1 A3.
	for _, p := range h.world.panes {
		switch p.id {
		case "w1:pA1":
			p.rect.X = 80
		case "w1:pA2":
			p.rect.X = 0
		}
	}
	before := map[string]string{}
	for _, n := range []string{"A1", "A2", "A3"} {
		before[n] = h.binding(n).Slot
	}
	res, err := Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moves(res.Plan), "A1⇄A2:1 A4→w1:tA:4"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
	if s := h.world.swaps; len(s) != 1 || s[0] != (herdr.PaneSwapParams{SourcePaneID: "w1:pA1", TargetPaneID: "w1:pA2"}) {
		t.Errorf("pane.swap = %+v", s)
	}
	if len(h.world.moves) != 1 || h.world.moves[0].PaneID != "w1:pA4" {
		t.Errorf("pane.move = %+v, want A4's only", h.world.moves)
	}
	if got, want := h.world.layout(6), "w1:tA proj [w1:pA1 w1:pA2 w1:pA3 w1:pA4]"; got != want {
		t.Errorf("layout %s, want %s", got, want)
	}
	if len(res.Done) != 2 || !res.Done[0].Swap || res.Done[0].With != "A2" || res.Done[0].Cell != 1 {
		t.Errorf("done %+v", res.Done)
	}
	for n, slot := range before {
		if b := h.binding(n); b.TabID != "w1:tA" || b.Slot != slot || b.Generation != 1 {
			t.Errorf("%s: tab %s slot %s generation %d, want w1:tA %s 1", n, b.TabID, b.Slot, b.Generation, slot)
		}
	}
	if again, err := Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: 6}); err != nil || len(again.Plan.Moves) != 0 {
		t.Errorf("a second run: %v, moves %s", err, moves(again.Plan))
	}
}

// A tab off the grid cannot take the rest of its group: its first agent
// moves to a new tab, labelled with its name, and the others join it
// there; the tab then takes the project's label.
func TestRunOpensANewTab(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:tA", "proj", 6, "A1", "A2", "A3")
	// Halves, not the grid's thirds, laid out by hand: A3 below A2.
	for _, p := range h.world.panes {
		switch p.id {
		case "w1:pA1":
			p.rect = herdr.LayoutRect{Width: 120, Height: 60}
		case "w1:pA2":
			p.rect = herdr.LayoutRect{X: 120, Width: 120, Height: 30}
		case "w1:pA3":
			p.rect = herdr.LayoutRect{X: 120, Y: 30, Width: 120, Height: 30}
		}
	}
	res, err := Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moves(res.Plan), "A1→new:1 A2→new0:2 A3→new0:3"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
	if got, want := h.world.layout(6), "w1:tn1 proj [w1:pA1 w1:pA2 w1:pA3]"; got != want {
		t.Errorf("layout %s, want %s", got, want)
	}
	if res.Plan.Groups[0].TabID != "w1:tn1" || h.binding("A1").TabID != "w1:tn1" {
		t.Errorf("group tab %s, A1's tab %s; want w1:tn1", res.Plan.Groups[0].TabID, h.binding("A1").TabID)
	}
	if d := h.world.moves[0].Destination; d.Type != "new_tab" || d.WorkspaceID != "w1" || d.Label != "A1" {
		t.Errorf("the first move: %+v, want a new tab of w1 labelled A1", d)
	}
	if len(h.world.swaps) != 0 {
		t.Errorf("pane.swap = %+v, want none", h.world.swaps)
	}
}

// While another nhm command holds the lock of one of the agents it
// arranges, nhm arrange refuses and moves nothing; that agent may be in
// a group that does not move (--per-tab 2: A3 stays alone in its tab).
func TestRunRefusesWhileAnAgentIsBusy(t *testing.T) {
	for _, c := range []struct {
		busy   string
		perTab int
	}{{"A3", 6}, {"A3", 2}, {"A1", 6}} {
		h := newHarness(t)
		h.tab("w1:tA", "A1", 6, "A1")
		h.tab("w1:tB", "A2", 6, "A2")
		h.tab("w1:tC", "A3", 6, "A3")
		unlock, err := send.LockTarget(context.Background(), h.env.InstanceDir, h.byName[c.busy].ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: c.perTab})
		unlock()
		if !errors.Is(err, tabs.ErrRefused) || !strings.Contains(err.Error(), c.busy+" is being sent to") {
			t.Errorf("%s busy, --per-tab %d: err %v, want a refusal that names it", c.busy, c.perTab, err)
		}
		if len(h.world.moves) != 0 {
			t.Errorf("%s busy, --per-tab %d: herdr moves %+v, want none", c.busy, c.perTab, h.world.moves)
		}
	}
}

// A move that fails stops the run: the moves made stay, and the error,
// not a refusal, names them. A pane that herdr moved before a later step
// of its move failed counts as moved.
func TestRunStopsAtAFailedMove(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:tA", "A1", 6, "A1")
	h.tab("w1:tB", "A2", 6, "A2")
	h.tab("w1:tC", "A3", 6, "A3")
	h.world.failAt = 2
	res, err := Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err == nil || errors.Is(err, tabs.ErrRefused) || !strings.Contains(err.Error(), "A3: ") ||
		!strings.Contains(err.Error(), "1 of 2 moves made (A2 to w1:tA)") {
		t.Fatalf("err %v, want A3's failure after 1 of 2 moves", err)
	}
	if len(res.Done) != 1 || res.Done[0].MailName != "A2" || h.binding("A2").TabID != "w1:tA" {
		t.Errorf("done %+v, A2's tab %s; want A2 in w1:tA", res.Done, h.binding("A2").TabID)
	}
	// herdr moves A2 but answers with another tab: the move fails after
	// the pane moved.
	hm := newHarness(t)
	hm.tab("w1:tA", "A1", 6, "A1")
	hm.tab("w1:tB", "A2", 6, "A2")
	hm.tab("w1:tC", "A3", 6, "A3")
	hm.world.misreport = 1
	res, err = Run(context.Background(), hm.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err == nil || errors.Is(err, tabs.ErrRefused) || !strings.Contains(err.Error(), "1 of 2 moves made (A2 to w1:tA)") {
		t.Fatalf("a misreported move: err %v, want A2 counted as moved", err)
	}
	if len(res.Done) != 1 || res.Done[0].TabID != "w1:tA" || res.Done[0].FromTab != "w1:tB" {
		t.Errorf("a misreported move: done %+v", res.Done)
	}
	// herdr fails the swap that sorts a tab: an error, and nothing moved.
	hs := newHarness(t)
	hs.tab("w1:tA", "proj", 6, "A1", "A2")
	p1, p2 := hs.world.find("w1:pA1"), hs.world.find("w1:pA2")
	p1.rect, p2.rect = p2.rect, p1.rect
	hs.world.failAt = 1
	res, err = Run(context.Background(), hs.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err == nil || errors.Is(err, tabs.ErrRefused) || !strings.Contains(err.Error(), "A1: herdr did not swap w1:pA1 and w1:pA2") ||
		!strings.Contains(err.Error(), "nothing moved") || len(res.Done) != 0 {
		t.Errorf("a failed swap: err %v, done %+v", err, res.Done)
	}
	// The first move refused: a refusal, and nothing moved.
	h2 := newHarness(t)
	h2.tab("w1:tA", "A1", 6, "A1")
	h2.tab("w1:tB", "A2", 6, "A2")
	h2.world.failAt = 1
	if _, err := Run(context.Background(), h2.env, Request{WorkspaceID: "w1", PerTab: 6}); err == nil ||
		!strings.Contains(err.Error(), "nothing moved") {
		t.Errorf("err %v, want A2's failure with nothing moved", err)
	}
}

// An agent whose process names it in no variable that ACFS's mail hook
// reads must not share a tab: its tab is left as it is. An agent whose
// environment nhm cannot read gets a warning when it shares one, once,
// though each join into its tab warns again.
func TestRunNamesItsAgents(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:tA", "A1", 6, "A1")
	h.tab("w1:tB", "A2", 6, "A2")
	h.tab("w1:tC", "A3", 6, "A3")
	h.tab("w1:tD", "A4", 6, "A4")
	delete(h.named, h.byName["A3"].ForegroundPID)
	old := bindings.ProcEnviron
	bindings.ProcEnviron = func(pid int64) (map[string]string, error) {
		if pid == h.byName["A2"].ForegroundPID {
			return nil, errors.New("permission denied")
		}
		return old(pid)
	}
	t.Cleanup(func() { bindings.ProcEnviron = old })
	res, err := Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: 6, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := moves(res.Plan), "A2→w1:tA:2 A4→w1:tA:3"; got != want {
		t.Errorf("moves %s, want %s", got, want)
	}
	if len(res.Plan.Left) != 1 || res.Plan.Left[0].TabID != "w1:tC" || !strings.Contains(res.Plan.Left[0].Reason, "A3's agent sets no AGENT_MAIL_AGENT") {
		t.Errorf("left out %+v, want w1:tC for A3's environment", res.Plan.Left)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "environment of A2's agent") {
		t.Errorf("warnings %q, want one for A2", res.Warnings)
	}
	res, err = Run(context.Background(), h.env, Request{WorkspaceID: "w1", PerTab: 6})
	if err != nil || len(res.Done) != 2 {
		t.Fatalf("run: %v, done %+v", err, res.Done)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "environment of A2's agent") {
		t.Errorf("run: warnings %q, want one for A2", res.Warnings)
	}
}
