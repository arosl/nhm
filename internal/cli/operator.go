package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

const operatorUsage = `Usage: nhm operator [PANE] [--socket PATH] [--json]
       nhm operator --off [PANE] [--socket PATH] [--json]
       nhm operator --list [--socket PATH] [--json]

Mark a herdr pane as an operator's: a person works there, in a shell or
in their own agent session. nhm never binds the pane, so no nhm send,
nudge, broadcast or interrupt reaches it: those go to bound agents only.
The Agent Mail registration hook, the import of Bash-spawned agents and
nhm adopt refuse the pane. An agent session in it can still register
with Agent Mail and read and send mail. PANE is a herdr pane id; the
default is the calling pane. nhm retires the pane's bindings, active or
displaced: the agent runs on, its Agent Mail identity stays active, and
nhm deletes its own copy of the registration token.

--off clears the mark: an agent that registers in the pane is bound
again. --list shows the marked panes and whether herdr has them. nhm
maintain drops the mark of a pane that herdr no longer has. Runs under
any owner.

Exit codes: 0 done; 1 an error; 2 refused (no such pane in herdr).
`

// operatorResult is nhm operator's result for a mark and for --off.
type operatorResult struct {
	Action string                 `json:"action"` // marked, unmarked, not_marked
	Pane   *bindings.OperatorPane `json:"pane,omitempty"`
	// Retired are the pane's bindings, active or displaced, that the mark
	// retired.
	Retired []operatorRetiredBinding `json:"retired_bindings,omitempty"`
}

// operatorListResult is nhm operator --list's result.
type operatorListResult struct {
	Action string              `json:"action"` // listed
	Panes  []operatorListEntry `json:"panes"`
}

// operatorListEntry is a marked pane in --list.
type operatorListEntry struct {
	bindings.OperatorPane
	// InHerdr is whether herdr has the pane now; nil when herdr could not
	// be read.
	InHerdr *bool `json:"in_herdr"`
}

// operatorRetiredBinding is the binding a mark retired.
type operatorRetiredBinding struct {
	BindingID  string `json:"binding_id"`
	MailName   string `json:"mail_name"`
	ProjectKey string `json:"mail_project_key"`
}

// operatorRetireReason is the status reason of a binding that a mark
// retired.
const operatorRetireReason = "the pane was marked as an operator's (nhm operator)"

func runOperator(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("operator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, operatorUsage) }
	off := fs.Bool("off", false, "clear the pane's mark")
	list := fs.Bool("list", false, "list the marked panes")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 1 || (*off && *list) || (*list && len(pos) > 0) {
		fmt.Fprint(stderr, operatorUsage)
		return exitRefused
	}
	env := envelope{Command: "operator"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	if *list {
		res, warnings, err := operatorList(ctx, ie)
		env.Result, env.Warnings = res, warnings
		code := exitOK
		if err != nil {
			code = exitError
		}
		return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printOperatorList(w, res) })
	}
	pane := ie.callerPane(ctx)
	if len(pos) == 1 {
		pane = pos[0]
	}
	if pane == "" {
		return output(stdout, stderr, *asJSON, env, errors.New("no pane: give PANE, or run nhm operator in a herdr pane"), exitRefused, nil)
	}
	var res operatorResult
	code := exitOK
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err = core.WithOwnerShared(lctx, ie.inst.Dir, func(core.Owner) error {
		var oerr error
		if *off {
			res, oerr = operatorUnmark(ctx, ie, pane)
			return oerr
		}
		res, code, oerr = operatorMark(ctx, ie, pane)
		return oerr
	})
	if err != nil && code == exitOK {
		code = exitError
	}
	env.Result = res
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printOperator(w, pane, res) })
}

