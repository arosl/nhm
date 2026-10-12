package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/hooks"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

const maintainUsage = `Usage: nhm maintain [--once | --loop [--interval D]] [--mail-from NAME [--mail-to NAME]]
                    [--socket PATH] [--json]

Run the maintenance pass (plan-v8 2.8): settle wake attempts that never
finished, read every bound agent's inbox without consuming it (mail sent
from the am CLI or the web UI that no hook saw), deliver the due wakes
(cooldown and rate limit passed, agent idle or done), and reconcile each
bound pane's name. With --mail-from, it also runs the limits scan and
mails each new limit event (nhm limits).

--once (the default) runs one pass; this is what ACFS's systemd timer
runs every 15 s. --loop repeats it every --interval (default 15s). One
runner per herdr server: a second one exits 0 and says so. nhm doctor
shows the last pass and its runner. Under ACFS's Bash, the pass does
nothing.
`

// maintainLockFile is the singleton lock in the instance directory.
const maintainLockFile = "maintain.lock"

func runMaintain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("maintain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, maintainUsage) }
	fs.Bool("once", false, "one pass (the default)")
	loop := fs.Bool("loop", false, "repeat every --interval")
	interval := fs.Duration("interval", 15*time.Second, "time between passes with --loop")
	mailFrom := fs.String("mail-from", "", "Agent Mail sender of the limit messages; runs the limits scan")
	mailTo := fs.String("mail-to", "", "Agent Mail recipient of the limit messages (default: the sender)")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 0 || *interval <= 0 || (*mailTo != "" && *mailFrom == "") {
		fmt.Fprint(stderr, maintainUsage)
		return exitRefused
	}
	env := envelope{Command: "maintain"}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	unlock, err := tryLock(filepath.Join(ie.inst.Dir, maintainLockFile))
	if errors.Is(err, errLocked) {
		env.Result = maintainResult{Skipped: "another nhm maintain runs for this herdr server"}
		return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
			fmt.Fprintln(w, "another nhm maintain runs for this herdr server; nothing to do")
		})
	}
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer unlock()
	opts := maintainOptions{socket: *socket, mailFrom: *mailFrom, mailTo: *mailTo, runner: wake.RunnerOnce}
	if *loop {
		opts.runner = wake.RunnerLoop
	}
	for {
		res, err := ie.maintainPass(ctx, opts)
		env.Result, env.Warnings = res, res.Warnings
		if !*loop {
			code := exitOK
			if err != nil {
				code = exitError
			}
			return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printMaintain(w, res, false) })
		}
		if err != nil {
			fmt.Fprintf(stderr, "nhm maintain: %v\n", err)
		} else {
			printMaintain(stdout, res, true)
			for _, w := range res.Warnings {
				fmt.Fprintf(stderr, "nhm maintain: warning: %s\n", w)
			}
		}
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(*interval):
		}
	}
}

// maintainOptions are one pass's settings.
type maintainOptions struct {
	socket           string
	mailFrom, mailTo string
	runner           string
}

// maintainResult is one maintenance pass.
type maintainResult struct {
	// Skipped says why the pass did nothing.
	Skipped string        `json:"skipped,omitempty"`
	Runner  string        `json:"runner,omitempty"`
	At      *time.Time    `json:"at,omitempty"`
	Wake    *wakeResult   `json:"wake,omitempty"`
	Names   []string      `json:"names"`
	Limits  []limitReport `json:"limits"`
	// LimitsSkipped says why the limits scan did not run.
	LimitsSkipped string   `json:"limits_skipped,omitempty"`
	Warnings      []string `json:"warnings"`
}

// maintainPass runs one maintenance pass. The board calls it on each
// refresh with runner "board".
func (ie *instanceEnv) maintainPass(ctx context.Context, opts maintainOptions) (maintainResult, error) {
	res := maintainResult{Names: []string{}, Limits: []limitReport{}, Warnings: []string{}}
	w, err := ie.wakeEnv(true)
	if err != nil {
		return res, err
	}
	wr, err := ie.wakePass(ctx, w, nil, wake.TriggerMaintain)
	if errors.Is(err, core.ErrNotOwner) {
		// The Bash owns the server: its own wake loop runs.
		res.Skipped = err.Error()
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Wake = &wr
	res.Warnings = append(res.Warnings, wr.Warnings...)

	// The cheap name reconcile: each bound pane, as a plugin event would.
	henv := hooks.Env{DB: ie.db, Bindings: w.Bindings, Herdr: ie.herdr, InstanceDir: ie.inst.Dir,
		Getenv: os.Getenv, Now: time.Now, LockWait: time.Second}
	active, err := w.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return res, err
	}
	for _, b := range active {
		if b.PaneID == "" {
			continue
		}
		r, err := hooks.ReconcilePane(ctx, henv, b.PaneID)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: reconcile: %v", b.MailName, err))
			continue
		}
		for _, a := range r.Actions {
			res.Names = append(res.Names, b.MailName+": "+a)
		}
		res.Warnings = append(res.Warnings, r.Warnings...)
	}

	if opts.mailFrom == "" {
		res.LimitsSkipped = "no --mail-from: nhm status shows limits as attention items"
	} else if reports, warnings, err := ie.limitsScan(ctx, opts); err != nil {
		res.Warnings = append(res.Warnings, "limits: "+err.Error())
	} else {
		res.Limits = reports
		res.Warnings = append(res.Warnings, warnings...)
	}

	now := time.Now().UTC()
	res.Runner, res.At = opts.runner, &now
	if err := wake.RecordPass(ctx, ie.db, opts.runner, now); err != nil {
		res.Warnings = append(res.Warnings, err.Error())
	}
	return res, nil
}

// limitsScan runs one limits pass that mails each new limit event.
func (ie *instanceEnv) limitsScan(ctx context.Context, opts maintainOptions) ([]limitReport, []string, error) {
	r, err := openReader(opts.socket, false)
	if err != nil {
		return nil, nil, err
	}
	defer r.close()
	seenPath, err := limitsSeenPath(r.inst)
	if err != nil {
		return nil, nil, err
	}
	mail, err := agentmail.NewClient("")
	if err != nil {
		return nil, nil, err
	}
	to := opts.mailTo
	if to == "" {
		to = opts.mailFrom
	}
	return limitsPass(ctx, r, "", 15, seenPath, mail, opts.mailFrom, to, false)
}

// printMaintain prints a pass. A loop prints only what changed: nudges,
// name actions and limits.
func printMaintain(w io.Writer, res maintainResult, quiet bool) {
	if res.Skipped != "" {
		if !quiet {
			fmt.Fprintf(w, "nothing to do: %s\n", res.Skipped)
		}
		return
	}
	stamp := ""
	if res.At != nil {
		stamp = res.At.Format(time.RFC3339) + " "
	}
	if res.Wake != nil {
		if quiet {
			for _, d := range res.Wake.Decisions {
				if d.Action == wake.ActionNudged {
					fmt.Fprintf(w, "%swoke %s (%s): %s for %d message(s)\n", stamp, d.MailName, dash(d.PaneID), d.Outcome, len(d.Pending))
				}
			}
		} else {
			printWake(w, *res.Wake)
		}
	}
	for _, n := range res.Names {
		fmt.Fprintf(w, "%sname: %s\n", stamp, n)
	}
	printLimits(w, res.Limits, false)
	if !quiet {
		fmt.Fprintf(w, "pass done (%s)\n", res.Runner)
	}
}

var errLocked = errors.New("locked")

// tryLock takes an exclusive flock on path without waiting.
func tryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
