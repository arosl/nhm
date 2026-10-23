package tabs

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
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
	_ "github.com/arosl/nhm/internal/testenv"
)

const project = "/src/proj"

// pane is one pane of the fake herdr session.
type pane struct {
	id, tab, ws string
	kind, name  string // the agent herdr sees, "" for none, and its alias
	pid         int64
	rect        herdr.LayoutRect
}

// world is a fake herdr session whose pane.move splits the target pane
// as herdr does: the target keeps ratio of its room, rounded.
type world struct {
	mu      sync.Mutex
	panes   []*pane
	labels  map[string]string // tab id: label
	moves   []herdr.PaneMoveParams
	renamed []string
	moveErr error
}

func (w *world) find(id string) *pane {
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
	count := map[string]uint{}
	for _, p := range w.panes {
		count[p.tab]++
		s.Panes = append(s.Panes, herdr.PaneInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: p.ws, Agent: p.kind})
		if p.kind != "" {
			s.Agents = append(s.Agents, herdr.AgentInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: p.ws, Agent: p.kind, Name: p.name})
		}
	}
	for tab, label := range w.labels {
		ws, _, _ := strings.Cut(tab, ":")
		s.Tabs = append(s.Tabs, herdr.TabInfo{TabID: tab, WorkspaceID: ws, Label: label, PaneCount: count[tab]})
	}
	slices.SortFunc(s.Tabs, func(a, b herdr.TabInfo) int { return strings.Compare(a.TabID, b.TabID) })
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
	l := herdr.PaneLayout{WorkspaceID: p.ws, TabID: p.tab}
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
	if w.moveErr != nil {
		return herdr.PaneMoveResult{}, w.moveErr
	}
	p, t := w.find(m.PaneID), w.find(m.Destination.TargetPaneID)
	if p == nil || t == nil || m.Destination.Type != "tab" || t.tab != m.Destination.TabID {
		return herdr.PaneMoveResult{}, fmt.Errorf("bad move %+v", m)
	}
	from := p.tab
	r := t.rect
	if m.Destination.Split == "right" {
		keep := uint16(math.Round(float64(r.Width) * *m.Destination.Ratio))
		t.rect.Width, p.rect = keep, herdr.LayoutRect{X: r.X + keep, Y: r.Y, Width: r.Width - keep, Height: r.Height}
	} else {
		keep := uint16(math.Round(float64(r.Height) * *m.Destination.Ratio))
		t.rect.Height, p.rect = keep, herdr.LayoutRect{X: r.X, Y: r.Y + keep, Width: r.Width, Height: r.Height - keep}
	}
	p.tab = t.tab
	res := herdr.PaneMoveResult{Changed: true, Pane: herdr.PaneInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: p.ws},
		PreviousPaneID: p.id, PreviousTabID: from}
	if !slices.ContainsFunc(w.panes, func(q *pane) bool { return q.tab == from }) {
		delete(w.labels, from)
		res.ClosedTabID = from
	}
	return res, nil
}

func (w *world) TabRename(_ context.Context, tab, label string) (herdr.TabInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.labels[tab] = label
	w.renamed = append(w.renamed, tab+" "+label)
	return herdr.TabInfo{TabID: tab, Label: label}, nil
}

// harness is a fake herdr session and an nhm store whose bindings are
// its agents.
type harness struct {
	t     *testing.T
	world *world
	env   Env
	names map[string]string // pane id: mail name
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
	h := &harness{t: t, world: &world{labels: map[string]string{}}, names: map[string]string{}}
	h.env = Env{Bindings: bindings.NewStore(db), Herdr: h.world, InstanceDir: inst.Dir, LockWait: 100 * time.Millisecond}
	// Each agent process names itself, as spawn's panes do (nhm-4c4).
	old := bindings.ProcEnviron
	bindings.ProcEnviron = func(pid int64) (map[string]string, error) {
		for id, name := range h.names {
			if p := h.world.find(id); p != nil && p.pid == pid {
				return map[string]string{"AGENT_MAIL_AGENT": name}, nil
			}
		}
		return nil, errors.New("no such process")
	}
	t.Cleanup(func() { bindings.ProcEnviron = old })
	return h
}

