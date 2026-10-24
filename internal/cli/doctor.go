package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

const doctorUsage = `Usage: nhm doctor [-v|--verbose] [--socket PATH] [--no-mail] [--json]
       nhm doctor contention [--terminate-pid N --confirm [--force]] [--json]
       nhm doctor --herdr-fit [--placements] [--socket PATH] [--json]

ntm's doctor on herdr: Tools (bv and br required, the other flywheel
tools optional), Dependencies (herdr in tmux's place, on nhm's protocol;
go; the optional grok and omp), Daemons (Agent Mail, the cm server),
Configuration (nhm's state directory, .beads, and nhm's checks: the
enrollment of this server, the state database, the owner switch, the
directory of Agent Mail's identity files, the kinds, each guard per
kind, and, when nhm owns the server, the maintenance runner), Safety
Defaults and Design Invariants. Each line is ✓, ⚠, ✗ or ?, and a box
ends it: Overall: HEALTHY, or UNHEALTHY with the counts. -v adds each
invariant's details. --json prints ntm's DoctorReport. It exits 1 only
when the report is unhealthy. --no-mail skips the Agent Mail check.

doctor contention lists the processes that hold a shared resource: the
beads database, cargo's registry, rsync, or an uninterruptible wait. A
holder whose directory is in this one is in the swarm. --terminate-pid
sends SIGTERM to a listed holder: only with --confirm, and one in the
swarm only with --force too.

--herdr-fit checks that the running herdr still fits nhm's interactive
surfaces (the X0 fit test): its protocol and version, nhm's plugin, and a
throwaway popup that runs a small program and closes. That popup is its
only effect on the server; when a popup is open already, it skips it. Run
it again after a herdr upgrade. --placements also opens the program as a
tab, a split and an overlay in a throwaway workspace, sends keys to each,
and reads the kill guard's fact. The clients of a live server see those
panes, so it runs only in CI (CI=true) or in nhm's sandbox
(SANDBOX_INSIDE=1).
`

// Check results.
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

// doctorCheck is one line of doctor.
type doctorCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	// guard is a guard line's state: ntm's No Silent Data Loss reads it.
	guard guards.State
}

// runDoctor is nhm's doctor --herdr-fit, on its own flags: ntm's doctor
// (newDoctorCmd) hands that mode to it. Its checks and envelope are nhm's.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, doctorUsage) }
	socket := fs.String("socket", "", "herdr API socket")
	noMail := fs.Bool("no-mail", false, "skip the Agent Mail check")
	fit := fs.Bool("herdr-fit", false, "check that the running herdr fits nhm's interactive surfaces")
	placements := fs.Bool("placements", false, "with --herdr-fit: also open a tab, a split and an overlay (sandbox, CI)")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	// The report itself is ntm's (newDoctorCmd); this runs --herdr-fit.
	if err != nil || len(pos) > 0 || !*fit || *fit && *noMail {
		if err == nil {
			fmt.Fprint(stderr, doctorUsage)
		}
		return exitRefused
	}
	if *placements && os.Getenv("CI") != "true" && os.Getenv("SANDBOX_INSIDE") != "1" {
		fmt.Fprintln(stderr, "nhm doctor: --placements opens panes that a live server's clients see; it runs only in CI (CI=true) or in nhm's sandbox (SANDBOX_INSIDE=1)")
		return exitRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	checks := herdrFitChecks(ctx, *socket, *placements)
	failed := 0
	for _, c := range checks {
		if c.Result == checkFail {
			failed++
		}
	}
	env := envelope{Command: "doctor", Result: map[string]any{"checks": checks, "failed": failed}}
	var runErr error
	code := exitOK
	if failed > 0 {
		runErr, code = fmt.Errorf("%d checks failed", failed), exitError
	}
	if *asJSON {
		return output(stdout, stderr, true, env, runErr, code, nil)
	}
	for _, c := range checks {
		fmt.Fprintf(stdout, "%-4s %-16s %s\n", c.Result, c.Name, c.Detail)
		if c.Fix != "" && c.Result != checkOK {
			fmt.Fprintf(stdout, "     %-16s fix: %s\n", "", c.Fix)
		}
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "nhm doctor: %v\n", runErr)
	}
	return code
}

