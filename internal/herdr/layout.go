package herdr

import "context"

// PaneLayout is the part of pane.layout's answer that nhm reads: the tab
// of the pane asked about, and where each of the tab's panes is.
type PaneLayout struct {
	WorkspaceID string       `json:"workspace_id"`
	TabID       string       `json:"tab_id"`
	Zoomed      bool         `json:"zoomed"`
	Panes       []LayoutPane `json:"panes"`
}

// LayoutPane is one pane of a PaneLayout.
type LayoutPane struct {
	PaneID string     `json:"pane_id"`
	Rect   LayoutRect `json:"rect"`
}

// LayoutRect is a pane's place in its tab, in terminal cells.
type LayoutRect struct {
	X      uint16 `json:"x"`
	Y      uint16 `json:"y"`
	Width  uint16 `json:"width"`
	Height uint16 `json:"height"`
}

type layoutResult struct {
	Layout PaneLayout `json:"layout"`
}

// PaneLayout reads the layout of the tab that holds paneID.
func (c *Client) PaneLayout(ctx context.Context, paneID string) (PaneLayout, error) {
	var r layoutResult
	err := c.call(ctx, "pane.layout", paneParams{PaneID: paneID}, &r, 0)
	return r.Layout, err
}
