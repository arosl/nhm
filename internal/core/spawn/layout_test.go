package spawn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// one is a request for one Claude agent of project p, with the default
// number of agents per tab.
func one(p string) Request {
	return Request{Session: "swarm", ProjectRoot: p, ProjectID: "proj" + p, Agents: []Want{{Kind: "claude", Count: 1}}, NoKickoff: true}
}

func slots(res Result) string {
	var out []string
	for _, t := range res.Targets {
		out = append(out, t.Slot)
	}
	return strings.Join(out, ",")
}

// Agents of one project spawned one at a time share a tab; the tab has
// the agent's name while it holds one, then the project's (nhm-4c4).
func TestSpawnsOfOneProjectShareATab(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	first, err := Spawn(ctx, w.env, one("/srv/work/acme"))
	if err != nil || slots(first) != "0.1" || w.h.tabLabel["w1:t1"] != "IcyKnoll" {
		t.Fatalf("first = %+v %v, labels %v", first.Targets, err, w.h.tabLabel)
	}
	add := one("/srv/work/acme")
	add.Add = true
	for i, want := range []string{"0.2", "0.3"} {
		res, err := Spawn(ctx, w.env, add)
		if err != nil || slots(res) != want || res.Targets[0].TabID != "w1:t1" {
			t.Fatalf("add %d = %+v %v", i, res.Targets, err)
		}
	}
	if w.h.tabLabel["w1:t1"] != "acme" || len(w.h.tabs) != 1 {
		t.Fatalf("tabs %v, labels %v", w.h.tabs, w.h.tabLabel)
	}
	// The first row of the 3x2 grid fills from left to right: the first
	// split leaves a third, and the next cuts the rest in two (nhm-cxh).
	if strings.Join(w.h.splits, ",") != "w1:p1 right,w1:p2 right" {
		t.Fatalf("splits = %v", w.h.splits)
	}
	for _, p := range []string{"w1:p1", "w1:p2", "w1:p3"} {
		if w.h.envs[p]["AGENT_MAIL_AGENT"] == "" || w.h.envs[p]["AGENT_MAIL_AGENT"] != w.h.envs[p]["NHM_AGENT"] {
			t.Fatalf("env of %s = %v", p, w.h.envs[p])
		}
	}
	// Another project's agent gets a tab of its own.
	other := one("/srv/work/other")
	other.Add = true
	res, err := Spawn(ctx, w.env, other)
	if err != nil || slots(res) != "1.1" || w.h.tabLabel[res.Targets[0].TabID] != "GreenPike" {
		t.Fatalf("other = %+v %v, labels %v", res.Targets, err, w.h.tabLabel)
	}
}

// Six fill a tab; the seventh opens the next. A dry run shows the slots
// the real spawn gives.
func TestSeventhAgentOpensATab(t *testing.T) {
	w := newWorld(t)
	w.mail.names = []string{"A1", "A2", "A3", "A4", "A5", "A6", "A7"}
	r := one("/srv/work/acme")
	r.Agents[0].Count = 7
	r.DryRun = true
	dry, err := Spawn(context.Background(), w.env, r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || slots(res) != "0.1,0.2,0.3,0.4,0.5,0.6,1.1" || slots(dry) != slots(res) {
		t.Fatalf("slots = %s, dry %s, %v", slots(res), slots(dry), err)
	}
	if res.Targets[5].TabID != res.Targets[0].TabID || res.Targets[6].TabID == res.Targets[0].TabID ||
		w.h.tabLabel[res.Targets[0].TabID] != "acme" || w.h.tabLabel[res.Targets[6].TabID] != "A7" {
		t.Fatalf("targets = %+v, labels %v", res.Targets, w.h.tabLabel)
	}
}

// A tab is not joined when it is from before nhm-4c4 (its spawn's request
// has no paneEnvKey), when it holds an unbound agent, when the user
// relabelled it, or when the request has --label or --group or a tab per
// agent.
func TestTabsThatAreNotJoined(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, w *world, first Result)
		edit  func(r *Request)
	}{
		{"older spawn", func(t *testing.T, w *world, first Result) {
			if _, err := w.db.SQL().Exec(`UPDATE operations SET request_json = '{"session":"swarm"}' WHERE operation_id = ?`,
				first.OperationID); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"unbound agent", func(t *testing.T, w *world, first Result) {
			p, err := w.h.PaneSplit(ctx, herdr.PaneSplitParams{Direction: "down", TargetPaneID: first.Targets[0].PaneID})
			if err != nil {
				t.Fatal(err)
			}
			w.h.agents[p.PaneID] = herdr.AgentInfo{PaneID: p.PaneID, Agent: "codex"}
		}, nil},
		{"user label", func(_ *testing.T, w *world, first Result) { w.h.tabLabel[first.Targets[0].TabID] = "mine" }, nil},
		{"group", nil, func(r *Request) { r.Group = "g" }},
		{"label", nil, func(r *Request) { r.Label = "L" }},
		{"a tab each", nil, func(r *Request) { r.PerTab = 1 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			first, err := Spawn(ctx, w.env, one("/srv/work/acme"))
			if err != nil {
				t.Fatal(err)
			}
			if c.setup != nil {
				c.setup(t, w, first)
			}
			add := one("/srv/work/acme")
			add.Add = true
			if c.edit != nil {
				c.edit(&add)
			}
			res, err := Spawn(ctx, w.env, add)
			if err != nil || slots(res) != "1.1" || res.Targets[0].TabID == first.Targets[0].TabID {
				t.Fatalf("add = %+v %v", res.Targets, err)
			}
		})
	}
}

