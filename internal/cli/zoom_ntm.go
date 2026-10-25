// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/zoom.go (newZoomCmd, runZoom, buildZoomResponse,
// resolvePaneSelector, zoomPanePresentation, runPaneSelector),
// internal/cli/session.go (sessionPanePresentation) and
// internal/output/schema.go (SuccessResponse); see LICENSE in this
// directory and PROVENANCE.md. Local changes: a session is a herdr
// workspace and its panes are the workspace's (core/session.Panes); a pane
// is ntm's index (the slot's M in the session's agent tab), a slot N.M, a
// herdr pane id, an agent type, or nhm's Agent Mail or herdr name; ntm's
// zoom is herdr's pane.zoom, and its attach is the workspace's focus with
// the pane's tab in front (core/zoom); a pane alone in its tab zooms as a
// lone tmux pane does, with nothing to change; a tab that stays behind is
// a warning on stderr; the picker's command is the pane's foreground
// process (herdr's pane.process_info); --socket beside ntm's flags; the
// output goes to cobra's writers.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/zoom"
	"github.com/arosl/nhm/internal/ui/tui/agent"
	"github.com/arosl/nhm/internal/ui/tui/icons"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

const zoomUsage = `Usage: nhm zoom [session-name] [pane-index] [--socket PATH] [--json]
       (also z)

ntm's zoom on herdr: zoom one pane of the session's workspace, then
focus the workspace and bring the pane's tab in front, so that every
herdr client shows it. Open herdr to see it.

Without a session: the calling pane's workspace, the cwd's, the only
one, or ntm's session picker. The pane is ntm's index (the slot's M in
the session's agent tab, as nhm status shows it), a slot N.M, a herdr
pane id, an agent type (cc, cod, …: its first pane) or an agent's name.
Without a pane, a numbered list asks on a terminal ("q" cancels); --json
needs the pane. Prints "✓ Zoomed pane N in 'S'"; --json gives ntm's
SuccessResponse and focuses nothing. Changes no agent, so it runs
whoever owns this herdr instance.
`

// successResponse is ntm's output.SuccessResponse.
type successResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// zoomInteractive is ntm's IsInteractive check of zoom's writer, and
// zoomStdin the picker's input; tests replace them.
var (
	zoomInteractive = func(w io.Writer) bool {
		f, ok := w.(*os.File)
		return ok && term.IsTerminal(int(f.Fd()))
	}
	zoomStdin io.Reader = os.Stdin
)

func newZoomCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:     "zoom [session-name] [pane-index]",
		Aliases: []string{"z"},
		Short:   "Zoom a specific pane in a session",
		Args:    cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name, pane string
			if len(args) >= 1 {
				name = args[0]
			}
			if len(args) >= 2 {
				pane = args[1]
			}
			return runZoomNTM(name, pane, socket, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "herdr API socket")
	return cmd
}

// runZoomNTM is ntm's runZoom. As in ntm, an error before the session is
// known is the root's (the robot error envelope under --json), and one
// after it ntm's JSON failure envelope.
func runZoomNTM(name, pane, socket string, asJSON bool, stdout, stderr io.Writer) error {
	fail := func(err error) error {
		if asJSON {
			return jsonFailure(stdout, err)
		}
		return err
	}
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	r, err := resolveSession(ctx, ie, name, asJSON, stdout, stderr)
	if err != nil {
		return err
	}
	if r.Session == "" {
		return nil
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return fail(err)
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: r.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return fail(err)
	}
	panes := session.Panes(snap, r.WorkspaceID, bound)
	var target session.Pane
	switch {
	case strings.TrimSpace(pane) != "":
		activeTab := ""
		for _, w := range snap.Workspaces {
			if w.WorkspaceID == r.WorkspaceID {
				activeTab = w.ActiveTabID
			}
		}
		target, err = zoomPaneOf(panes, pane, r.Session, session.AgentTabs(bound), activeTab)
		if err != nil {
			return fail(err)
		}
	case asJSON:
		return fail(errors.New("pane index is required for JSON output"))
	case !zoomInteractive(stdout):
		return errors.New("non-interactive environment: pane index is required for zoom")
	case len(panes) == 0:
		return fmt.Errorf("no panes found in session '%s'", r.Session)
	default:
		var ok bool
		target, ok, err = zoomPicker(ctx, ie, panes, stdout)
		if err != nil || !ok {
			return err
		}
	}
	// A lone pane is an *Alone: nothing to zoom, as with a lone tmux pane.
	var alone *zoom.Alone
	z, err := zoom.Only(ctx, ie.herdr, target.PaneID)
	if err != nil && !errors.As(err, &alone) {
		return fail(fmt.Errorf("failed to zoom pane: %w", err))
	}
	msg := fmt.Sprintf("pane %s in '%s'", target.Ref(), r.Session)
	if asJSON {
		return writeNTMJSON(stdout, successResponse{Success: true, Message: "zoomed " + msg})
	}
	t := theme.Current()
	fmt.Fprintf(stdout, "%s✓%s Zoomed %s\n", colorize(t.Success), colorize(t.Text), msg)
	// ntm attaches after the zoom; a failed attach fails the call.
	var unshown *zoom.Unshown
	if err := zoom.Show(ctx, ie.herdr, r.WorkspaceID, target.PaneID, z); errors.As(err, &unshown) {
		fmt.Fprintf(stderr, "warning: %v\n", unshown)
	} else if err != nil {
		return err
	}
	return nil
}

