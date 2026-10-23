package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// splitTree serves the tabs a spawn makes as herdr 0.9.3 lays them out
// (nhm-cxh): tab.create opens a tab of one pane, 152x52 as on the ACFS
// host; pane.split replaces a pane with a split of it and a new pane, the
// pane keeping its share, the ratio, rounded to whole cells (seen in the
// sandbox); pane.layout gives the rects of the tab's panes; tab.rename
// keeps the label. session.snapshot lists these tabs and panes, each pane
// with a claude agent, so a later add finds the tab and joins it.
type splitTree struct {
	mu     sync.Mutex
	n      int
	tabs   []string
	roots  map[string]*tile // by tab
	leaves map[string]*tile // by pane
	labels map[string]string
}

// tile is a pane, or a split of two tiles.
type tile struct {
	pane, tab     string
	dir           string
	ratio         float64
	first, second *tile
}

func newSplitTree(s *herdrtest.Server) *splitTree {
	st := &splitTree{n: 10, roots: map[string]*tile{}, leaves: map[string]*tile{}, labels: map[string]string{}}
	paneMap := func(pane, tab string) map[string]any {
		return map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": "w1", "terminal_id": "t-" + pane,
			"agent_status": "idle", "focused": false, "revision": 1}
	}
	tabMap := func(tab string) map[string]any {
		return map[string]any{"tab_id": tab, "workspace_id": "w1", "number": 1, "label": st.labels[tab], "focused": false,
			"pane_count": len(st.rects(tab)), "agent_status": "idle"}
	}
	s.Handle("tab.create", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(r.Params, &p)
		st.mu.Lock()
		defer st.mu.Unlock()
		st.n++
		pane, tab := "w1:p"+strconv.Itoa(st.n), "w1:t"+strconv.Itoa(st.n)
		st.tabs = append(st.tabs, tab)
		st.roots[tab] = &tile{pane: pane, tab: tab}
		st.leaves[pane], st.labels[tab] = st.roots[tab], p.Label
		return map[string]any{"type": "tab_created", "tab": tabMap(tab), "root_pane": paneMap(pane, tab)}, nil
	})
	s.Handle("pane.split", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target    string   `json:"target_pane_id"`
			Direction string   `json:"direction"`
			Ratio     *float64 `json:"ratio"`
		}
		_ = json.Unmarshal(r.Params, &p)
		st.mu.Lock()
		defer st.mu.Unlock()
		l, ok := st.leaves[p.Target]
		if !ok {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane " + p.Target}
		}
		ratio := 0.5
		if p.Ratio != nil {
			ratio = *p.Ratio
		}
		st.n++
		pane := "w1:p" + strconv.Itoa(st.n)
		kept, made := &tile{pane: l.pane, tab: l.tab}, &tile{pane: pane, tab: l.tab}
		*l = tile{tab: l.tab, dir: p.Direction, ratio: ratio, first: kept, second: made}
		st.leaves[kept.pane], st.leaves[pane] = kept, made
		return map[string]any{"type": "pane_info", "pane": paneMap(pane, l.tab)}, nil
	})
	s.Handle("pane.layout", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		st.mu.Lock()
		defer st.mu.Unlock()
		l, ok := st.leaves[p.PaneID]
		if !ok {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane " + p.PaneID}
		}
		panes := []any{}
		for pane, rc := range st.rects(l.tab) {
			panes = append(panes, map[string]any{"pane_id": pane, "focused": false, "rect": rc})
		}
		return map[string]any{"type": "pane_layout", "layout": map[string]any{"workspace_id": "w1", "tab_id": l.tab, "zoomed": false,
			"area": herdr.LayoutRect{Width: 152, Height: 52}, "focused_pane_id": p.PaneID, "panes": panes, "splits": []any{}}}, nil
	})
	s.Handle("tab.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
			Label string `json:"label"`
		}
		_ = json.Unmarshal(r.Params, &p)
		st.mu.Lock()
		defer st.mu.Unlock()
		st.labels[p.TabID] = p.Label
		return map[string]any{"type": "tab_info", "tab": tabMap(p.TabID)}, nil
	})
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		st.mu.Lock()
		defer st.mu.Unlock()
		tabs, panes := []any{}, []any{}
		for _, tab := range st.tabs {
			tabs = append(tabs, tabMap(tab))
			for pane := range st.rects(tab) {
				pm := paneMap(pane, tab)
				pm["agent"] = "claude"
				panes = append(panes, pm)
			}
		}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": tabs, "panes": panes, "agents": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": len(panes), "tab_count": len(tabs), "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	return st
}

// rects are the places of the tab's panes, in its 152x52 area. The caller
// holds st.mu.
func (st *splitTree) rects(tab string) map[string]herdr.LayoutRect {
	out := map[string]herdr.LayoutRect{}
	var walk func(t *tile, r herdr.LayoutRect)
	walk = func(t *tile, r herdr.LayoutRect) {
		if t.pane != "" {
			out[t.pane] = r
			return
		}
		a, b := r, r
		if t.dir == "right" {
			a.Width = uint16(math.Round(float64(r.Width) * t.ratio))
			b.X, b.Width = r.X+a.Width, r.Width-a.Width
		} else {
			a.Height = uint16(math.Round(float64(r.Height) * t.ratio))
			b.Y, b.Height = r.Y+a.Height, r.Height-a.Height
		}
		walk(t.first, a)
		walk(t.second, b)
	}
	if root := st.roots[tab]; root != nil {
		walk(root, herdr.LayoutRect{Width: 152, Height: 52})
	}
	return out
}

