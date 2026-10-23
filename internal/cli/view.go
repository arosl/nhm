package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/arosl/nhm/internal/herdr"
)

const viewUsage = `Usage: nhm view [session] [--socket PATH] [--json]   (also nhm tile, nhm v)

ntm's view on herdr: take the zoom off in every tab of the session's
workspace (a workspace id or label; without one, the calling pane's),
so that each tab shows all its panes, then focus the workspace, as nhm
attach does. Open herdr to see it. herdr lays out the panes itself:
nhm's spawn grid is not applied again. Changes no agent, so it runs
whoever owns this herdr instance.
`

// ViewResult is view's result: the workspace, its tabs, and the tabs
// whose zoom view took off.
type ViewResult struct {
	WorkspaceID string   `json:"workspace_id"`
	Tabs        []string `json:"tabs"`
	Unzoomed    []string `json:"unzoomed"`
}

// viewHerdr is the part of herdr's client that view calls.
type viewHerdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	PaneZoom(ctx context.Context, paneID, mode string) (herdr.ZoomResult, error)
	WorkspaceFocus(ctx context.Context, workspaceID string) error
}

func runView(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, viewUsage) }
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 1 {
		fmt.Fprint(stderr, viewUsage)
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	env := envelope{Command: "view"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, fmt.Errorf("%w (nhm list shows the sessions)", err), exitRefused, nil)
	}
	res, err := viewWorkspace(ctx, ie.herdr, ws)
	env.Result = res
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	name := firstOf(session, ws)
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "✓ Zoom off in every tab of '%s' (tabs: %d, zoomed before: %d); focused it; open herdr to see it\n",
			name, len(res.Tabs), len(res.Unzoomed))
	})
}

// viewWorkspace takes the zoom off in each tab of workspace ws, through
// the tab's first pane (herdr zooms a tab by one of its panes), then
// focuses ws.
func viewWorkspace(ctx context.Context, h viewHerdr, ws string) (ViewResult, error) {
	res := ViewResult{WorkspaceID: ws, Tabs: []string{}, Unzoomed: []string{}}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return res, err
	}
	first := map[string]string{}
	for _, p := range snap.Panes {
		if p.WorkspaceID != ws || first[p.TabID] != "" {
			continue
		}
		first[p.TabID] = p.PaneID
		res.Tabs = append(res.Tabs, p.TabID)
	}
	for _, tab := range res.Tabs {
		z, err := h.PaneZoom(ctx, first[tab], "off")
		if err != nil {
			return res, fmt.Errorf("tab %s: %w", tab, err)
		}
		if z.ZoomChanged {
			res.Unzoomed = append(res.Unzoomed, tab)
		}
	}
	if err := h.WorkspaceFocus(ctx, ws); err != nil {
		return res, err
	}
	return res, nil
}
