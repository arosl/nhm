package spawn

import (
	"context"
	"errors"
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
	// The first split cuts the wide pane to the right; the next cuts the
	// biggest pane left, now as tall as it is wide, down.
	if strings.Join(w.h.splits, ",") != "w1:p1 right,w1:p1 down" {
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

func TestSplitTarget(t *testing.T) {
	pane := func(id string, w, h uint16) herdr.LayoutPane {
		return herdr.LayoutPane{PaneID: id, Rect: herdr.LayoutRect{Width: w, Height: h}}
	}
	for _, c := range []struct {
		name      string
		panes     []herdr.LayoutPane
		err       error
		pane, dir string
	}{
		{"wide", []herdr.LayoutPane{pane("a", 240, 60)}, nil, "a", "right"},
		{"square cell area", []herdr.LayoutPane{pane("a", 120, 60), pane("b", 120, 60)}, nil, "a", "down"},
		{"biggest", []herdr.LayoutPane{pane("a", 120, 30), pane("b", 120, 60), pane("c", 120, 30)}, nil, "b", "down"},
		{"tall", []herdr.LayoutPane{pane("a", 80, 50)}, nil, "a", "down"},
		{"no layout", nil, errors.New("down"), "anchor", "right"},
		{"empty layout", nil, nil, "anchor", "right"},
	} {
		got, dir := splitTarget(context.Background(), layoutHerdr{l: herdr.PaneLayout{Panes: c.panes}, err: c.err}, "anchor")
		if got != c.pane || dir != c.dir {
			t.Errorf("%s: split %s %s, want %s %s", c.name, got, dir, c.pane, c.dir)
		}
	}
}
