package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// tabWorld is a herdr session for nhm move --tab, in workspace w1:
// IcyKnoll alone in tab w1:t1; BlueLake, RedFox and GreenPike in one row
// of tab w1:t2, labelled with the project's label; GreenPike's pane
// belongs to another project when other is set. pane.move into a tab
// splits the target pane, which keeps the ratio of its room, rounded.
type tabWorld struct {
	mu     sync.Mutex
	panes  map[string]map[string]any
	rects  map[string]herdr.LayoutRect
	labels map[string]string
}

func moveTabInstance(t *testing.T, other bool) (*herdrtest.Server, store.Instance, *tabWorld, string) {
	t.Helper()
	s, inst := cliInstance(t)
	project := filepath.Join(t.TempDir(), "proj")
	w := &tabWorld{panes: map[string]map[string]any{}, rects: map[string]herdr.LayoutRect{},
		labels: map[string]string{"w1:t1": "IcyKnoll", "w1:t2": "proj"}}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, a := range []struct {
		name, pane, tab string
		rect            herdr.LayoutRect
	}{
		{"IcyKnoll", "w1:p1", "w1:t1", herdr.LayoutRect{Width: 120, Height: 40}},
		{"BlueLake", "w1:p2", "w1:t2", herdr.LayoutRect{Width: 40, Height: 40}},
		{"RedFox", "w1:p3", "w1:t2", herdr.LayoutRect{X: 40, Width: 40, Height: 40}},
		{"GreenPike", "w1:p4", "w1:t2", herdr.LayoutRect{X: 80, Width: 40, Height: 40}},
	} {
		alias, session := strings.ToLower(a.name), "sess-"+strings.ToLower(a.name)
		w.panes[a.pane], w.rects[a.pane] = agentJSON(a.pane, a.tab, "w1", "claude", alias, session), a.rect
		key := project
		if other && a.name == "GreenPike" {
			key = "/src/other"
		}
		if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: a.name, MailProjectKey: key, ProjectID: "p-" + key,
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: a.tab, PaneID: a.pane, HerdrAlias: alias,
				Kind: "claude", NativeSession: &bindings.NativeSession{Value: session}}}); err != nil {
			t.Fatal(err)
		}
	}
	tabJSON := func(id string) map[string]any {
		n := 0
		for _, p := range w.panes {
			if p["tab_id"] == id {
				n++
			}
		}
		return map[string]any{"tab_id": id, "workspace_id": "w1", "number": 1, "label": w.labels[id], "focused": false,
			"pane_count": n, "agent_status": "idle"}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		var panes, tabs []any
		for _, id := range slices.Sorted(maps.Keys(w.panes)) {
			panes = append(panes, w.panes[id])
		}
		for _, id := range slices.Sorted(maps.Keys(w.labels)) {
			tabs = append(tabs, tabJSON(id))
		}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": tabs, "panes": panes, "agents": panes,
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": false, "pane_count": len(panes),
				"tab_count": len(tabs), "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID}}, nil
	})
	s.Handle("pane.layout", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		tab := w.panes[p.PaneID]["tab_id"]
		panes := []any{}
		for _, id := range slices.Sorted(maps.Keys(w.panes)) {
			if w.panes[id]["tab_id"] == tab {
				panes = append(panes, map[string]any{"pane_id": id, "rect": w.rects[id]})
			}
		}
		return map[string]any{"type": "pane_layout", "layout": map[string]any{"workspace_id": "w1", "tab_id": tab, "zoomed": false,
			"area": herdr.LayoutRect{Width: 120, Height: 40}, "focused_pane_id": p.PaneID, "panes": panes, "splits": []any{}}}, nil
	})
	s.Handle("pane.move", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID      string                    `json:"pane_id"`
			Destination herdr.PaneMoveDestination `json:"destination"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		d := p.Destination
		target, ok := w.rects[d.TargetPaneID]
		if !ok || d.Type != "tab" || w.panes[d.TargetPaneID]["tab_id"] != d.TabID {
			return nil, &herdrtest.Error{Code: "invalid_params", Message: fmt.Sprintf("bad move %s", r.Params)}
		}
		if d.Split == "right" {
			keep := uint16(math.Round(float64(target.Width) * *d.Ratio))
			w.rects[d.TargetPaneID] = herdr.LayoutRect{X: target.X, Y: target.Y, Width: keep, Height: target.Height}
			w.rects[p.PaneID] = herdr.LayoutRect{X: target.X + keep, Y: target.Y, Width: target.Width - keep, Height: target.Height}
		} else {
			keep := uint16(math.Round(float64(target.Height) * *d.Ratio))
			w.rects[d.TargetPaneID] = herdr.LayoutRect{X: target.X, Y: target.Y, Width: target.Width, Height: keep}
			w.rects[p.PaneID] = herdr.LayoutRect{X: target.X, Y: target.Y + keep, Width: target.Width, Height: target.Height - keep}
		}
		from := w.panes[p.PaneID]["tab_id"].(string)
		w.panes[p.PaneID]["tab_id"] = d.TabID
		var closed any
		if tabJSON(from)["pane_count"] == 0 {
			delete(w.labels, from)
			closed = from
		}
		return map[string]any{"type": "pane_move", "move_result": map[string]any{
			"changed": true, "pane": w.panes[p.PaneID], "previous_pane_id": p.PaneID, "previous_tab_id": from,
			"previous_workspace_id": "w1", "focused_pane_id": "w1:p1", "target_layout": map[string]any{},
			"closed_tab_id": closed, "closed_workspace_id": nil,
		}}, nil
	})
	s.Handle("tab.rename", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			TabID string `json:"tab_id"`
			Label string `json:"label"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.labels[p.TabID] = p.Label
		return map[string]any{"type": "tab_info", "tab": tabJSON(p.TabID)}, nil
	})
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	return s, inst, w, project
}

