package spawn

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// A tab that GridSplit grows pane by pane is clean at every size, and
// lists its panes in the order in which they joined: the order nhm
// arrange keeps (nhm-yr6).
func TestGridOrderOfAGrownTab(t *testing.T) {
	for perTab := 1; perTab <= 9; perTab++ {
		panes := []herdr.LayoutPane{{PaneID: "p0", Rect: herdr.LayoutRect{Width: 240, Height: 60}}}
		for k := 1; ; k++ {
			order, clean := GridOrder(panes, perTab)
			want := make([]string, k)
			for i := range want {
				want[i] = fmt.Sprintf("p%d", i)
			}
			if !clean || !slices.Equal(order, want) {
				t.Fatalf("per-tab %d, %d panes: order %v clean %v, want %v clean", perTab, k, order, clean, want)
			}
			cell, pane, dir, ratio, ok := GridSplit(panes, perTab)
			if !ok {
				if c, r := grid(perTab); k != c*r {
					t.Fatalf("per-tab %d: no free cell at %d panes", perTab, k)
				}
				break
			}
			if cell != k+1 {
				t.Fatalf("per-tab %d, %d panes: the next pane goes into cell %d, want %d", perTab, k, cell, k+1)
			}
			panes = splitPane(panes, pane, dir, ratio, fmt.Sprintf("p%d", k))
		}
	}
}

func TestGridOrder(t *testing.T) {
	pane := func(id string, x, y, w, h uint16) herdr.LayoutPane {
		return herdr.LayoutPane{PaneID: id, Rect: herdr.LayoutRect{X: x, Y: y, Width: w, Height: h}}
	}
	for _, c := range []struct {
		name   string
		panes  []herdr.LayoutPane
		perTab int
		order  []string
		clean  bool
	}{
		{"no panes", nil, 6, nil, false},
		{"one pane", []herdr.LayoutPane{pane("a", 0, 0, 240, 60)}, 6, []string{"a"}, true},
		{"a full tab", []herdr.LayoutPane{pane("a", 0, 0, 80, 30), pane("b", 80, 0, 80, 30), pane("c", 160, 0, 80, 30),
			pane("d", 0, 30, 80, 30), pane("e", 80, 30, 80, 30), pane("f", 160, 30, 80, 30)}, 6,
			[]string{"a", "b", "c", "d", "e", "f"}, true},
		// The panes' list order is herdr's, not the reading order.
		{"listed out of reading order", []herdr.LayoutPane{pane("d", 0, 30, 80, 30), pane("a", 0, 0, 80, 30), pane("b", 80, 0, 80, 60),
			pane("c", 160, 0, 80, 60)}, 6, []string{"a", "b", "c", "d"}, true},
		// b, the middle of the top row, closed, and e below it took its
		// room: e's own cell is now b's, and the cell below is free, with
		// f's cell after it.
		{"a closed pane's gap", []herdr.LayoutPane{pane("a", 0, 0, 80, 30), pane("d", 0, 30, 80, 30), pane("e", 80, 0, 80, 60),
			pane("c", 160, 0, 80, 30), pane("f", 160, 30, 80, 30)}, 6, []string{"a", "e", "c", "d", "f"}, false},
		{"off the grid", []herdr.LayoutPane{pane("a", 0, 0, 120, 60), pane("b", 120, 0, 120, 60)}, 6, []string{"a", "b"}, false},
		// --per-tab 4 over a tab of 6: two panes have no cell of their own.
		{"more panes than cells", []herdr.LayoutPane{pane("a", 0, 0, 80, 30), pane("b", 80, 0, 80, 30), pane("c", 160, 0, 80, 30),
			pane("d", 0, 30, 80, 30), pane("e", 80, 30, 80, 30), pane("f", 160, 30, 80, 30)}, 4, []string{"a", "c", "d", "f"}, false},
	} {
		order, clean := GridOrder(c.panes, c.perTab)
		if !slices.Equal(order, c.order) || clean != c.clean {
			t.Errorf("%s: order %v clean %v, want %v clean %v", c.name, order, clean, c.order, c.clean)
		}
	}
}

// splitPane cuts pane id as herdr's pane.split does: id keeps ratio of
// its width (right) or height (down), and the new pane takes the rest.
func splitPane(panes []herdr.LayoutPane, id, dir string, ratio float64, newID string) []herdr.LayoutPane {
	out := slices.Clone(panes)
	for i, p := range out {
		if p.PaneID != id {
			continue
		}
		r := p.Rect
		n := herdr.LayoutPane{PaneID: newID, Rect: r}
		if dir == "right" {
			keep := uint16(math.Round(float64(r.Width) * ratio))
			out[i].Rect.Width = keep
			n.Rect.X, n.Rect.Width = r.X+keep, r.Width-keep
		} else {
			keep := uint16(math.Round(float64(r.Height) * ratio))
			out[i].Rect.Height = keep
			n.Rect.Y, n.Rect.Height = r.Y+keep, r.Height-keep
		}
		return append(out, n)
	}
	return out
}