// tab adds a tab with the given label.
func (h *harness) tab(id, label string) {
	h.world.labels[id] = label
}

// agent adds an agent's pane with its binding; key "" is the project.
func (h *harness) agent(name, id, tab, key string, r herdr.LayoutRect) bindings.Binding {
	h.t.Helper()
	ws, _, _ := strings.Cut(id, ":")
	p := &pane{id: id, tab: tab, ws: ws, kind: "claude", name: strings.ToLower(name), pid: int64(100 + len(h.world.panes)), rect: r}
	h.world.panes = append(h.world.panes, p)
	h.names[id] = name
	if key == "" {
		key = project
	}
	b, err := h.env.Bindings.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: key, ProjectID: "p-" + key,
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: ws, TabID: tab, PaneID: id,
			HerdrAlias: strings.ToLower(name), Kind: "claude", ForegroundPID: p.pid}})
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

func (h *harness) binding(id string) bindings.Binding {
	h.t.Helper()
	b, err := h.env.Bindings.Get(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return b
}

func rect(x, y, w, ht uint16) herdr.LayoutRect {
	return herdr.LayoutRect{X: x, Y: y, Width: w, Height: ht}
}

// full is a tab of its own, 120x40.
var full = rect(0, 0, 120, 40)

// A move into a tab of 3 in one row (2 rows of 3 for --per-tab 6)
// lands under the first pane, in cell 4. The binding records the new
// tab and keeps its slot and generation; herdr closes the tab left
// empty; a tab with the project's label keeps it.
func TestJoinIntoARowOfThree(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:t1", "IcyKnoll")
	h.tab("w1:t2", "proj")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
	h.agent("BlueLake", "w1:p2", "w1:t2", "", rect(0, 0, 40, 40))
	h.agent("RedFox", "w1:p3", "w1:t2", "", rect(40, 0, 40, 40))
	h.agent("GreenPike", "w1:p4", "w1:t2", "", rect(80, 0, 40, 40))
	r, err := Join(context.Background(), h.env, icy, "w1:t2", 6)
	if err != nil {
		t.Fatal(err)
	}
	m := h.world.moves
	if len(m) != 1 || m[0].PaneID != "w1:p1" || m[0].Destination.Type != "tab" || m[0].Destination.TabID != "w1:t2" ||
		m[0].Destination.TargetPaneID != "w1:p2" || m[0].Destination.Split != "down" || *m[0].Destination.Ratio != 0.5 {
		t.Fatalf("pane.move = %+v", m)
	}
	nb := h.binding(icy.ID)
	if nb.TabID != "w1:t2" || nb.PaneID != "w1:p1" || nb.WorkspaceID != "w1" || nb.Slot != icy.Slot || nb.Generation != icy.Generation ||
		nb.Status != bindings.Active || r.Binding.Slot != icy.Slot {
		t.Fatalf("binding %+v, was %+v", nb, icy)
	}
	if ev := nb.Evidence[len(nb.Evidence)-1]; ev.Kind != "move" || ev.Detail != "nhm move w1:p1 from tab w1:t1 to tab w1:t2" {
		t.Errorf("evidence = %+v", ev)
	}
	want := []string{"herdr closed tab w1:t1: its last pane moved", "IcyKnoll moved from tab w1:t1 to tab w1:t2, cell 4, slot " + icy.Slot}
	if r.Cell.Cell != 4 || r.Cell.Agents != 3 || r.Cell.FromTab != "w1:t1" || r.ClosedTabID != "w1:t1" || !slices.Equal(r.Actions, want) ||
		len(r.Warnings) != 0 || len(h.world.renamed) != 0 {
		t.Fatalf("result %+v, renamed %v", r, h.world.renamed)
	}
	if got := h.world.find("w1:p1").rect; got != rect(0, 20, 40, 20) {
		t.Errorf("the moved pane is at %+v", got)
	}
}

// A move into a tab of 5 fills the sixth cell, right of the fifth pane;
// a tab with herdr's default label (its number) takes the project's.
// A second move finds the tab full.
func TestJoinFillsTheSixthCell(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:t1", "IcyKnoll")
	h.tab("w1:t2", "7")
	h.tab("w1:t3", "TealOwl")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
	for i, r := range []herdr.LayoutRect{rect(0, 0, 40, 20), rect(40, 0, 40, 20), rect(80, 0, 40, 20), rect(0, 20, 40, 20), rect(40, 20, 80, 20)} {
		h.agent(fmt.Sprintf("Agent%d", i), fmt.Sprintf("w1:p%d", i+2), "w1:t2", "", r)
	}
	owl := h.agent("TealOwl", "w1:p9", "w1:t3", "", full)
	r, err := Join(context.Background(), h.env, icy, "w1:t2", 6)
	if err != nil {
		t.Fatal(err)
	}
	if d := h.world.moves[0].Destination; d.TargetPaneID != "w1:p6" || d.Split != "right" || *d.Ratio != 0.5 || r.Cell.Cell != 6 {
		t.Fatalf("pane.move = %+v, cell %d", d, r.Cell.Cell)
	}
	if !slices.Equal(h.world.renamed, []string{"w1:t2 proj"}) || !slices.Contains(r.Actions, "labelled tab w1:t2 proj") || r.Cell.NewLabel != "proj" {
		t.Fatalf("labels %v, actions %v", h.world.renamed, r.Actions)
	}
	if got := h.world.find("w1:p1").rect; got != rect(80, 20, 40, 20) {
		t.Errorf("the moved pane is at %+v", got)
	}
	if _, err := Join(context.Background(), h.env, owl, "w1:t2", 6); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "tab w1:t2 is full: it holds 6 panes") {
		t.Fatalf("a seventh agent: %v", err)
	}
}