// A retired agent's slot is never given again; its tab still has room.
func TestJoinAfterARetire(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := one("/srv/work/acme")
	r.Agents[0].Count = 2
	first, err := Spawn(ctx, w.env, r)
	if err != nil || slots(first) != "0.1,0.2" {
		t.Fatalf("first = %+v %v", first.Targets, err)
	}
	b, err := w.env.Bindings.Get(ctx, first.Targets[1].BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.env.Bindings.Retire(ctx, b.ID, b.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	_ = w.h.PaneClose(ctx, b.PaneID)
	add := one("/srv/work/acme")
	add.Add = true
	res, err := Spawn(ctx, w.env, add)
	if err != nil || slots(res) != "0.3" || res.Targets[0].TabID != first.Targets[0].TabID {
		t.Fatalf("add = %+v %v", res.Targets, err)
	}
}

// The tab takes the project's label when the second pane is split, before
// its agent starts: an agent that then fails leaves no tab of two panes
// under the first agent's name, and the next add still joins it.
func TestAFailedSecondAgentLeavesTheProjectLabel(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.h.startErr[2] = errors.New("the agent did not start")
	r := one("/srv/work/acme")
	r.Agents[0].Count = 2
	res, _ := Spawn(ctx, w.env, r)
	if len(res.Targets) != 2 || res.Targets[1].Ready || res.Targets[1].PaneID == "" {
		t.Fatalf("targets = %+v", res.Targets)
	}
	tab := res.Targets[0].TabID
	if w.h.tabLabel[tab] != "acme" {
		t.Fatalf("label = %q", w.h.tabLabel[tab])
	}
	delete(w.h.startErr, 2)
	add := one("/srv/work/acme")
	add.Add = true
	next, err := Spawn(ctx, w.env, add)
	if err != nil || len(next.Targets) != 1 || next.Targets[0].TabID != tab {
		t.Fatalf("add = %+v %v", next.Targets, err)
	}
}

type layoutHerdr struct {
	Herdr
	l   herdr.PaneLayout
	err error
}

func (h layoutHerdr) PaneLayout(context.Context, string) (herdr.PaneLayout, error) { return h.l, h.err }

func TestGrid(t *testing.T) {
	for perTab, want := range map[int]string{0: "1x1", 1: "1x1", 2: "2x1", 3: "3x1", 4: "2x2", 5: "3x2", 6: "3x2",
		7: "4x2", 8: "4x2", 9: "3x3", 10: "4x3", 12: "4x3", 16: "4x4"} {
		if c, r := grid(perTab); fmt.Sprintf("%dx%d", c, r) != want {
			t.Errorf("grid(%d) = %dx%d, want %s", perTab, c, r, want)
		}
	}
}

func TestSplitTarget(t *testing.T) {
	pane := func(id string, x, y, w, h uint16) herdr.LayoutPane {
		return herdr.LayoutPane{PaneID: id, Rect: herdr.LayoutRect{X: x, Y: y, Width: w, Height: h}}
	}
	// A 2x3 grid of 240x60 with b, the middle of the top row, closed:
	// herdr gave its room to e, below it.
	gap := []herdr.LayoutPane{pane("a", 0, 0, 80, 30), pane("d", 0, 30, 80, 30), pane("e", 80, 0, 80, 60),
		pane("c", 160, 0, 80, 30), pane("f", 160, 30, 80, 30)}
	// A tab of 3 that nhm 0.1.8 made: the biggest pane cut in half.
	old := []herdr.LayoutPane{pane("a", 0, 0, 76, 26), pane("c", 0, 26, 76, 26), pane("b", 76, 0, 76, 52)}
	full := []herdr.LayoutPane{pane("a", 0, 0, 80, 30), pane("b", 80, 0, 80, 30), pane("c", 160, 0, 80, 30),
		pane("d", 0, 30, 80, 30), pane("e", 80, 30, 80, 30), pane("f", 160, 30, 80, 30)}
	for _, c := range []struct {
		name      string
		panes     []herdr.LayoutPane
		perTab    int
		err       error
		pane, dir string
		ratio     float64
	}{
		{"first row: a third", []herdr.LayoutPane{pane("a", 0, 0, 240, 60)}, 6, nil, "a", "right", 1.0 / 3},
		{"first row: the rest in two", []herdr.LayoutPane{pane("a", 0, 0, 80, 60), pane("b", 80, 0, 160, 60)}, 6, nil, "b", "right", 0.5},
		{"second row", []herdr.LayoutPane{pane("a", 0, 0, 80, 60), pane("b", 80, 0, 80, 60), pane("c", 160, 0, 80, 60)}, 6, nil, "a", "down", 0.5},
		{"second row, middle", []herdr.LayoutPane{pane("a", 0, 0, 80, 30), pane("d", 0, 30, 80, 30), pane("b", 80, 0, 80, 60),
			pane("c", 160, 0, 80, 60)}, 6, nil, "b", "down", 0.5},
		{"a closed pane's gap", gap, 6, nil, "e", "down", 0.5},
		{"an older tab", old, 6, nil, "b", "right", (152.0/3*2 - 76) / 76},
		{"origin not 0", []herdr.LayoutPane{pane("a", 10, 5, 240, 60)}, 6, nil, "a", "right", 1.0 / 3},
		{"2x2", []herdr.LayoutPane{pane("a", 0, 0, 120, 60), pane("b", 120, 0, 120, 60)}, 4, nil, "a", "down", 0.5},
		{"one row of 3", []herdr.LayoutPane{pane("a", 0, 0, 80, 60), pane("b", 80, 0, 160, 60)}, 3, nil, "b", "right", 0.5},
		{"3x3: the second row", []herdr.LayoutPane{pane("a", 0, 0, 80, 60), pane("b", 80, 0, 80, 60), pane("c", 160, 0, 80, 60)}, 9,
			nil, "a", "down", 1.0 / 3},
		{"3x3: the third row", []herdr.LayoutPane{pane("a", 0, 0, 80, 20), pane("d", 0, 20, 80, 40), pane("b", 80, 0, 80, 20),
			pane("e", 80, 20, 80, 40), pane("c", 160, 0, 80, 20), pane("f", 160, 20, 80, 40)}, 9, nil, "d", "down", 0.5},
		// Every cell has a pane of its own: the biggest pane is cut in
		// half along its longer side, a cell about twice as tall as wide.
		{"full: wide", full, 6, nil, "a", "right", 0.5},
		{"full: square cell area", []herdr.LayoutPane{pane("a", 0, 0, 120, 60), pane("b", 120, 0, 120, 60)}, 2, nil, "a", "down", 0.5},
		{"full: biggest", []herdr.LayoutPane{pane("a", 0, 0, 120, 30), pane("b", 0, 30, 120, 60), pane("c", 0, 90, 120, 30)}, 1,
			nil, "b", "down", 0.5},
		{"full: tall", []herdr.LayoutPane{pane("a", 0, 0, 80, 50)}, 1, nil, "a", "down", 0.5},
		{"no layout", nil, 6, errors.New("down"), "anchor", "right", 0.5},
		{"empty layout", nil, 6, nil, "anchor", "right", 0.5},
	} {
		got, dir, ratio := splitTarget(context.Background(), layoutHerdr{l: herdr.PaneLayout{Panes: c.panes}, err: c.err}, "anchor", c.perTab)
		if got != c.pane || dir != c.dir || math.Abs(ratio-c.ratio) > 1e-9 {
			t.Errorf("%s: split %s %s %.4f, want %s %s %.4f", c.name, got, dir, ratio, c.pane, c.dir, c.ratio)
		}
	}
}

// rectOf is the place of the pane that holds slot in the fake's tab.
func rectOf(t *testing.T, w *world, res Result, slot string) herdr.LayoutRect {
	t.Helper()
	for _, tg := range res.Targets {
		if tg.Slot == slot {
			return w.h.rects[tg.PaneID]
		}
	}
	t.Fatalf("no slot %s in %+v", slot, res.Targets)
	return herdr.LayoutRect{}
}

// A full tab of 6 is 2 rows of 3 even panes, the slots in reading order
// (nhm-cxh). The fake's tab is 240x60.
func TestSixAgentsMakeA2x3Grid(t *testing.T) {
	w := newWorld(t)
	w.mail.names = []string{"A1", "A2", "A3", "A4", "A5", "A6"}
	r := one("/srv/work/acme")
	r.Agents[0].Count = 6
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || slots(res) != "0.1,0.2,0.3,0.4,0.5,0.6" {
		t.Fatalf("spawn = %s %v", slots(res), err)
	}
	for k := 0; k < 6; k++ {
		want := herdr.LayoutRect{X: uint16(k%3) * 80, Y: uint16(k/3) * 30, Width: 80, Height: 30}
		if got := rectOf(t, w, res, fmt.Sprintf("0.%d", k+1)); got != want {
			t.Errorf("slot 0.%d at %+v, want %+v", k+1, got, want)
		}
	}
}

// Agents added one at a time grow the same grid (nhm-cxh). A tab of 2 is
// a third and two thirds of the width; a tab of 4 has the first column
// cut in two beside two full-height columns. No pane is narrower than a
// third.
func TestAddsGrowTheGrid(t *testing.T) {
	w := newWorld(t)
	w.mail.names = []string{"A1", "A2", "A3", "A4", "A5", "A6"}
	ctx := context.Background()
	var all Result
	for i := 0; i < 6; i++ {
		r := one("/srv/work/acme")
		r.Add = i > 0
		res, err := Spawn(ctx, w.env, r)
		if err != nil || len(res.Targets) != 1 || res.Targets[0].Slot != fmt.Sprintf("0.%d", i+1) {
			t.Fatalf("add %d = %+v %v", i, res.Targets, err)
		}
		all.Targets = append(all.Targets, res.Targets...)
		switch i {
		case 1:
			if a, b := rectOf(t, w, all, "0.1"), rectOf(t, w, all, "0.2"); a.Width != 80 || b.X != 80 || b.Width != 160 || b.Height != 60 {
				t.Fatalf("a tab of 2: %+v %+v", a, b)
			}
		case 3:
			if a, d := rectOf(t, w, all, "0.1"), rectOf(t, w, all, "0.4"); a.Height != 30 || d.X != 0 || d.Y != 30 ||
				rectOf(t, w, all, "0.2").Height != 60 || rectOf(t, w, all, "0.3").Height != 60 {
				t.Fatalf("a tab of 4: %+v %+v", a, d)
			}
		}
	}
	for k := 0; k < 6; k++ {
		want := herdr.LayoutRect{X: uint16(k%3) * 80, Y: uint16(k/3) * 30, Width: 80, Height: 30}
		if got := rectOf(t, w, all, fmt.Sprintf("0.%d", k+1)); got != want {
			t.Errorf("slot 0.%d at %+v, want %+v", k+1, got, want)
		}
	}
}