// place is where the pane is: its tab and rect.
func (st *splitTree) place(pane string) (string, herdr.LayoutRect) {
	st.mu.Lock()
	defer st.mu.Unlock()
	l, ok := st.leaves[pane]
	if !ok {
		return "", herdr.LayoutRect{}
	}
	return l.tab, st.rects(l.tab)[pane]
}

// gridWorld is a send instance with spawn's handlers, a split tree and
// Agent Mail names for the agents, owned by nhm. It returns the socket
// and the project.
func gridWorld(t *testing.T, names ...string) (*splitTree, string, string) {
	t.Helper()
	s, inst, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	st := newSplitTree(s)
	spawnAgentMail(t, names...)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAll(t, filepath.Join(home, ".acfs/onboard/docs/ntm/command_palette.md"),
		"## A\n### default_new_agent | Default\nRead AGENTS.md.\n")
	t.Setenv("PATH", t.TempDir()) // no dcg: the argv cannot be screened
	ownNhm(t, inst)
	return st, s.SocketPath, t.TempDir()
}

// spawnSlots runs a spawn or an add and gives each agent's pane by slot.
func spawnSlots(t *testing.T, args ...string) map[string]string {
	t.Helper()
	code, out, errOut := run(append(args, "--allow-unguarded", "--json")...)
	if code != 0 {
		t.Fatalf("%v = %d %s %s", args, code, out, errOut)
	}
	panes := map[string]string{}
	for _, a := range decodeEnvelope(t, out)["result"].(map[string]any)["agents"].([]any) {
		m := a.(map[string]any)
		panes[m["slot"].(string)] = m["pane_id"].(string)
	}
	return panes
}

// grid2x3 is a full tab of 6 in a 152x52 tab: 2 rows of 3, the slots in
// reading order (nhm-cxh).
var grid2x3 = []herdr.LayoutRect{
	{X: 0, Y: 0, Width: 51, Height: 26}, {X: 51, Y: 0, Width: 50, Height: 26}, {X: 101, Y: 0, Width: 51, Height: 26},
	{X: 0, Y: 26, Width: 51, Height: 26}, {X: 51, Y: 26, Width: 50, Height: 26}, {X: 101, Y: 26, Width: 51, Height: 26},
}

// checkGrid checks that slots 2.1 to 2.6 are the 2x3 grid of one tab,
// labelled with the project's directory name.
func checkGrid(t *testing.T, st *splitTree, panes map[string]string, project string) {
	t.Helper()
	tab0, _ := st.place(panes["2.1"])
	for k, want := range grid2x3 {
		slot := fmt.Sprintf("2.%d", k+1)
		tab, got := st.place(panes[slot])
		if tab != tab0 || got != want {
			t.Errorf("slot %s in %s at %+v, want %s at %+v", slot, tab, got, tab0, want)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.labels[tab0] != filepath.Base(project) || len(st.rects(tab0)) != 6 {
		t.Errorf("tab %s: label %q, panes %v", tab0, st.labels[tab0], st.rects(tab0))
	}
}

// A spawn of 6 fills one tab as 2 rows of 3 even panes, through herdr's
// pane.split and pane.layout as the stub checks them (nhm-cxh).
func TestSpawnOfSixMakesA2x3Grid(t *testing.T) {
	st, sock, project := gridWorld(t, "GreenPike", "RedStone", "AmberFox", "CoralBay", "MossyFern", "SilverOak")
	panes := spawnSlots(t, "spawn", "swarm", "--cc=6", "--project", project, "--socket", sock)
	if len(panes) != 6 {
		t.Fatalf("panes = %v", panes)
	}
	checkGrid(t, st, panes, project)
}

// Adds of 2, then 1 at a time, grow the same grid in the same tab. A tab
// of 2 is a third and two thirds of the width; a tab of 4 has the first
// column cut in two beside two full-height columns (nhm-cxh).
func TestAddsGrowA2x3Grid(t *testing.T) {
	st, sock, project := gridWorld(t, "GreenPike", "RedStone", "AmberFox", "CoralBay", "MossyFern", "SilverOak")
	add := []string{"add", "swarm", "--project", project, "--socket", sock}
	panes := spawnSlots(t, append(add, "--cc=2")...)
	if _, a := st.place(panes["2.1"]); a.Width != 51 || a.Height != 52 {
		t.Fatalf("a tab of 2: 2.1 at %+v", a)
	}
	if _, b := st.place(panes["2.2"]); b.X != 51 || b.Width != 101 || b.Height != 52 {
		t.Fatalf("a tab of 2: 2.2 at %+v", b)
	}
	for i := 3; i <= 6; i++ {
		got := spawnSlots(t, append(add, "--cc=1")...)
		slot := fmt.Sprintf("2.%d", i)
		if got[slot] == "" || len(got) != 1 {
			t.Fatalf("add %d = %v", i, got)
		}
		panes[slot] = got[slot]
		if i == 4 {
			var rects []string
			for _, s := range []string{"2.1", "2.2", "2.3", "2.4"} {
				_, r := st.place(panes[s])
				rects = append(rects, fmt.Sprintf("%d,%d %dx%d", r.X, r.Y, r.Width, r.Height))
			}
			if strings.Join(rects, " ") != "0,0 51x26 51,0 50x52 101,0 51x52 0,26 51x26" {
				t.Fatalf("a tab of 4: %v", rects)
			}
		}
	}
	checkGrid(t, st, panes, project)
}
