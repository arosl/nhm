package spawn

import "github.com/arosl/nhm/internal/herdr"

// GridOrder lists a tab's panes in the reading order of their own cells
// on the grid of perTab, as GridSplit counts them: a pane's own cell is
// the first, in reading order, whose centre it covers. clean reports
// whether GridSplit fills the tab in order from here: the tab lies on the
// grid (OnGrid), every pane has a cell of its own, and no free cell comes
// before an owned one. A clean tab of k panes takes its next pane in cell
// k+1, so its panes stay in the order in which they joined (nhm-yr6). A
// tab with a gap, such as one whose pane in the first row closed, takes
// its next pane in the gap. order holds the panes that have a cell of
// their own, clean or not.
func GridOrder(panes []herdr.LayoutPane, perTab int) (order []string, clean bool) {
	if len(panes) == 0 {
		return nil, false
	}
	clean = OnGrid(panes, perTab)
	x0, y0, cw, ch, cols, rows := cells(panes, perTab)
	owned, free := map[string]bool{}, false
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			left, top := x0+float64(c)*cw, y0+float64(r)*ch
			p, found := paneAt(panes, left+cw/2, top+ch/2)
			switch {
			case !found:
				clean = false
			case !owned[p.PaneID]:
				owned[p.PaneID] = true
				order = append(order, p.PaneID)
				if free {
					clean = false
				}
			default:
				free = true
			}
		}
	}
	if len(order) != len(panes) {
		clean = false
	}
	return order, clean
}