// zoomPaneOf is ntm's pane argument of zoom on herdr: an index or a slot
// (ParsePane), a herdr pane id of the session, else ntm's agent type
// selector (resolvePaneSelector: the type's first pane), else an agent's
// Agent Mail or herdr name.
func zoomPaneOf(panes []session.Pane, text, sessionName string, tabs []session.AgentTab, activeTab string) (session.Pane, error) {
	t, err := session.ParsePane(text, tabs, activeTab)
	if err != nil {
		return session.Pane{}, err
	}
	for _, p := range panes {
		switch {
		case t.Form == session.PaneSlot && p.Slot == t.Slot,
			t.Form == session.PaneHerdrID && p.PaneID == t.PaneID:
			return p, nil
		}
	}
	if t.Form != session.PaneWord {
		return session.Pane{}, fmt.Errorf("pane %s not found in session '%s'", text, sessionName)
	}
	// ntm's ResolveAgentType lowercases a type it does not know (pi).
	want := string(agent.AgentType(t.Word).Canonical())
	for _, p := range panes {
		if strings.EqualFold(string(agent.AgentType(p.Type).Canonical()), want) {
			return p, nil
		}
	}
	for _, p := range panes {
		if b := p.Binding; b != nil && (strings.EqualFold(b.MailName, t.Word) || strings.EqualFold(b.HerdrAlias, t.Word)) {
			return p, nil
		}
	}
	return session.Pane{}, fmt.Errorf("no panes match selector %q", strings.TrimSpace(text))
}

// zoomPicker is ntm's runPaneSelector: a numbered list of the session's
// panes (the first nine shown), and a number read from stdin; q or
// nothing cancels.
func zoomPicker(ctx context.Context, ie *instanceEnv, panes []session.Pane, stdout io.Writer) (session.Pane, bool, error) {
	t := theme.Current()
	ic := icons.Current()
	fmt.Fprintf(stdout, "\n%sSelect pane to zoom:%s\n\n", "\033[1m", "\033[0m")
	for i, p := range panes {
		if num := i + 1; num <= 9 {
			colorKey, icon := zoomPanePresentation(p.Type, t, ic)
			command := ""
			if info, err := ie.herdr.PaneProcessInfo(ctx, p.PaneID); err == nil && len(info.ForegroundProcesses) > 0 {
				command = info.ForegroundProcesses[0].Name
			}
			fmt.Fprintf(stdout, "  %s%d%s %s%s %s%s (%s)\n", "\033[38;5;245m", num, "\033[0m",
				"\033[38;2;"+colorToRGB(colorKey), icon, "\033[0m", p.Title, command)
		}
	}
	fmt.Fprint(stdout, "\nEnter number (or q to cancel): ")
	line, _ := bufio.NewReader(zoomStdin).ReadString('\n')
	input := strings.TrimSpace(line)
	if fields := strings.Fields(input); len(fields) > 0 {
		input = fields[0]
	}
	if input == "q" || input == "" {
		return session.Pane{}, false, nil
	}
	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > len(panes) {
		return session.Pane{}, false, errors.New("invalid selection")
	}
	return panes[idx-1], true, nil
}

// zoomPanePresentation is ntm's sessionPanePresentation: the theme colour
// and the icon of a pane's agent type.
func zoomPanePresentation(paneType string, th theme.Theme, ic icons.IconSet) (string, string) {
	switch agent.AgentType(paneType).Canonical() {
	case agent.AgentTypeClaudeCode:
		return string(th.Claude), ic.AgentIcon(string(agent.AgentTypeClaudeCode))
	case agent.AgentTypeCodex:
		return string(th.Codex), ic.AgentIcon(string(agent.AgentTypeCodex))
	case agent.AgentTypeGemini:
		return string(th.Gemini), ic.AgentIcon(string(agent.AgentTypeGemini))
	case agent.AgentTypeAntigravity:
		return string(th.Lavender), ic.AgentIcon(string(agent.AgentTypeAntigravity))
	case agent.AgentTypeGrok:
		return string(th.Pink), ic.AgentIcon(string(agent.AgentTypeGrok))
	case agent.AgentTypeOmp:
		return string(th.Sky), ic.AgentIcon(string(agent.AgentTypeOmp))
	case agent.AgentTypeOpencode:
		return string(th.Opencode), ic.AgentIcon(string(agent.AgentTypeOpencode))
	case agent.AgentTypeCursor:
		return string(th.Cursor), ic.AgentIcon(string(agent.AgentTypeCursor))
	case agent.AgentTypeWindsurf:
		return string(th.Windsurf), ic.AgentIcon(string(agent.AgentTypeWindsurf))
	case agent.AgentTypeAider:
		return string(th.Aider), ic.AgentIcon(string(agent.AgentTypeAider))
	case agent.AgentTypeOllama:
		return string(th.Ollama), ic.AgentIcon(string(agent.AgentTypeOllama))
	case agent.AgentTypeUser:
		return string(th.User), ic.AgentIcon(string(agent.AgentTypeUser))
	default:
		return string(th.Overlay), ic.AgentIcon(string(agent.AgentTypeUnknown))
	}
}
