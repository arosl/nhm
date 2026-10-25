package cli

import (
	"context"
	"fmt"

	"github.com/arosl/nhm/internal/herdr"
)

// viewHerdr is the part of herdr's client that view's unzoom calls.
type viewHerdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	PaneZoom(ctx context.Context, paneID, mode string) (herdr.ZoomResult, error)
}

// unzoomWorkspace is ntm's tiling on herdr: it takes the zoom off in each
// zoomed tab of workspace ws, as the snapshot's layouts show them. herdr
// lays out the panes itself, so no layout is applied (the operator's
// ruling, nhm-k9d, 2026-10-23). herdr zooms a tab by one of its panes:
// the zoom goes off through the tab's focused pane, so the tab keeps its
// focus, as ntm's tiling keeps the active pane (ScarletOrchid's review,
// nhm-k9d). A tab that is not zoomed is left as it is.
func unzoomWorkspace(ctx context.Context, h viewHerdr, ws string) error {
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, l := range snap.Layouts {
		if l.WorkspaceID != ws || !l.Zoomed {
			continue
		}
		if _, err := h.PaneZoom(ctx, l.FocusedPaneID, "off"); err != nil {
			return fmt.Errorf("tab %s: %w", l.TabID, err)
		}
	}
	return nil
}
