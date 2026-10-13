package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/zoom"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

const attachUsage = `Usage: nhm attach [session] [--socket PATH] [--json]

ntm's attach on herdr: focus the session's workspace (a workspace id or
label), so that every herdr client shows it. Open herdr to see it. Without
a session, it runs nhm list (the agents and their sessions), as ntm runs
its list. Changes no agent, so it runs whoever owns this herdr instance.
`

const zoomUsage = `Usage: nhm zoom <session> <pane> [--socket PATH] [--json]

ntm's zoom on herdr: focus the session's workspace, zoom one pane, and
bring the pane's tab in front. The pane is a slot N.M (as nhm status shows
it), a herdr pane id such as w1:p3, an Agent Mail or herdr name, or a kind
(cc, cod, agy, or a kind's name), which zooms that kind's first agent.
ntm's tmux pane indexes do not exist on herdr: use the slot. Changes no
agent, so it runs whoever owns this herdr instance.
`

var (
	slotPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	indexPattern  = regexp.MustCompile(`^[0-9]+$`)
	paneIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*:p[0-9A-Za-z]+$`)
)

// AttachResult is attach's and zoom's result.
type AttachResult struct {
	WorkspaceID string `json:"workspace_id"`
	PaneID      string `json:"pane_id,omitempty"`
	Slot        string `json:"slot,omitempty"`
	MailName    string `json:"mail_name,omitempty"`
	Zoomed      bool   `json:"zoomed,omitempty"`
}

func runAttach(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, attachUsage) }
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	switch len(pos) {
	case 0:
		listArgs := []string{}
		if *socket != "" {
			listArgs = append(listArgs, "--socket", *socket)
		}
		if *asJSON {
			listArgs = append(listArgs, "--json")
		}
		return runList(listArgs, stdout, stderr)
	case 1:
	default:
		fmt.Fprint(stderr, attachUsage)
		return exitRefused
	}
	env := envelope{Command: "attach"}
	ctx := context.Background()
	h, code, err := focusSession(ctx, *socket, pos[0])
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, code, nil)
	}
	res := AttachResult{WorkspaceID: h}
	env.Result = res
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "focused %s (%s); open herdr to see it\n", pos[0], res.WorkspaceID)
	})
}

func runZoom(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("zoom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, zoomUsage) }
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) != 2 {
		fmt.Fprint(stderr, zoomUsage)
		return exitRefused
	}
	env := envelope{Command: "zoom"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	ws, err := sessionWorkspace(ctx, ie.herdr, pos[0])
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	res, err := zoomTarget(ctx, ie.herdr, bindings.NewStore(ie.db), ws, pos[1])
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	z, err := zoom.Pane(ctx, ie.herdr, ws, res.PaneID)
	var unshown *zoom.Unshown
	var alone *zoom.Alone
	switch {
	case errors.As(err, &unshown):
		// The pane is zoomed; only its tab did not come in front.
		env.Warnings = append(env.Warnings, unshown.Error())
	case errors.As(err, &alone):
		// Nothing to zoom; the pane's tab is in front.
	case err != nil:
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	res.Zoomed = z.Zoomed
	env.Result = res
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		who := res.PaneID
		if res.MailName != "" {
			who = fmt.Sprintf("%s (%s, %s)", res.MailName, dash(res.Slot), res.PaneID)
		}
		if alone != nil {
			fmt.Fprintf(w, "%s is alone in its tab in %s: nothing to zoom; open herdr to see it\n", who, pos[0])
			return
		}
		fmt.Fprintf(w, "zoomed %s in %s; open herdr to see it\n", who, pos[0])
	})
}

// focusSession focuses a session's workspace and returns its id, or the
// exit code and error.
func focusSession(ctx context.Context, socket, session string) (string, int, error) {
	ie, err := openInstance(socket)
	if err != nil {
		return "", exitError, err
	}
	defer ie.db.Close()
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return "", exitRefused, fmt.Errorf("%w (nhm list shows the sessions)", err)
	}
	if err := ie.herdr.WorkspaceFocus(ctx, ws); err != nil {
		return "", exitError, err
	}
	return ws, exitOK, nil
}

// zoomTarget resolves zoom's pane argument in workspace ws: a slot, a name
// or a kind through the bindings (the first by slot), or a herdr pane id
// that herdr lists in ws.
func zoomTarget(ctx context.Context, h *herdr.Client, store wait.Lister, ws, pane string) (AttachResult, error) {
	res := AttachResult{WorkspaceID: ws}
	var sel wait.Selector
	switch {
	case indexPattern.MatchString(pane):
		return res, fmt.Errorf("pane %s: herdr has no tmux pane indexes; give the slot N.M (nhm status shows it)", pane)
	case slotPattern.MatchString(pane):
		sel.Slots = []string{pane}
	case paneIDPattern.MatchString(pane):
		snap, err := h.Snapshot(ctx)
		if err != nil {
			return res, err
		}
		for _, p := range snap.Panes {
			if p.PaneID == pane && p.WorkspaceID == ws {
				res.PaneID = pane
				return res, nil
			}
		}
		return res, fmt.Errorf("pane %s is not in workspace %s", pane, ws)
	default:
		if a, ok := kinds.Default().ByFlag(strings.ToLower(pane)); ok {
			sel.Kinds = []string{a.Name}
		} else if a, ok := kinds.Get(strings.ToLower(pane)); ok {
			sel.Kinds = []string{a.Name}
		} else {
			sel.Names = []string{pane}
		}
	}
	targets, err := wait.Select(ctx, store, ws, "", sel)
	if err != nil {
		if errors.Is(err, wait.ErrNoTargets) {
			return res, fmt.Errorf("pane %s: %w", pane, err)
		}
		return res, err
	}
	b := targets[0]
	res.PaneID, res.Slot, res.MailName = b.PaneID, b.Slot, b.MailName
	return res, nil
}
