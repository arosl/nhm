package tabs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// noSwapWorld is the fake herdr session whose pane.swap changes nothing.
type noSwapWorld struct{ *world }

func (w noSwapWorld) PaneSwap(_ context.Context, s herdr.PaneSwapParams) (herdr.PaneSwapResult, error) {
	return herdr.PaneSwapResult{SourcePaneID: s.SourcePaneID, TargetPaneID: s.TargetPaneID, Reason: "same_pane"}, nil
}

// SwapLocked exchanges two agents' places in their tab (nhm arrange's
// sort in place): the result names the first agent's new cell, and
// neither binding changes. It refuses agents of two tabs, which herdr
// would not swap, and a binding that changed while its caller waited,
// and swaps nothing then. herdr's failure, or a swap it does not make,
// is an error.
func TestSwapLocked(t *testing.T) {
	h := newHarness(t)
	h.tab("w1:t1", "proj")
	h.tab("w1:t2", "RedFox")
	icy := h.agent("IcyKnoll", "w1:p1", "w1:t1", "", rect(0, 0, 40, 40))
	h.agent("BlueLake", "w1:p2", "w1:t1", "", rect(40, 0, 40, 40))
	pike := h.agent("GreenPike", "w1:p4", "w1:t1", "", rect(80, 0, 40, 40))
	red := h.agent("RedFox", "w1:p3", "w1:t2", "", full)
	ctx := context.Background()

	if _, err := SwapLocked(ctx, h.env, icy, red, 6); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "not in one tab") {
		t.Errorf("two tabs: %v", err)
	}
	stale := pike
	stale.Generation++
	if _, err := SwapLocked(ctx, h.env, icy, stale, 6); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "binding changed") {
		t.Errorf("a changed binding: %v", err)
	}
	if len(h.world.swaps) != 0 {
		t.Fatalf("a refusal swapped: %+v", h.world.swaps)
	}

	r, err := SwapLocked(ctx, h.env, icy, pike, 6)
	if err != nil {
		t.Fatal(err)
	}
	if s := h.world.swaps; len(s) != 1 || s[0] != (herdr.PaneSwapParams{SourcePaneID: "w1:p1", TargetPaneID: "w1:p4"}) {
		t.Fatalf("pane.swap = %+v", s)
	}
	if got := h.world.find("w1:p1").rect; got != rect(80, 0, 40, 40) {
		t.Errorf("IcyKnoll's pane is at %+v", got)
	}
	want := []string{"IcyKnoll and GreenPike swapped places in tab w1:t1: IcyKnoll is in cell 3"}
	if r.Cell.Cell != 3 || r.Cell.TabID != "w1:t1" || r.Cell.FromTab != "w1:t1" || r.Binding.ID != icy.ID || !slices.Equal(r.Actions, want) {
		t.Errorf("result %+v", r)
	}
	for _, b := range []struct{ id, tab, pane, slot string }{{icy.ID, "w1:t1", "w1:p1", icy.Slot}, {pike.ID, "w1:t1", "w1:p4", pike.Slot}} {
		if nb := h.binding(b.id); nb.TabID != b.tab || nb.PaneID != b.pane || nb.Slot != b.slot || nb.Generation != 1 {
			t.Errorf("binding %+v changed", nb)
		}
	}

	h.world.moveErr = errors.New("herdr: pane_swap_failed")
	if _, err := SwapLocked(ctx, h.env, icy, pike, 6); err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "herdr did not swap w1:p1 and w1:p4") {
		t.Errorf("herdr's failure: %v", err)
	}
	h.world.moveErr = nil
	h.env.Herdr = noSwapWorld{h.world}
	if _, err := SwapLocked(ctx, h.env, icy, pike, 6); err == nil || !strings.Contains(err.Error(), "nothing changed (same_pane)") {
		t.Errorf("a swap herdr does not make: %v", err)
	}
}