func doctorChecks(ctx context.Context, socket string, withMail bool) []doctorCheck {
	var checks []doctorCheck
	add := func(c doctorCheck) { checks = append(checks, c) }
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if socket == "" {
		add(doctorCheck{Name: "herdr", Result: checkFail, Detail: "no herdr socket",
			Fix: "run nhm inside a herdr pane, or give --socket PATH"})
		add(doctorCheck{Name: "enrollment", Result: checkSkip, Detail: "not checked: no herdr socket"})
	} else {
		h := herdr.New(socket)
		pong, err := h.Ping(ctx)
		switch {
		case err != nil:
			add(doctorCheck{Name: "herdr", Result: checkFail, Detail: fmt.Sprintf("%s: %v", socket, err),
				Fix: "start the herdr server, or check the socket path"})
		case pong.Protocol != herdr.Protocol:
			add(doctorCheck{Name: "herdr", Result: checkFail,
				Detail: fmt.Sprintf("herdr %s speaks protocol %d; this nhm speaks %d", pong.Version, pong.Protocol, herdr.Protocol),
				Fix:    "install the nhm release for this herdr, or the herdr release this nhm pins"})
		default:
			add(doctorCheck{Name: "herdr", Result: checkOK, Detail: fmt.Sprintf("herdr %s, protocol %d, %s", pong.Version, pong.Protocol, socket)})
		}
		checks = append(checks, enrollmentChecks(ctx, socket)...)
	}

	// The checks below need no herdr server: they run without one too.
	home, err := agentmail.ConfigHome()
	if err != nil {
		add(doctorCheck{Name: "identity files", Result: checkFail, Detail: err.Error()})
	} else {
		dir := filepath.Join(home, "agent-mail", "identity")
		if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
			add(doctorCheck{Name: "identity files", Result: checkFail, Detail: dir + " is a symlink; Agent Mail and nhm refuse it",
				Fix: "replace the link with a directory"})
		} else {
			add(doctorCheck{Name: "identity files", Result: checkOK, Detail: dir})
		}
	}

	checks = append(checks, kindsCheck(kinds.ConfigError(), kinds.Names()))
	checks = append(checks, guardChecks(ctx)...)

	if withMail {
		c, err := agentmail.NewClient("")
		if err == nil {
			err = c.Call(ctx, "health_check", map[string]any{}, nil)
		}
		if err != nil {
			add(doctorCheck{Name: "agent mail", Result: checkWarn, Detail: err.Error(),
				Fix: "start Agent Mail (am serve-http), or set AGENT_MAIL_URL; status shows mail as unknown until then"})
		} else {
			add(doctorCheck{Name: "agent mail", Result: checkOK, Detail: c.URL})
		}
	}
	return checks
}

// enrollmentChecks report the enrollment of the herdr server at socket
// and, when it is enrolled, its instance: database, owner, maintenance.
func enrollmentChecks(ctx context.Context, socket string) []doctorCheck {
	root, err := store.StateRoot()
	if err != nil {
		return []doctorCheck{{Name: "state", Result: checkFail, Detail: err.Error(), Fix: "set HOME or XDG_STATE_HOME"}}
	}
	inst, err := store.Lookup(root, socket)
	switch {
	case errors.Is(err, store.ErrNotEnrolled):
		return []doctorCheck{{Name: "enrollment", Result: checkWarn, Detail: "this herdr server is not enrolled; hooks and actions do nothing",
			Fix: "nhm admin instance enroll (ACFS's install runs it)"}}
	case err != nil:
		return []doctorCheck{{Name: "enrollment", Result: checkFail, Detail: err.Error()}}
	}
	checks := []doctorCheck{{Name: "enrollment", Result: checkOK, Detail: fmt.Sprintf("%s (session %s) in %s", inst.ID, inst.Session, inst.Dir)}}
	return append(checks, instanceChecks(ctx, inst)...)
}

// kindsCheck reports the kind registry: a [kinds.*] table of nhm's config
// that does not load adds none of its kinds, and only doctor says so.
func kindsCheck(err error, names []string) doctorCheck {
	if err != nil {
		return doctorCheck{Name: "kinds", Result: checkWarn,
			Detail: "the config's kinds did not load; only the built-in kinds work: " + err.Error(),
			Fix:    "repair the [kinds.*] tables of ~/.config/nhm/config.toml"}
	}
	return doctorCheck{Name: "kinds", Result: checkOK, Detail: strings.Join(names, ", ")}
}

