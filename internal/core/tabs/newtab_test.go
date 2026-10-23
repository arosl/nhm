package tabs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// newTabWorld is the fake herdr session, whose pane.move also opens a
// new tab, <workspace>:tn, that the pane fills.
type newTabWorld struct{ *world }

func (w newTabWorld) PaneMove(ctx context.Context, m herdr.PaneMoveParams) (herdr.PaneMoveResult, error) {
	if m.Destination.Type != "new_tab" {
		return w.world.PaneMove(ctx, m)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moves = append(w.moves, m)
	if w.moveErr != nil {
		return herdr.PaneMoveResult{}, w.moveErr
	}
	p := w.find(m.PaneID)
	if p == nil || m.Destination.WorkspaceID != p.ws {
		return herdr.PaneMoveResult{}, fmt.Errorf("bad move %+v", m)
	}
	from := p.tab
	p.tab, p.rect = p.ws+":tn", full
	w.labels[p.tab] = m.Destination.Label
	return herdr.PaneMoveResult{Changed: true, Pane: herdr.PaneInfo{PaneID: p.id, TabID: p.tab, WorkspaceID: p.ws},
		PreviousPaneID: p.id, PreviousTabID: from, CreatedTab: &herdr.TabInfo{TabID: p.tab, WorkspaceID: p.ws, Label: m.Destination.Label}}, nil
}

// NewTabLocked moves an agent out of a shared tab into a new tab of its
// workspace, with the label asked for (nhm arrange's first agent of a
// group). The pane keeps its id; the binding records the new tab and
// keeps its slot and generation. It refuses an agent alone in its tab
// and a binding that changed while its caller waited, and moves nothing
// then; herdr's failure is an error.
func TestNewTabLocked(t *testing.T) {
	h := newHarness(t)
	h.env.Herdr = newTabWorld{h.world}
	h.tab("w1:t1", "proj")
	h.tab("w1:t2", "RedFox")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", rect(0, 0, 40, 40))
	blue := h.agent("BlueLake", "w1:p2", "w1:t1", "", rect(40, 0, 40, 40))
	h.agent("GreenPike", "w1:p4", "w1:t1", "", rect(80, 0, 40, 40))
	red := h.agent("RedFox", "w1:p3", "w1:t2", "", full)
	ctx := context.Background()

	if _, err := NewTabLocked(ctx, h.env, red, "RedFox"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "RedFox is alone in tab w1:t2 already") {
		t.Errorf("an agent alone: %v", err)
	}
	stale := icy
	stale.Generation++
	if _, err := NewTabLocked(ctx, h.env, stale, "IcyKnoll"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "binding changed") {
		t.Errorf("a changed binding: %v", err)
	}
	if len(h.world.moves) != 0 {
		t.Fatalf("a refusal moved: %+v", h.world.moves)
	}

	r, err := NewTabLocked(ctx, h.env, icy, "IcyKnoll")
	if err != nil {
		t.Fatal(err)
	}
	if m := h.world.moves; len(m) != 1 || m[0].PaneID != "w1:p1" ||
		m[0].Destination != (herdr.PaneMoveDestination{Type: "new_tab", WorkspaceID: "w1", Label: "IcyKnoll"}) {
		t.Fatalf("pane.move = %+v", m)
	}
	nb := h.binding(icy.ID)
	if nb.TabID != "w1:tn" || nb.PaneID != "w1:p1" || nb.Slot != icy.Slot || nb.Generation != icy.Generation {
		t.Fatalf("binding %+v, was %+v", nb, icy)
	}
	if ev := nb.Evidence[len(nb.Evidence)-1]; ev.Kind != "move" || ev.Detail != "nhm move w1:p1 from tab w1:t1 to tab w1:tn" {
		t.Errorf("evidence = %+v", ev)
	}
	want := []string{"IcyKnoll moved from tab w1:t1 to a new tab w1:tn (IcyKnoll), slot " + icy.Slot}
	if r.Cell.TabID != "w1:tn" || r.Cell.FromTab != "w1:t1" || r.Cell.Cell != 1 || r.Binding.TabID != "w1:tn" || !slices.Equal(r.Actions, want) {
		t.Errorf("result %+v", r)
	}

	h.world.moveErr = errors.New("herdr: pane_move_failed")
	if _, err := NewTabLocked(ctx, h.env, blue, "BlueLake"); err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "herdr did not move w1:p2") {
		t.Errorf("herdr's failure: %v", err)
	}
	if b := h.binding(blue.ID); b.TabID != "w1:t1" {
		t.Errorf("BlueLake's binding moved to %s", b.TabID)
	}
}
