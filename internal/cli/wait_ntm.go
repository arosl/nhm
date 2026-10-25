// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/wait.go (newWaitCmd's use, flags, defaults and texts,
// runWait's lines and errors, isValidCondition, WaitTimeoutError,
// WaitErrorStateError); see LICENSE in this directory and PROVENANCE.md;
// ntm's --type is agenttype_ntm.go's ntmTypeKinds. Local
// changes: the agents are the session's bound agents, and the calling
// pane, nhm's user pane, is left out; the poll loop is core/wait's
// NTMWait, on herdr's agent states where ntm reads a screen classifier;
// --exit-on-error ends at an agent that can no longer meet the condition
// (herdr has no error state); --pane takes ntm's index or the slot N.M; a
// pane is its herdr pane id; nhm's --name, --project, --after-op and
// --socket beside ntm's flags; the timeout and error types are
// unexported.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// waitFlags are ntm's wait flags and nhm's beside them.
type waitFlags struct {
	until            string
	timeout, poll    time.Duration
	pane             ntmPaneIndex
	agentType        string
	any, exitOnError bool
	count            int
	names            []string
	project, afterOp string
	socket           string
}

// newWaitCmd is ntm's wait on the shared parser. --after-op, nhm's own
// wait for an operation's turn, keeps nhm's result (runWaitAfterOp).
func newWaitCmd() *cobra.Command {
	f := waitFlags{pane: ntmPaneIndex{n: -1}}
	cmd := &cobra.Command{
		Use:   "wait [session]",
		Short: "Wait until agents reach a desired state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			if f.afterOp != "" {
				return runWaitAfterOp(name, f, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			return runWaitNTM(name, f, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&f.until, "until", "idle", "Wait condition: idle, complete, generating, healthy")
	fs.DurationVar(&f.timeout, "timeout", defaultWaitTimeout, "Maximum wait time")
	fs.DurationVar(&f.poll, "poll", wait.DefaultPoll, "Polling interval")
	fs.Var(&f.pane, "pane", "Wait for specific pane only (-1 = all)")
	fs.StringVar(&f.agentType, "type", "", "Wait for specific agent type (claude, codex, gemini)")
	fs.BoolVar(&f.any, "any", false, "Wait for ANY agent (vs ALL)")
	fs.BoolVar(&f.exitOnError, "exit-on-error", false, "Exit immediately if ERROR state detected")
	fs.IntVar(&f.count, "count", 1, "With --any, wait for at least N agents matching condition")
	fs.StringSliceVar(&f.names, "name", nil, "an Agent Mail or herdr name (repeatable)")
	fs.StringVar(&f.project, "project", "", "only this project's agents")
	fs.StringVar(&f.afterOp, "after-op", "", "wait for this operation's turn (nhm's result)")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	return cmd
}

// ntmPaneIndex is ntm's --pane: an int, negative for all panes, with
// ntm's error for anything else. nhm also takes the slot N.M.
type ntmPaneIndex struct {
	n    int
	slot string
}

func (p *ntmPaneIndex) String() string {
	if p.slot != "" {
		return p.slot
	}
	return strconv.Itoa(p.n)
}

func (p *ntmPaneIndex) Type() string { return "int" }

func (p *ntmPaneIndex) Set(s string) error {
	if slot, err := bindings.ParseSlot(s); err == nil {
		p.n, p.slot = 0, slot.String()
		return nil
	}
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return err
	}
	p.n, p.slot = int(v), ""
	return nil
}

// errWaitTimeout is ntm's WaitTimeoutError; errWaitAgent is its
// WaitErrorStateError. ntm exits 1 for both.
type errWaitTimeout struct{ d time.Duration }

func (e errWaitTimeout) Error() string { return fmt.Sprintf("wait timed out after %v", e.d) }

type errWaitAgent struct{ pane string }

func (e errWaitAgent) Error() string {
	return fmt.Sprintf("agent in pane '%s' entered ERROR state", e.pane)
}

// ntmWaitConditions is ntm's isValidCondition: a comma list of idle,
// complete, generating and healthy, and nhm's done.
func ntmWaitConditions(until string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(until, ",") {
		switch c := strings.TrimSpace(part); c {
		case "idle", "complete", "generating", "healthy", "done":
			out = append(out, c)
		default:
			return nil, fmt.Errorf("invalid condition '%s': must be one of idle, complete, generating, healthy", until)
		}
	}
	return out, nil
}

// waitPaneSlot is --pane as a slot of the session: "" for all panes.
func waitPaneSlot(ctx context.Context, ie *instanceEnv, ws string, p ntmPaneIndex, all []bindings.Binding) (string, error) {
	switch {
	case p.slot != "":
		return p.slot, nil
	case p.n < 0:
		return "", nil
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	active := ""
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == ws {
			active = w.ActiveTabID
		}
	}
	t, err := session.ParsePane(strconv.Itoa(p.n), session.AgentTabs(all), active)
	if err != nil {
		// As ntm's: an index that no agent pane has matches nothing.
		return "", fmt.Errorf("%w: %v", wait.ErrNoTargets, err)
	}
	return t.Slot, nil
}

// runWaitNTM is ntm's runWait on nhm's bound agents and herdr's states.
// Its errors are ntm's, returned for the root to write; exit 1.
func runWaitNTM(name string, f waitFlags, asJSON bool, stdout, stderr io.Writer) error {
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	r, err := resolveSession(ctx, ie, name, asJSON, stdout, stderr)
	if err != nil || r.WorkspaceID == "" {
		return err
	}
	conds, err := ntmWaitConditions(f.until)
	if err != nil {
		return err
	}
	var kindsOf []string
	if f.agentType != "" {
		if kindsOf, err = ntmTypeKinds(f.agentType); err != nil {
			return err
		}
	}
	t := theme.Current()
	fmt.Fprintf(stdout, "%s⏳%s Waiting for '%s' until %s (timeout: %v)...\n", colorize(t.Info), colorize(t.Text), r.Session, f.until, f.timeout)

	noPanes := func() error {
		fmt.Fprintf(stdout, "%s!%s No matching panes found\n", colorize(t.Warning), colorize(t.Text))
		return errors.New("no panes match the filter criteria")
	}
	first, err := waitSelect(ctx, ie, r.WorkspaceID, f, kindsOf)
	if errors.Is(err, wait.ErrNoTargets) && len(f.names) == 0 {
		return noPanes()
	}
	if err != nil {
		return err
	}
	// Each poll selects the agents again, as ntm lists the panes again;
	// the first poll takes the selection above.
	sel := func(ctx context.Context) ([]bindings.Binding, error) {
		if first != nil {
			list := first
			first = nil
			return list, nil
		}
		list, err := waitSelect(ctx, ie, r.WorkspaceID, f, kindsOf)
		if errors.Is(err, wait.ErrNoTargets) {
			return nil, nil
		}
		return list, err
	}
	start := time.Now()
	wctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	res, err := wait.NTMWait(wctx, ie.herdr, sel, wait.NTMRequest{Conds: conds, Any: f.any, Count: f.count, ExitOnError: f.exitOnError, Poll: f.poll})
	switch {
	case err != nil && wctx.Err() != nil:
		fmt.Fprintf(stdout, "%s✗%s Timeout after %v\n", colorize(t.Error), colorize(t.Text), f.timeout)
		return errWaitTimeout{d: f.timeout}
	case err != nil:
		return err
	case res.Lost != "":
		fmt.Fprintf(stdout, "%s✗%s Agent error detected in %s\n", colorize(t.Error), colorize(t.Text), res.Lost)
		return errWaitAgent{pane: res.Lost}
	case res.NoPanes:
		return noPanes()
	}
	fmt.Fprintf(stdout, "%s✓%s Condition '%s' met after %v\n", colorize(t.Success), colorize(t.Text), f.until, time.Since(start).Round(time.Millisecond))
	for _, a := range res.Met {
		state, _, _ := snapshot.Activity(a.State)
		fmt.Fprintf(stdout, "    %s: %s\n", a.PaneID, state)
	}
	return nil
}

// waitSelect is ntm's filterPanesForWait on nhm's bindings: the session's
// bound agents but the caller's (unless named), --pane's slot, --type's
// kinds, and nhm's --name and --project.
func waitSelect(ctx context.Context, ie *instanceEnv, ws string, f waitFlags, kindsOf []string) ([]bindings.Binding, error) {
	store := bindings.NewStore(ie.db)
	projectKey := ""
	if f.project != "" {
		s, err := projectScope(ctx, ie.inst.ID, f.project)
		if err != nil {
			return nil, err
		}
		projectKey = s.MailProjectKey
	}
	all, err := store.List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	sel := wait.Selector{Names: f.names, Kinds: kindsOf, Exclude: ie.callerPane(ctx)}
	slot, err := waitPaneSlot(ctx, ie, ws, f.pane, all)
	if err != nil {
		return nil, err
	}
	if slot != "" {
		sel.Slots = []string{slot}
	}
	return wait.Select(ctx, store, ws, projectKey, sel)
}