// guardChecks reports each guard per kind (plan-v8 2.14), in the project
// of the cwd when there is one. A missing guard is a warning here: spawn
// refuses the kind until it is configured.
func guardChecks(ctx context.Context) []doctorCheck {
	env := guards.Env{}
	if s, err := projectScope(ctx, "", ""); err == nil {
		env.ProjectRoot = s.ProjectRoot
	}
	var checks []doctorCheck
	for _, kind := range guards.Kinds() {
		for _, r := range guards.Check(kind, env) {
			c := doctorCheck{Name: "guard " + kind + " " + r.Guard, Detail: string(r.State), guard: r.State}
			switch r.State {
			case guards.Configured, guards.VerifiedForLaunch:
				c.Result = checkOK
				if len(r.Sources) > 0 {
					c.Detail += " (" + strings.Join(r.Sources, ", ") + ")"
				}
			case guards.Unsupported:
				c.Result, c.Detail = checkOK, c.Detail+": "+r.Detail
			default:
				c.Result, c.Detail, c.Fix = checkWarn, c.Detail+": "+r.Detail, r.Fix
			}
			checks = append(checks, c)
		}
	}
	return checks
}

func instanceChecks(ctx context.Context, inst store.Instance) []doctorCheck {
	var checks []doctorCheck
	var runner *doctorCheck
	db, err := store.Open(inst.Dir)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "database", Result: checkFail, Detail: err.Error(),
			Fix: "a newer nhm wrote it: install that release, or restore the database from its backup"})
	} else {
		v, verr := db.UserVersion(ctx)
		name, last, ran, lerr := wake.LastPass(ctx, db)
		_ = db.Close()
		if verr != nil {
			checks = append(checks, doctorCheck{Name: "database", Result: checkFail, Detail: verr.Error()})
		} else {
			checks = append(checks, doctorCheck{Name: "database", Result: checkOK, Detail: fmt.Sprintf("schema %d", v)})
		}
		c := maintainCheck(name, last, ran, lerr, time.Now())
		runner = &c
	}
	o, err := core.ReadOwner(inst.Dir)
	switch {
	case err != nil:
		checks = append(checks, doctorCheck{Name: "owner", Result: checkFail, Detail: err.Error(),
			Fix: "every mutation is refused until owner.toml is repaired, or removed and nhm admin takeover run again"})
	case o.Owner == core.OwnerNhm:
		checks = append(checks, doctorCheck{Name: "owner", Result: checkOK, Detail: fmt.Sprintf("nhm owns this server (generation %d)", o.Generation)})
	default:
		checks = append(checks, doctorCheck{Name: "owner", Result: checkWarn,
			Detail: fmt.Sprintf("ACFS's Bash owns this server (generation %d); nhm reads only", o.Generation),
			Fix:    "nhm admin takeover, when the rehearsal has passed"})
	}
	// The maintenance runner matters only when nhm owns the server.
	if err == nil && o.Owner == core.OwnerNhm && runner != nil {
		checks = append(checks, *runner)
	}
	return checks
}

// maintainRunnerStale is how old the last maintenance pass may be: the
// timer runs every 15 s.
const maintainRunnerStale = 2 * time.Minute

// maintainCheck reports the maintenance runner (plan-v8 2.8): which one
// ran the last pass, and whether one runs at all.
func maintainCheck(runner string, last time.Time, ran bool, err error, now time.Time) doctorCheck {
	c := doctorCheck{Name: "maintenance"}
	fix := "install ACFS's nhm-maintain timer, or run nhm maintain --loop; until then, mail to a busy agent waits"
	switch {
	case err != nil:
		c.Result, c.Detail = checkWarn, err.Error()
	case !ran:
		c.Result, c.Detail, c.Fix = checkWarn, "no runner: nhm maintain never ran", fix
	case now.Sub(last) > maintainRunnerStale:
		c.Result, c.Detail, c.Fix = checkWarn, fmt.Sprintf("no runner: the last pass (%s) was at %s", runner, last.UTC().Format(time.RFC3339)), fix
	default:
		c.Result, c.Detail = checkOK, fmt.Sprintf("runner %s, last pass at %s", runner, last.UTC().Format(time.RFC3339))
	}
	return c
}