// A tab labelled with its one agent's name takes the project's label:
// a shared tab never carries one agent's name. Another label stays.
func TestJoinLabelsTheSharedTab(t *testing.T) {
	for _, c := range []struct{ label, want string }{{"BlueLake", "proj"}, {"MistyDesert (pusher)", ""}} {
		h := newHarness(t)
		h.tab("w1:t1", "IcyKnoll")
		h.tab("w1:t2", c.label)
		icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
		h.agent("BlueLake", "w1:p2", "w1:t2", "", full)
		r, err := Join(context.Background(), h.env, icy, "w1:t2", 6)
		if err != nil {
			t.Fatal(err)
		}
		if d := h.world.moves[0].Destination; d.TargetPaneID != "w1:p2" || d.Split != "right" || math.Abs(*d.Ratio-1.0/3) > 1e-9 {
			t.Errorf("%s: pane.move = %+v", c.label, d)
		}
		after := c.label
		if c.want != "" {
			after = c.want
		}
		if r.Cell.NewLabel != c.want || h.world.labels["w1:t2"] != after {
			t.Errorf("%s: new label %q, tab labelled %q", c.label, r.Cell.NewLabel, h.world.labels["w1:t2"])
		}
	}
}

// Each refusal moves nothing and leaves the binding as it was.
func TestJoinRefuses(t *testing.T) {
	for _, c := range []struct {
		name, tab, want string
		setup           func(h *harness)
	}{
		{"full", "w1:t2", "tab w1:t2 is full: it holds 6 panes, and --per-tab is 6", func(h *harness) {
			for i := 0; i < 6; i++ {
				h.agent(fmt.Sprintf("Agent%d", i), fmt.Sprintf("w1:p%d", i+2), "w1:t2", "", rect(uint16(i%3)*40, uint16(i/3)*20, 40, 20))
			}
		}},
		{"off the grid", "w1:t2", "tab w1:t2 is off the grid of 6", func(h *harness) {
			h.agent("BlueLake", "w1:p2", "w1:t2", "", rect(0, 0, 60, 40))
			h.agent("RedFox", "w1:p3", "w1:t2", "", rect(60, 0, 60, 40))
		}},
		{"another project", "w1:t2", "tab w1:t2 holds GreenPike, an agent of another project (/src/other)", func(h *harness) {
			h.agent("BlueLake", "w1:p2", "w1:t2", "", rect(0, 0, 40, 40))
			h.agent("GreenPike", "w1:p3", "w1:t2", "/src/other", rect(40, 0, 80, 40))
		}},
		{"another workspace", "w2:t1", "tab w2:t1 is in workspace w2, IcyKnoll is in w1", func(h *harness) {
			h.tab("w2:t1", "proj")
			h.agent("BlueLake", "w2:p1", "w2:t1", "", full)
		}},
		{"a shell pane", "w1:t2", "tab w1:t2 holds w1:p3, a pane with no agent", func(h *harness) {
			h.agent("BlueLake", "w1:p2", "w1:t2", "", rect(0, 0, 40, 40))
			h.world.panes = append(h.world.panes, &pane{id: "w1:p3", tab: "w1:t2", ws: "w1", rect: rect(40, 0, 80, 40)})
		}},
		{"an unbound agent", "w1:t2", "tab w1:t2 holds w1:p3, a codex agent with no binding", func(h *harness) {
			h.agent("BlueLake", "w1:p2", "w1:t2", "", rect(0, 0, 40, 40))
			h.world.panes = append(h.world.panes, &pane{id: "w1:p3", tab: "w1:t2", ws: "w1", kind: "codex", rect: rect(40, 0, 80, 40)})
		}},
		{"its own tab", "w1:t1", "IcyKnoll is in tab w1:t1 already", func(*harness) {}},
		{"no tab", "w1:t7", "no tab w1:t7", func(*harness) {}},
		{"an agent that the tab label names", "w1:t2", "BlueLake's agent sets no AGENT_MAIL_AGENT", func(h *harness) {
			h.agent("BlueLake", "w1:p2", "w1:t2", "", full)
			old := bindings.ProcEnviron
			bindings.ProcEnviron = func(pid int64) (map[string]string, error) {
				if pid == h.world.find("w1:p2").pid {
					return map[string]string{"HOME": "/home/u"}, nil
				}
				return old(pid)
			}
			h.t.Cleanup(func() { bindings.ProcEnviron = old })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.tab("w1:t1", "IcyKnoll")
			h.tab("w1:t2", "proj")
			icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
			c.setup(h)
			_, err := Join(context.Background(), h.env, icy, c.tab, 6)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if len(h.world.moves) != 0 || len(h.world.renamed) != 0 {
				t.Fatalf("moves %v, labels %v", h.world.moves, h.world.renamed)
			}
			if nb := h.binding(icy.ID); nb.TabID != "w1:t1" || len(nb.Evidence) != len(icy.Evidence) {
				t.Fatalf("binding %+v", nb)
			}
		})
	}
}