// operatorMark marks pane and retires its bindings, active or displaced.
// The caller holds the owner lock shared. The mark comes first: a
// registration that binds the pane after it is refused, and one that
// bound it before is retired here.
func operatorMark(ctx context.Context, ie *instanceEnv, pane string) (operatorResult, int, error) {
	res := operatorResult{Action: "marked"}
	info, err := ie.herdr.PaneGet(ctx, pane)
	if err != nil {
		if herdr.ErrorCode(err) == herdr.CodePaneNotFound {
			return res, exitRefused, fmt.Errorf("herdr has no pane %s", pane)
		}
		return res, exitError, fmt.Errorf("herdr: %w", err)
	}
	s := bindings.NewStore(ie.db)
	mark, err := s.MarkOperator(ctx, bindings.OperatorPane{PaneID: pane, WorkspaceID: info.WorkspaceID, MarkedAt: time.Now()})
	if err != nil {
		return res, exitError, err
	}
	res.Pane = &mark
	open, err := s.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
	if err != nil {
		return res, exitError, err
	}
	for _, b := range open {
		if b.PaneID != pane {
			continue
		}
		if _, err := s.Retire(ctx, b.ID, b.Generation, operatorRetireReason); err != nil {
			return res, exitError, fmt.Errorf("the pane is marked; its binding %s stays: %w", b.ID, err)
		}
		// No token outlives the binding it was kept for; the agent holds
		// its own.
		_ = store.DeleteSecret(ie.inst.Dir, b.ID)
		res.Retired = append(res.Retired, operatorRetiredBinding{BindingID: b.ID, MailName: b.MailName, ProjectKey: b.MailProjectKey})
	}
	return res, exitOK, nil
}

func operatorUnmark(ctx context.Context, ie *instanceEnv, pane string) (operatorResult, error) {
	ok, err := bindings.NewStore(ie.db).UnmarkOperator(ctx, pane)
	if err != nil {
		return operatorResult{}, err
	}
	if !ok {
		return operatorResult{Action: "not_marked"}, nil
	}
	return operatorResult{Action: "unmarked"}, nil
}

func operatorList(ctx context.Context, ie *instanceEnv) (operatorListResult, []string, error) {
	res := operatorListResult{Action: "listed", Panes: []operatorListEntry{}}
	marks, err := bindings.NewStore(ie.db).Operators(ctx)
	if err != nil {
		return res, nil, err
	}
	var warnings []string
	var live map[string]bool
	if len(marks) > 0 {
		snap, err := ie.herdr.Snapshot(ctx)
		if err != nil {
			warnings = append(warnings, "herdr: "+err.Error()+"; whether herdr has each pane is unknown")
		} else {
			live = map[string]bool{}
			for _, p := range snap.Panes {
				live[p.PaneID] = true
			}
		}
	}
	for _, m := range marks {
		e := operatorListEntry{OperatorPane: m}
		if live != nil {
			in := live[m.PaneID]
			e.InHerdr = &in
		}
		res.Panes = append(res.Panes, e)
	}
	return res, warnings, nil
}

func printOperator(w io.Writer, pane string, res operatorResult) {
	switch res.Action {
	case "marked":
		fmt.Fprintf(w, "%s is an operator's pane: nhm never binds it, so no nhm send, nudge or broadcast reaches it\n", pane)
		for _, r := range res.Retired {
			fmt.Fprintf(w, "retired %s's binding %s; the agent runs on and its Agent Mail identity stays active\n", r.MailName, r.BindingID)
		}
	case "unmarked":
		fmt.Fprintf(w, "%s is not an operator's pane now: an agent that registers there is bound\n", pane)
	case "not_marked":
		fmt.Fprintf(w, "%s has no operator mark\n", pane)
	}
}

func printOperatorList(w io.Writer, res operatorListResult) {
	if len(res.Panes) == 0 {
		fmt.Fprintln(w, "no operator panes")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PANE\tWORKSPACE\tMARKED\tIN HERDR")
	for _, p := range res.Panes {
		in := "unknown"
		if p.InHerdr != nil {
			in = map[bool]string{true: "yes", false: "no"}[*p.InHerdr]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.PaneID, dash(p.WorkspaceID), p.MarkedAt.UTC().Format(time.RFC3339), in)
	}
	_ = tw.Flush()
}
