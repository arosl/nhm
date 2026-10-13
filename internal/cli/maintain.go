package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
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
	"github.com/arosl/nhm/internal/store"
)

const maintainUsage = `Usage: nhm maintain [--once | --loop [--interval D]] [--mail-from NAME [--mail-to NAME]]
                    [--socket PATH] [--json]

Run the maintenance pass (plan-v8 2.8): settle wake attempts that never
finished, read every bound agent's inbox without consuming it (mail sent
from the am CLI or the web UI that no hook saw), deliver the due wakes
(cooldown and rate limit passed, agent idle or done), and reconcile each
bound pane's name. A bound agent's tab that still has herdr's default
label (a number) and holds only that pane gets the agent's Mail name.
With --mail-from, it also runs the limits scan and
mails each new limit event (nhm limits).

--once (the default) runs one pass; this is what ACFS's systemd timer
runs every 15 s. --loop repeats it every --interval (default 15s). One
runner per herdr server: a second one exits 0 and says so. nhm doctor
shows the last pass and its runner. Under ACFS's Bash, the pass does
nothing. With --once, a herdr server that is not enrolled or does not
run is nothing to do too: exit 0, the reason on one stdout line, nothing
on stderr. --loop waits for such a server: it says why once, then tries
again every --interval until a pass can run.
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
	// ACFS's timer runs --once every 15 s whether or not nhm serves this
	// herdr server: not enrolled and no server are nothing to do, as the
	// Bash owning it is (acfs-9hkh.78).
	skip := func(why string) int {
		env.Result = maintainResult{Skipped: why, Names: []string{}, Limits: []limitReport{}, Warnings: []string{}}
		return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) { fmt.Fprintf(w, "nothing to do: %s\n", why) })
	}
	ready := func() (*instanceEnv, string, error) { return maintainReady(*socket) }
	var ie *instanceEnv
	if *loop {
		// --loop waits for a server to enrol and run (nhm-wmn).
		if ie, err = waitForServer(ctx, ready, *interval, stdout); err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitError, nil)
		}
		if ie == nil {
			return exitOK
		}
	} else {
		var why string
		if ie, why, err = ready(); err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitError, nil)
		}
		if why != "" {
			return skip(why)
		}
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

// maintainReady opens the instance for a pass. why, when not "", says
// why there is nothing to do: the server is not enrolled, or none runs.
func maintainReady(socket string) (ie *instanceEnv, why string, err error) {
	ie, err = openInstance(socket)
	if errors.Is(err, store.ErrNotEnrolled) {
		return nil, "this herdr server is not enrolled (nhm admin instance enroll)", nil
	}
	if err != nil {
		return nil, "", err
	}
	up, err := serverRuns(ie.herdr.SocketPath)
	if err != nil || !up {
		_ = ie.db.Close()
		if err != nil {
			return nil, "", err
		}
		return nil, "no herdr server runs on " + ie.herdr.SocketPath, nil
	}
	return ie, "", nil
}

// waitForServer is --loop's start: it tries ready every interval until a
// pass can run, and says on out why it waits, once per reason. It returns
// nil and no error when ctx ends first, and fails on what waiting cannot
// fix (a socket path the kernel will not take, a database it cannot open).
func waitForServer(ctx context.Context, ready func() (*instanceEnv, string, error), interval time.Duration,
	out io.Writer) (*instanceEnv, error) {
	said := ""
	for {
		ie, why, err := ready()
		if err != nil || why == "" {
			return ie, err
		}
		if why != said {
			fmt.Fprintf(out, "nhm maintain: waiting: %s\n", why)
			said = why
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(interval):
		}
	}
}

// serverRuns reports false only when nothing listens on the socket: the
// file is missing, or the connection is refused (a socket left by a
// server that stopped). A path the kernel will not take (over the
// sockaddr limit: EINVAL, ENAMETOOLONG) cannot tell, and is an error,
// never a quiet skip. Any other failure is the pass's to report.
func serverRuns(socket string) (bool, error) {
	c, err := net.DialTimeout("unix", socket, 2*time.Second)
	switch {
	case err == nil:
		_ = c.Close()
		return true, nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
		return false, nil
	case errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENAMETOOLONG):
		return false, fmt.Errorf("cannot tell whether a herdr server runs on %s: %w", socket, err)
	}
	return true, nil
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

	// The cheap name reconcile: each bound pane, as a plugin event would,
	// and its tab when herdr's default label (a number) is still on it.
	henv := hooks.Env{DB: ie.db, Bindings: w.Bindings, Herdr: ie.herdr, InstanceDir: ie.inst.Dir,
		Getenv: os.Getenv, Now: time.Now, LockWait: time.Second, TabLabels: true}
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
