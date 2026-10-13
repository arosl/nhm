// Package zoom is nhm zoom's herdr part, which the CLI, the action menu
// and the board share: focus the workspace, zoom one pane, and bring the
// pane's tab in front.
package zoom

import (
	"context"

	"github.com/arosl/nhm/internal/herdr"
)

// Herdr is the part of herdr's client that a zoom calls.
type Herdr interface {
	WorkspaceFocus(ctx context.Context, workspaceID string) error
	PaneZoom(ctx context.Context, paneID, mode string) (herdr.ZoomResult, error)
	TabFocus(ctx context.Context, tabID string) (herdr.TabInfo, error)
}

// Unshown is Pane's error when the pane is zoomed but herdr did not bring
// the pane's tab in front, so an attached client may still show another
// tab. The zoom worked: a caller reports it with a warning.
type Unshown struct {
	PaneID string
	Reason string
}

func (e *Unshown) Error() string {
	return "zoomed " + e.PaneID + ", but herdr did not bring its tab in front: " + e.Reason
}

// Pane focuses workspace ws, zooms pane on, and brings the tab of
// pane.zoom's layout in front. herdr 0.9.3's client follows tab.focus and
// workspace.focus, but not the focus that pane.zoom moves to another tab
// (seen in the sandbox, nhm-qbi.5). A failed workspace focus or zoom is
// the error; only a tab that stays behind is an *Unshown.
func Pane(ctx context.Context, h Herdr, ws, pane string) (herdr.ZoomResult, error) {
	if err := h.WorkspaceFocus(ctx, ws); err != nil {
		return herdr.ZoomResult{}, err
	}
	z, err := h.PaneZoom(ctx, pane, "on")
	if err != nil {
		return z, err
	}
	if z.Layout.TabID == "" {
		return z, &Unshown{PaneID: pane, Reason: "herdr named no tab for the pane"}
	}
	if _, err := h.TabFocus(ctx, z.Layout.TabID); err != nil {
		return z, &Unshown{PaneID: pane, Reason: err.Error()}
	}
	return z, nil
}
