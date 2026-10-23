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

// arrangeWorld is a herdr session for nhm arrange, in workspace w1
// "swarm", 120x40 a tab: A1 and A2 in tab w1:t1 "proj", A2 in the first
// cell and A1, the first slot, in the rest; A3 alone in w1:t2 "A3";
// MistyDesert alone in w1:t3 "MistyDesert (pusher)". pane.move splits
// the target pane, which keeps the ratio of its room, or opens a new
// tab, w1:t9; a pane left alone gets the whole tab, and a tab left
// empty is closed.
type arrangeWorld struct {
	mu     sync.Mutex
	panes  map[string]map[string]any
	rects  map[string]herdr.LayoutRect
	labels map[string]string
}

func arrangeInstance(t *testing.T) (*herdrtest.Server, store.Instance, *arrangeWorld) {
	t.Helper()
	s, inst := cliInstance(t)
	project := filepath.Join(t.TempDir(), "proj")
	full := herdr.LayoutRect{Width: 120, Height: 40}
	w := &arrangeWorld{panes: map[string]map[string]any{}, rects: map[string]herdr.LayoutRect{},
		labels: map[string]string{"w1:t1": "proj", "w1:t2": "A3", "w1:t3": "MistyDesert (pusher)"}}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, a := range []struct {
		name, pane, tab string
		rect            herdr.LayoutRect
	}{
		{"A1", "w1:p1", "w1:t1", herdr.LayoutRect{X: 40, Width: 80, Height: 40}},
		{"A2", "w1:p2", "w1:t1", herdr.LayoutRect{Width: 40, Height: 40}},
		{"A3", "w1:p3", "w1:t2", full},
		{"MistyDesert", "w1:p4", "w1:t3", full},
	} {
		alias, session := strings.ToLower(a.name), "sess-"+strings.ToLower(a.name)
		w.panes[a.pane], w.rects[a.pane] = agentJSON(a.pane, a.tab, "w1", "claude", alias, session), a.rect
		if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: a.name, MailProjectKey: project, ProjectID: "p",
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
			"area": full, "focused_pane_id": p.PaneID, "panes": panes, "splits": []any{}}}, nil
	})
	s.Handle("pane.move", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID      string                    `json:"pane_id"`
			Destination herdr.PaneMoveDestination `json:"destination"`
		}
		_ = json.Unmarshal(r.Params, &p)
		w.mu.Lock()
		defer w.mu.Unlock()
		d, from := p.Destination, w.panes[p.PaneID]["tab_id"].(string)
		var created any
		switch d.Type {
		case "new_tab":
			if d.WorkspaceID != "w1" {
				return nil, &herdrtest.Error{Code: "invalid_params", Message: fmt.Sprintf("bad move %s", r.Params)}
			}
			w.labels["w1:t9"] = d.Label
			w.panes[p.PaneID]["tab_id"], w.rects[p.PaneID] = "w1:t9", full
			created = tabJSON("w1:t9")
		case "tab":
			target, ok := w.rects[d.TargetPaneID]
			if !ok || w.panes[d.TargetPaneID]["tab_id"] != d.TabID {
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
			w.panes[p.PaneID]["tab_id"] = d.TabID
		default:
			return nil, &herdrtest.Error{Code: "invalid_params", Message: fmt.Sprintf("bad move %s", r.Params)}
		}
		var closed any
		var left []string
		for id, q := range w.panes {
			if q["tab_id"] == from {
				left = append(left, id)
			}
		}
		switch len(left) {
		case 0:
			delete(w.labels, from)
			closed = from
		case 1:
			w.rects[left[0]] = full
		}
		return map[string]any{"type": "pane_move", "move_result": map[string]any{
			"changed": true, "pane": w.panes[p.PaneID], "previous_pane_id": p.PaneID, "previous_tab_id": from,
			"previous_workspace_id": "w1", "focused_pane_id": "w1:p1", "target_layout": map[string]any{},
			"created_tab": created, "closed_tab_id": closed, "closed_workspace_id": nil,
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
	return s, inst, w
}

func paneMoves(s *herdrtest.Server) []string {
	var out []string
	for _, r := range s.Requests() {
		if r.Method == "pane.move" {
			out = append(out, string(r.Params))
		}
	}
	return out
}

// nhm arrange on the stub, whose every request and result the pinned
// schema checks: it refuses unless nhm owns the server; --dry-run shows
// the plan and moves nothing; the run moves A1, the first slot, to a new
// tab, since its tab holds A2 first, and A2 and A3 join it there, in
// slot order; the pusher's tab stays; a second run moves nothing.
func TestArrangeVerb(t *testing.T) {
	s, inst, w := arrangeInstance(t)
	for _, args := range [][]string{{"swarm", "other"}, {"swarm", "--per-tab", "0"}} {
		if code, _, errOut := run(append([]string{"arrange", "--socket", s.SocketPath}, args...)...); code != exitRefused ||
			!strings.Contains(errOut, "Usage: nhm arrange") {
			t.Errorf("arrange %v = %d %s", args, code, errOut)
		}
	}
	if code, _, errOut := run("arrange", "swarm", "--dry-run", "--socket", s.SocketPath); code != exitRefused || !strings.Contains(errOut, "owner is acfs-bash") {
		t.Fatalf("arrange under acfs-bash = %d %s", code, errOut)
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	before := map[string]bindings.Binding{}
	for _, n := range []string{"A1", "A2", "A3"} {
		before[n] = binding(t, inst, n)
	}

	code, out, errOut := run("arrange", "swarm", "--dry-run", "--socket", s.SocketPath)
	want := `would arrange 3 agents of workspace w1 in 1 tab of up to 6:
  a new tab: A1 A2 A3
  would move A1 (slot 0.1) from tab w1:t1 to a new tab "A1", cell 1
  would move A2 (slot 0.2) from tab w1:t1 to the new tab, cell 2
  would move A3 (slot 1.1) from tab w1:t2 to the new tab, cell 3
left as they are:
  tab w1:t3 "MistyDesert (pusher)": its label "MistyDesert (pusher)" is neither the project's ("proj") nor its agent's name
`
	if code != exitOK || out != want || len(paneMoves(s)) != 0 {
		t.Fatalf("dry run = %d\n%s\nwant\n%s\n%s; moves %v", code, out, want, errOut, paneMoves(s))
	}

	code, out, errOut = run("arrange", "swarm", "--socket", s.SocketPath, "--json")
	if code != exitOK {
		t.Fatalf("arrange = %d %s %s", code, out, errOut)
	}
	var env struct {
		Result struct {
			Plan struct {
				Groups []struct {
					TabID  string   `json:"tab_id"`
					Agents []string `json:"agents"`
				} `json:"groups"`
			} `json:"plan"`
			Done []struct {
				MailName string `json:"mail_name"`
				TabID    string `json:"tab_id"`
				Cell     int    `json:"cell"`
			} `json:"done"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if g := env.Result.Plan.Groups; len(g) != 1 || g[0].TabID != "w1:t9" {
		t.Errorf("groups %+v, want one in w1:t9", g)
	}
	if got := fmt.Sprint(env.Result.Done); got != "[{A1 w1:t9 1} {A2 w1:t9 2} {A3 w1:t9 3}]" {
		t.Errorf("done %s", got)
	}
	if mv := paneMoves(s); len(mv) != 3 || !strings.Contains(mv[0], `"type":"new_tab"`) || !strings.Contains(mv[0], `"label":"A1"`) {
		t.Errorf("herdr moves %v", mv)
	}
	if got, want := fmt.Sprint(w.labels), `map[w1:t3:MistyDesert (pusher) w1:t9:proj]`; got != want {
		t.Errorf("tabs %s, want %s", got, want)
	}
	for _, n := range []string{"A1", "A2", "A3"} {
		if b := binding(t, inst, n); b.TabID != "w1:t9" || b.Slot != before[n].Slot || b.Generation != before[n].Generation {
			t.Errorf("%s: tab %s slot %s generation %d, want w1:t9 %s %d", n, b.TabID, b.Slot, b.Generation, before[n].Slot, before[n].Generation)
		}
	}

	code, out, _ = run("arrange", "swarm", "--socket", s.SocketPath)
	if code != exitOK || !strings.HasPrefix(out, "nothing to arrange in workspace w1: 3 agents in 1 tab of up to 6") || len(paneMoves(s)) != 3 {
		t.Errorf("a second arrange = %d %q, moves %d", code, out, len(paneMoves(s)))
	}
}
