// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/dashboard.go (newDashboardCmd, shouldUsePlainMode,
// isTUIDebugEnabled, runDashboardJSON, runDashboardPlain,
// dashboardPaneTypeSummary); see LICENSE in this directory and
// PROVENANCE.md. Local changes: a session is a herdr workspace and its
// panes are the workspace's (core/session.Panes), a pane without an agent
// being ntm's user pane; a pane's id is herdr's, its index the slot's M,
// its variant the bound agent's model, its tags the end of its herdr
// label, its command its foreground process (herdr's pane.process_info)
// and its size its rect in herdr's layout; the TUI is nhm's board, which
// runs where it is started and closes on Esc, as ntm's --popup does, and
// has no debug logs or attention cursors yet; nhm's env name
// NHM_TUI_DEBUG for ntm's NTM_TUI_DEBUG; nhm board answers to the same
// verb; --socket beside ntm's flags; the output goes to cobra's writer.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/panestate"
	"github.com/arosl/nhm/internal/core/session"
)

const dashboardUsage = `Usage: nhm dashboard [session] [--no-tui] [--json] [--popup] [--socket PATH]
       (also dash, d, board)

ntm's dashboard on herdr. The interactive dashboard is nhm's board (keys:
? for help; Esc or q closes it), for the session's workspace, else the
calling pane's. --no-tui prints ntm's plain form: Session:, Panes: and
Pane Types:, then one line per pane, "[type] title (model) [tags] -
active|idle (WxH)". --json implies --no-tui and prints ntm's
DashboardOutput (session, pane_count, agent_counts, panes). CI, TERM=dumb
or NO_COLOR picks the plain form by itself, as ntm does. --popup changes
nothing: the board always runs where it is started and closes on Esc.
--debug (and NHM_TUI_DEBUG=1) and --attention-cursor wait for their
beads (nhm-2p6.3.13, nhm-2p6.22).
`

// dashboardPane is ntm's PaneInfo of runDashboardJSON.
type dashboardPane struct {
	ID      string   `json:"id"`
	Index   int      `json:"index"`
	Type    string   `json:"type"`
	Variant string   `json:"variant,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Command string   `json:"command,omitempty"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
	Active  bool     `json:"active"`
	// title is the plain form's pane title; ntm's JSON has none.
	title string
}

// dashboardOutput is ntm's DashboardOutput of runDashboardJSON.
type dashboardOutput struct {
	Session    string          `json:"session"`
	PaneCount  int             `json:"pane_count"`
	AgentCount map[string]int  `json:"agent_counts"`
	Panes      []dashboardPane `json:"panes"`
}

// dashboardGoBoard runs nhm's board for a workspace id, or for the
// calling pane's workspace with ""; tests replace it.
var dashboardGoBoard = runGoBoard

func newDashboardCmd() *cobra.Command {
	var noTUI, debug, popup, goFlag bool
	var attentionCursor int64
	var socket string
	cmd := &cobra.Command{
		Use:     "dashboard [session]",
		Aliases: []string{"dash", "d", "board"},
		Short:   "Open interactive session dashboard",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			machineJSON := wantJSON(cmd)
			var name string
			if len(args) > 0 {
				name = args[0]
			}
			if machineJSON {
				noTUI = true
			}
			if !noTUI && shouldUsePlainMode() {
				noTUI = true
			}
			if !debug && isTUIDebugEnabled() {
				debug = true
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			if machineJSON {
				return runDashboardJSON(name, socket, out, errOut)
			}
			if noTUI {
				return runDashboardPlain(name, socket, out, errOut)
			}
			if debug {
				return notBuiltError{flag: "debug", bead: "nhm-2p6.3.13"}
			}
			if attentionCursor != 0 {
				return notBuiltError{flag: "attention-cursor", bead: "nhm-2p6.22"}
			}
			return runDashboardBoard(name, socket, out, errOut)
		},
	}
	fs := cmd.Flags()
	fs.BoolVar(&noTUI, "no-tui", false, "Plain text output (no interactive UI)")
	fs.BoolVar(&debug, "debug", false, "Enable debug mode with state inspection")
	fs.BoolVar(&popup, "popup", false, "Run in popup/overlay mode (Esc closes, zoom focuses pane)")
	fs.Int64Var(&attentionCursor, "attention-cursor", 0, "Pre-focus the attention panel on this event cursor")
	fs.StringVar(&socket, "socket", "", "herdr API socket")
	// nhm board's --go, which old callers pass; it changes nothing.
	fs.BoolVar(&goFlag, "go", false, "")
	_ = fs.MarkHidden("go")
	return cmd
}

// shouldUsePlainMode is ntm's: CI, a dumb terminal or NO_COLOR picks the
// plain form.
func shouldUsePlainMode() bool {
	return os.Getenv("CI") != "" || os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") != ""
}

// isTUIDebugEnabled is ntm's, with nhm's env name.
func isTUIDebugEnabled() bool {
	return os.Getenv("NHM_TUI_DEBUG") == "1"
}

