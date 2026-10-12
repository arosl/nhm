package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

const wakeUsage = `Usage: nhm wake [--once] [--recipients NAME[,NAME]]… [--project DIR] [--socket PATH] [--json]

Run one wake pass by hand (plan-v8 2.8). For each recipient (an Agent
Mail name, a herdr name or a pane id; without --recipients, every bound
agent), read its Agent Mail inbox without consuming it, then type "Check
your Agent Mail inbox." once if it has mail that no nudge announced and it
is idle or done. A working, blocked or unknown agent keeps its pending
mail, and so does one in its 120-second cooldown or at the rate limit
(three nudges per sender and thread per hour): nhm maintain delivers it
later. Each decision is printed, with its reason.

--once is the default and the only mode: nhm maintain --loop is the
runner. Needs nhm to own the herdr server.
`

func runWake(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wake", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, wakeUsage) }
	fs.Bool("once", true, "one pass (the default)")
	var recipients listFlag
	fs.Var(&recipients, "recipients", "recipients, comma-separated (repeatable)")
	project := fs.String("project", "", "the project of the Agent Mail names")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 {
		fmt.Fprint(stderr, wakeUsage)
		return exitRefused
	}
	env := envelope{Command: "wake"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	names := splitList(recipients)
	projectKey := ""
	if *project != "" {
		s, err := projectScope(ctx, ie.inst.ID, *project)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		projectKey = s.MailProjectKey
	}
	w, err := ie.wakeEnv(true)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	var targets []bindings.Binding
	for _, n := range names {
		b, err := wake.Resolve(ctx, w.Bindings, projectKey, n)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		targets = append(targets, b)
	}
	res, err := ie.wakePass(ctx, w, targets, wake.TriggerManual)
	env.Result, env.Warnings = res, res.Warnings
	code := exitOK
	if err != nil {
		code = exitError
		if errors.Is(err, core.ErrNotOwner) {
			code = exitRefused
		}
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printWake(w, res) })
}

// wakeResult is a wake pass's result: the report and the nudges whose
// submission is not known.
type wakeResult struct {
	wake.Report
	PossiblySubmitted []wake.Uncertain `json:"possibly_submitted"`
}

// wakeEnv is the wake side of the instance; withMail adds the Agent
// Mail client that reads inboxes.
func (ie *instanceEnv) wakeEnv(withMail bool) (wake.Env, error) {
	w := wake.Env{DB: ie.db, Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, Config: wake.DefaultConfig(), Now: time.Now}
	if withMail {
		mail, err := agentmail.NewClient("")
		if err != nil {
			return w, err
		}
		w.Mail = mail
	}
	return w, nil
}

// wakePass runs one pass under the owner lock; nil targets means every
// active binding.
func (ie *instanceEnv) wakePass(ctx context.Context, w wake.Env, targets []bindings.Binding, trigger string) (wakeResult, error) {
	res := wakeResult{PossiblySubmitted: []wake.Uncertain{}}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		var err error
		res.Report, err = wake.Pass(ctx, w, targets, trigger)
		return err
	})
	if res.Decisions == nil {
		res.Report = wake.Report{Decisions: []wake.Decision{}, Unbound: []string{}, Warnings: []string{}}
	}
	if err != nil {
		return res, err
	}
	if u, err := wake.PossiblySubmitted(ctx, w); err == nil {
		res.PossiblySubmitted = u
	}
	return res, nil
}

func printWake(w io.Writer, res wakeResult) {
	for _, d := range res.Decisions {
		line := fmt.Sprintf("%s (%s): %s", d.MailName, dash(d.PaneID), d.Action)
		if d.Outcome != "" {
			line += " (" + strings.ReplaceAll(d.Outcome, "_", " ") + ")"
		}
		if len(d.Pending) > 0 {
			line += " for #" + strings.Join(d.Pending, " #")
		}
		if d.Reason != "" {
			line += ": " + d.Reason
		}
		if d.WakeAfter != nil {
			line += "; next try after " + d.WakeAfter.UTC().Format(time.RFC3339)
		}
		fmt.Fprintln(w, line)
	}
	if len(res.Decisions) == 0 {
		fmt.Fprintln(w, "no pending mail")
	}
	for _, u := range res.PossiblySubmitted {
		fmt.Fprintf(w, "possibly submitted, not repeated: %s at %s (operation %s)\n", u.MailName, u.At.UTC().Format(time.RFC3339), u.OperationID)
	}
	if res.Recorded > 0 || res.Read > 0 || res.Stale > 0 {
		fmt.Fprintf(w, "inboxes: %d new pending, %d read since the last pass, %d stale attempts settled\n", res.Recorded, res.Read, res.Stale)
	}
}

// splitList splits comma-separated values and drops empty ones.
func splitList(values []string) []string {
	var out []string
	for _, v := range values {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
