// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/send.go (newKillCmd's use, flags and argument checks,
// runKill's flow and text, runKillProject, executeKill's confirm texts and
// messages), internal/cli/confirm_huh.go (confirmHuhDestructive,
// confirmSimple, isTTY) and internal/output/schema.go (KillResponse);
// see LICENSE in this directory and PROVENANCE.md. Local changes: a
// session is a herdr workspace and its kill is core/lifecycle's
// KillSession; tags end the herdr pane label; --pane and --summarize wait
// for their engines (nhm-2p6.11, nhm-2p6.8) and refuse; nhm runs no
// command hooks, so --no-hooks changes nothing; no audit, webhook or
// timeline records; the confirm writes to the verb's stdout; --socket
// beside ntm's flags.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/sessionlabel"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// KillResponse is ntm's output.KillResponse, the JSON of
// `nhm kill --json`.
type KillResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	Session     string    `json:"session"`
	Killed      bool      `json:"killed"`
	Message     string    `json:"message"`
	Summary     any       `json:"summary,omitempty"`
}

// killFlags are ntm's kill flags and nhm's --socket.
type killFlags struct {
	force, noHooks, summarize bool
	tags                      []string
	project, pane, socket     string
}

func newKillCmd() *cobra.Command {
	var f killFlags
	cmd := &cobra.Command{
		Use:   "kill <session>",
		Short: "Kill a session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, out := wantJSON(cmd), cmd.OutOrStdout()
			if f.project != "" && len(args) > 0 {
				return errors.New("cannot use --project with a specific session name")
			}
			if f.pane != "" {
				if f.project != "" || len(f.tags) > 0 || f.summarize {
					return errors.New("--pane cannot be combined with --project, --tag, or --summarize")
				}
				if len(args) == 0 {
					return errors.New("session name required with --pane")
				}
				return notBuiltError{flag: "pane", bead: "nhm-2p6.11"}
			}
			if f.summarize {
				return notBuiltError{flag: "summarize", bead: "nhm-2p6.8"}
			}
			if f.project != "" {
				return runKillProject(f, asJSON, out, cmd.ErrOrStderr())
			}
			if len(args) == 0 {
				return errors.New("session name or --project required")
			}
			return runKillNTM(args[0], f, asJSON, out, cmd.ErrOrStderr())
		},
	}
	fs := cmd.Flags()
	fs.BoolVarP(&f.force, "force", "f", false, "skip confirmation")
	fs.StringSliceVar(&f.tags, "tag", nil, "filter panes to kill by tag (if used, only matching panes are killed)")
	// ntm's command hooks ([hooks] pre-kill, post-kill): nhm runs none.
	fs.BoolVar(&f.noHooks, "no-hooks", false, "Disable command hooks")
	fs.BoolVar(&f.summarize, "summarize", false, "Generate session summary before killing")
	fs.StringVarP(&f.project, "project", "p", "", "kill all sessions for a base project name")
	fs.StringVar(&f.pane, "pane", "", "remove only the selected pane(s) (N, W.P, or %N; comma-separated); the session and sibling panes survive")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	return cmd
}

// killReaper ends the processes that outlive a killed workspace. Tests
// replace it: their herdr reports host pids.
var killReaper = func() lifecycle.Reaper { return lifecycle.HostReaper() }

// confirmKill is ntm's confirmHuhDestructive: huh's form on a terminal,
// else y/N on stdin. Tests replace it.
var confirmKill = func(stdout io.Writer, title, desc string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return confirmSimple(stdout, os.Stdin, title)
	}
	var ok bool
	form := huh.NewConfirm().Title(title).Description(desc).Affirmative("Yes, I'm sure").Negative("Cancel").Value(&ok)
	if err := form.WithTheme(theme.HuhDestructiveTheme()).Run(); err != nil {
		return false
	}
	return ok
}

// confirmSimple is ntm's y/N prompt: yes only for y or yes.
func confirmSimple(stdout io.Writer, stdin io.Reader, prompt string) bool {
	fmt.Fprintf(stdout, "%s [y/N]: ", prompt)
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes"
}