// When nhm cannot read an agent's environment, the move goes on with a
// warning; a failed pane.move leaves the binding as it was.
func TestJoinWarnsAndFails(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:t1", "IcyKnoll")
	h.tab("w1:t2", "proj")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
	h.agent("BlueLake", "w1:p2", "w1:t2", "", full)
	delete(h.names, "w1:p2")
	h.world.moveErr = errors.New("pane_not_found")
	_, err := Join(context.Background(), h.env, icy, "w1:t2", 6)
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "herdr did not move w1:p1: pane_not_found") ||
		h.binding(icy.ID).TabID != "w1:t1" {
		t.Fatalf("a failed move: %v", err)
	}
	h.world.moveErr = nil
	r, err := Join(context.Background(), h.env, icy, "w1:t2", 6)
	if err != nil || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "nhm cannot read the environment of BlueLake's agent") {
		t.Fatalf("result %+v, %v", r, err)
	}
}

// Join waits for the binding's mutation lock, which send, recycle and
// retire hold, and refuses when it stays taken; JoinLocked refuses a
// binding that changed while its caller waited.
func TestJoinLocks(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:t1", "IcyKnoll")
	h.tab("w1:t2", "proj")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
	h.agent("BlueLake", "w1:p2", "w1:t2", "", full)
	unlock, err := send.LockTarget(context.Background(), h.env.InstanceDir, icy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Join(context.Background(), h.env, icy, "w1:t2", 6); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "IcyKnoll is being sent to") {
		t.Fatalf("a held lock: %v", err)
	}
	stale := icy
	stale.Generation++
	if _, err := JoinLocked(context.Background(), h.env, stale, "w1:t2", 6); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "binding changed") {
		t.Fatalf("a changed binding: %v", err)
	}
	if _, err := JoinLocked(context.Background(), h.env, icy, "w1:t2", 6); err != nil {
		t.Fatalf("the lock's holder: %v", err)
	}
	unlock()
	if len(h.world.moves) != 1 {
		t.Fatalf("moves %v", h.world.moves)
	}
}