// runDashboardBoard runs nhm's board in ntm's TUI place. A named session
// resolves as ntm's does; without one, the board takes the calling pane's
// workspace. A board that fails exits 1, with the board's own line.
func runDashboardBoard(name, socket string, stdout, stderr io.Writer) error {
	ws := ""
	if name != "" {
		ie, err := openInstance(socket)
		if err != nil {
			return err
		}
		r, err := resolveSession(context.Background(), ie, name, false, stdout, stderr)
		_ = ie.db.Close()
		if err != nil {
			return err
		}
		if r.Session == "" {
			return nil
		}
		ws = r.WorkspaceID
	}
	if code := dashboardGoBoard(ws, stderr); code != exitOK {
		return verbExit{code: exitError}
	}
	return nil
}

// dashboardPanes resolves the session as ntm's dashboard does and reads its
// panes; "" is a cancelled picker.
func dashboardPanes(name, socket string, asJSON bool, stdout, stderr io.Writer) (string, []dashboardPane, error) {
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return "", nil, err
	}
	defer ie.db.Close()
	r, err := resolveSession(ctx, ie, name, asJSON, stdout, stderr)
	if err != nil || r.Session == "" {
		return "", nil, err
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get panes: %w", err)
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: r.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return "", nil, fmt.Errorf("failed to get panes: %w", err)
	}
	sizes := map[string][2]int{}
	for _, l := range snap.Layouts {
		for _, p := range l.Panes {
			sizes[p.PaneID] = [2]int{int(p.Rect.Width), int(p.Rect.Height)}
		}
	}
	labels := map[string]string{}
	for _, p := range snap.Panes {
		labels[p.PaneID] = p.Label
	}
	var out []dashboardPane
	for _, p := range session.Panes(snap, r.WorkspaceID, bound) {
		d := dashboardPane{ID: p.PaneID, Index: p.Index, Type: p.Type, Tags: session.PaneTags(labels[p.PaneID]),
			Width: sizes[p.PaneID][0], Height: sizes[p.PaneID][1], Active: p.Active, title: p.Title}
		if p.Binding != nil {
			d.Variant = p.Binding.Model
		}
		if info, err := ie.herdr.PaneProcessInfo(ctx, p.PaneID); err == nil && len(info.ForegroundProcesses) > 0 {
			d.Command = info.ForegroundProcesses[0].Name
		}
		out = append(out, d)
	}
	return r.Session, out, nil
}

// runDashboardJSON is ntm's: DashboardOutput, or {} for a cancelled
// picker.
func runDashboardJSON(name, socket string, stdout, stderr io.Writer) error {
	sessionName, panes, err := dashboardPanes(name, socket, true, stdout, stderr)
	if err != nil {
		return err
	}
	if sessionName == "" {
		fmt.Fprintln(stdout, "{}")
		return nil
	}
	counts := map[string]int{}
	infos := make([]dashboardPane, 0, len(panes))
	for _, p := range panes {
		counts[p.Type]++
		infos = append(infos, p)
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(dashboardOutput{Session: sessionName, PaneCount: len(panes), AgentCount: counts, Panes: infos})
}

// runDashboardPlain is ntm's plain form.
func runDashboardPlain(name, socket string, stdout, stderr io.Writer) error {
	sessionName, panes, err := dashboardPanes(name, socket, false, stdout, stderr)
	if err != nil || sessionName == "" {
		return err
	}
	fmt.Fprintf(stdout, "Session: %s\n", sessionName)
	fmt.Fprintf(stdout, "Panes: %d\n", len(panes))
	fmt.Fprintf(stdout, "Pane Types: %s\n", dashboardPaneTypeSummary(panes))
	fmt.Fprintln(stdout, strings.Repeat("-", 60))
	for _, p := range panes {
		status := "idle"
		if p.Active {
			status = "active"
		}
		tags := ""
		if len(p.Tags) > 0 {
			tags = " [" + strings.Join(p.Tags, ",") + "]"
		}
		variant := ""
		if p.Variant != "" {
			variant = " (" + p.Variant + ")"
		}
		fmt.Fprintf(stdout, "[%s] %s%s%s - %s (%dx%d)\n", p.Type, p.title, variant, tags, status, p.Width, p.Height)
	}
	return nil
}

// dashboardPaneTypeSummary is ntm's: the count of each type it names, the
// rest as Other.
func dashboardPaneTypeSummary(panes []dashboardPane) string {
	counts := map[string]int{}
	for _, p := range panes {
		switch t := panestate.ResolveAgentType(p.Type); t {
		case "claude", "codex", "gemini", "grok", "cursor", "windsurf", "aider", "oc", "omp", "ollama", "user":
			counts[t]++
		default:
			counts["other"]++
		}
	}
	return fmt.Sprintf("Claude=%d Codex=%d Gemini=%d Grok=%d Cursor=%d Windsurf=%d Aider=%d Opencode=%d Omp=%d Ollama=%d User=%d Other=%d",
		counts["claude"], counts["codex"], counts["gemini"], counts["grok"], counts["cursor"], counts["windsurf"],
		counts["aider"], counts["oc"], counts["omp"], counts["ollama"], counts["user"], counts["other"])
}