// nhm move --tab puts IcyKnoll into the row of 3, under its first pane:
// the tab is named by its slot N or its label; --dry-run calls no
// pane.move; the binding records the new tab and keeps its slot and
// generation; herdr closes the tab left empty; a second move is refused.
func TestMoveIntoTab(t *testing.T) {
	s, inst, w, _ := moveTabInstance(t, false)
	before := binding(t, inst, "IcyKnoll")
	n, err := bindings.ParseSlot(binding(t, inst, "BlueLake").Slot)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"IcyKnoll"},
		{"IcyKnoll", "--tab", "w1:t2", "--workspace", "swarm"},
		{"IcyKnoll", "--workspace", "swarm", "--per-tab", "4"},
	} {
		if code, _, errOut := run(append([]string{"move", "--socket", s.SocketPath}, args...)...); code != exitRefused || !strings.Contains(errOut, "--tab T [--per-tab N]") {
			t.Errorf("move %v = %d %s", args, code, errOut)
		}
	}
	code, out, errOut := run("move", "IcyKnoll", "--tab", fmt.Sprint(n.Tab), "--dry-run", "--socket", s.SocketPath)
	if want := "would move IcyKnoll (claude) from tab w1:t1 to cell 4 of 6 in tab w1:t2 \"proj\" (split w1:p2 down)\n"; code != exitOK || out != want || len(moves(s)) != 0 {
		t.Fatalf("dry run = %d %q %s; moves %v", code, out, errOut, moves(s))
	}
	// nhm cannot read the agents' environment here (no process).
	if !strings.Contains(errOut, "warning: nhm cannot read the environment of IcyKnoll's agent") {
		t.Errorf("dry run warnings: %s", errOut)
	}

	code, out, errOut = run("move", "IcyKnoll", "--tab", "proj", "--socket", s.SocketPath, "--json")
	if code != exitOK {
		t.Fatalf("move = %d %s %s", code, out, errOut)
	}
	if got := moves(s); len(got) != 1 || got[0] != `{"pane_id":"w1:p1","destination":{"type":"tab","tab_id":"w1:t2","split":"down","target_pane_id":"w1:p2","ratio":0.5}}` {
		t.Fatalf("pane.move params = %v", got)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	after := binding(t, inst, "IcyKnoll")
	if after.TabID != "w1:t2" || after.PaneID != "w1:p1" || after.Slot != before.Slot || after.Generation != before.Generation ||
		res["to_tab_id"] != "w1:t2" || res["to_pane_id"] != "w1:p1" || res["slot"] != before.Slot {
		t.Fatalf("after the move: binding %+v, result %v", after, res)
	}
	if cell := res["cell"].(map[string]any); cell["cell"] != 4.0 || cell["target_pane_id"] != "w1:p2" || cell["from_tab_id"] != "w1:t1" {
		t.Errorf("cell = %v", cell)
	}
	if acts, _ := res["actions"].([]any); !slices.Contains(acts, any("herdr closed tab w1:t1: its last pane moved")) ||
		!slices.Contains(acts, any("IcyKnoll moved from tab w1:t1 to tab w1:t2, cell 4, slot "+before.Slot)) {
		t.Errorf("actions = %v", res["actions"])
	}
	w.mu.Lock()
	rc := w.rects["w1:p1"]
	w.mu.Unlock()
	if rc != (herdr.LayoutRect{Y: 20, Width: 40, Height: 20}) {
		t.Errorf("the moved pane is at %+v", rc)
	}
	if code, out, _ := run("resolve", "IcyKnoll", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "verification: verified") {
		t.Errorf("resolve after the move = %d %s", code, out)
	}
	if code, _, errOut := run("move", "IcyKnoll", "--tab", "w1:t2", "--socket", s.SocketPath); code != exitRefused ||
		!strings.Contains(errOut, "IcyKnoll is in tab w1:t2 already") || len(moves(s)) != 1 {
		t.Errorf("a second move = %d %s", code, errOut)
	}
}

// A tab that holds another project's agent is refused, and nothing
// moves; a dry run is refused the same way.
func TestMoveIntoTabRefusesAnotherProjectsTab(t *testing.T) {
	s, inst, _, _ := moveTabInstance(t, true)
	for _, dry := range []string{"--dry-run", "--json"} {
		code, out, errOut := run("move", "IcyKnoll", "--tab", "w1:t2", dry, "--socket", s.SocketPath)
		if code != exitRefused || !strings.Contains(errOut+out, "tab w1:t2 holds GreenPike, an agent of another project (/src/other)") {
			t.Errorf("%s: %d %s %s", dry, code, out, errOut)
		}
	}
	if len(moves(s)) != 0 || binding(t, inst, "IcyKnoll").TabID != "w1:t1" {
		t.Fatalf("moves %v", moves(s))
	}
}