// A tab is named by its id, by the slot N of the agents it holds now, or
// by its label in the workspace.
func TestFindTab(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:t1", "IcyKnoll")
	h.tab("w1:t2", "proj")
	h.tab("w1:t3", "dup")
	h.tab("w1:t4", "dup")
	h.tab("w2:t1", "elsewhere")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", full)
	blue := h.agent("BlueLake", "w1:p2", "w1:t2", "", full)
	h.agent("RedFox", "w1:p3", "w1:t3", "", full)
	h.agent("GreenPike", "w1:p4", "w1:t4", "", full)
	n := func(b bindings.Binding) string {
		s, err := bindings.ParseSlot(b.Slot)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(s.Tab)
	}
	for _, c := range []struct{ spec, want, refusal string }{
		{"w1:t2", "w1:t2", ""},
		{"w2:t1", "w2:t1", ""},
		{"w1:t9", "", "no tab w1:t9"},
		{n(blue), "w1:t2", ""},
		{"99", "", "no tab of workspace w1 holds an agent of slot 99"},
		{"proj", "w1:t2", ""},
		{"elsewhere", "", `no tab of workspace w1 is labelled "elsewhere"`},
		{"dup", "", `the label "dup" names 2 tabs of workspace w1 (w1:t3, w1:t4)`},
	} {
		got, err := FindTab(context.Background(), h.env, "w1", c.spec)
		if got != c.want || (c.refusal == "") != (err == nil) || (err != nil && (!errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.refusal))) {
			t.Errorf("FindTab(%q) = %q, %v; want %q, %q", c.spec, got, err, c.want, c.refusal)
		}
	}
	// After IcyKnoll moves into BlueLake's tab, slot N of each names
	// that tab; a slot whose agents are in two tabs names none.
	if _, err := Join(context.Background(), h.env, icy, "w1:t2", 6); err != nil {
		t.Fatal(err)
	}
	if got, err := FindTab(context.Background(), h.env, "w1", n(icy)); got != "w1:t2" || err != nil {
		t.Errorf("slot %s after the move = %q, %v", n(icy), got, err)
	}
	h.agent("TealOwl", "w1:p5", "w1:t1", "", full)
	h.tab("w1:t1", "TealOwl")
	if _, err := FindTab(context.Background(), h.env, "w1", n(icy)); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "are in 2 tabs (w1:t1, w1:t2)") {
		t.Errorf("slot %s in two tabs: %v", n(icy), err)
	}
}