// runKillNTM is ntm's runKill: under --json no confirm and ntm's
// KillResponse, else the confirm (unless -f) and ntm's line.
func runKillNTM(name string, f killFlags, asJSON bool, stdout, stderr io.Writer) error {
	fail := func(err error) error {
		if asJSON {
			return jsonFailure(stdout, err)
		}
		return err
	}
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return fail(err)
	}
	defer ie.db.Close()
	// ntm's kill takes a unique prefix of the name under --json too
	// (normalizeExplicitLiveSessionName(…, true)).
	r, err := resolveSession(ctx, ie, name, false, stdout, stderr)
	if err != nil {
		return fail(err)
	}
	if r.WorkspaceID == "" {
		return fail(errors.New("session is required"))
	}
	resp, err := killSession(ctx, ie, r.Session, r.WorkspaceID, f, !asJSON && !f.force, stdout)
	if err != nil {
		return fail(err)
	}
	if asJSON {
		return writeNTMJSON(stdout, resp)
	}
	switch {
	case resp.Message == "Aborted.":
		fmt.Fprintln(stdout, "Aborted.")
	case !resp.Killed && len(f.tags) > 0:
		fmt.Fprintln(stdout, "No panes found matching tags.")
	default:
		fmt.Fprintln(stdout, resp.Message)
	}
	return nil
}

// killSession is ntm's executeKill: the panes that match --tag, or the
// whole session, after the confirm when ask is set.
func killSession(ctx context.Context, ie *instanceEnv, name, ws string, f killFlags, ask bool, stdout io.Writer) (KillResponse, error) {
	resp := KillResponse{GeneratedAt: time.Now().UTC(), Session: name}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return resp, err
	}
	var panes, tagged []string
	for _, p := range snap.Panes {
		if p.WorkspaceID != ws {
			continue
		}
		panes = append(panes, p.PaneID)
		if session.HasAnyTag(session.PaneTags(p.Label), f.tags) {
			tagged = append(tagged, p.PaneID)
		}
	}
	req := lifecycle.KillSessionRequest{WorkspaceID: ws, Reaper: killReaper()}
	if len(f.tags) > 0 {
		if len(tagged) == 0 {
			resp.Message = "No panes found matching tags"
			return resp, nil
		}
		if ask && !confirmKill(stdout, fmt.Sprintf("Kill %d pane(s)?", len(tagged)),
			fmt.Sprintf("This will terminate panes matching tags %v in session '%s'.", f.tags, name)) {
			resp.Message = "Aborted."
			return resp, nil
		}
		req.Panes = tagged
	} else if ask && !confirmKill(stdout, fmt.Sprintf("Kill session '%s'?", name),
		fmt.Sprintf("This will terminate %d running agent(s).", len(panes))) {
		resp.Message = "Aborted."
		return resp, nil
	}
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return resp, err
	}
	var res lifecycle.KillSessionResult
	if _, err := underOwner(ctx, ie, func() error {
		var kerr error
		res, kerr = lifecycle.KillSession(ctx, lenv, req)
		return kerr
	}); err != nil {
		return resp, err
	}
	resp.Killed = true
	if req.Panes != nil {
		resp.Message = fmt.Sprintf("Killed %d pane(s) matching tags", len(res.ClosedPanes))
	} else {
		resp.Message = fmt.Sprintf("Killed session '%s'", name)
	}
	return resp, nil
}

// runKillProject is ntm's runKillProject: every session whose base name
// is the project, after one confirm (unless -f), each killed as by
// `kill -f`.
func runKillProject(f killFlags, asJSON bool, stdout, stderr io.Writer) error {
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	ss, _, err := herdrSessions(ctx, ie, false)
	if err != nil {
		return err
	}
	var names []string
	byName := map[string]string{}
	for _, s := range ss {
		if sessionlabel.SessionBase(s.Name) == f.project {
			names = append(names, s.Name)
			byName[s.Name] = s.WorkspaceID
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no sessions found for project %q", f.project)
	}
	if !f.force && !confirmKill(stdout, fmt.Sprintf("Kill %d session(s)?", len(names)), fmt.Sprintf("Sessions: %s", strings.Join(names, ", "))) {
		fmt.Fprintln(stdout, "Aborted.")
		return nil
	}
	var failures []string
	for _, n := range names {
		resp, err := killSession(ctx, ie, n, byName[n], f, false, stdout)
		switch {
		case err != nil:
			failures = append(failures, fmt.Sprintf("%s: %v", n, err))
		case asJSON:
			if err := writeNTMJSON(stdout, resp); err != nil {
				return err
			}
		case !resp.Killed && len(f.tags) > 0:
			fmt.Fprintln(stdout, "No panes found matching tags.")
		default:
			fmt.Fprintln(stdout, resp.Message)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("some sessions failed to kill: %s", strings.Join(failures, "; "))
	}
	return nil
}
