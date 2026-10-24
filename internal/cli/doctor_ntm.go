// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/doctor.go
// (newDoctorCmd, DoctorReport, SafetyDefaults, InvariantCheck, ToolCheck,
// DepCheck, DaemonCheck, ConfigCheck, doctorExitFailure, runDoctor,
// performDoctorCheck's counts and verdict, checkTools' statuses,
// checkDependencies' go, grok and omp checks, daemonVerdict, checkDaemons,
// the .beads check of checkConfiguration, renderDoctorTUITo,
// newDoctorContentionCmd) and
// internal/invariants/invariants.go (the six invariants' ids and names,
// checkGracefulDegradation's rule, the unverified status) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local changes:
// the operator's ruling (2026-10-24, nhm-2p6.6): each section maps onto
// nhm's own evidence. herdr is the dependency in tmux's place; nhm's setup
// checks (doctor.go) are its Configuration, with nhm's state directory in
// ~/.ntm's place; Agent Mail's daemon is its health check; the tools are
// found and their versions read as nhm deps reads them, without ntm's
// per-tool health and capability probes; the safety defaults and the
// invariants read nhm's guards, store and herdr, and the ones with no nhm
// evidence are unverified. --socket, --no-mail and --herdr-fit stay nhm's.
// contention scans through internal/core/contention, and prints each cwd
// and command on one line, without control characters.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/contention"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// DoctorReport is ntm's doctor report.
type DoctorReport struct {
	Timestamp      time.Time        `json:"timestamp"`
	Overall        string           `json:"overall"` // "healthy", "warning", "unhealthy"
	SafetyDefaults SafetyDefaults   `json:"safety_defaults"`
	Tools          []ToolCheck      `json:"tools"`
	Dependencies   []DepCheck       `json:"dependencies"`
	Daemons        []DaemonCheck    `json:"daemons"`
	Configuration  []ConfigCheck    `json:"configuration"`
	Invariants     []InvariantCheck `json:"invariants"`
	Warnings       int              `json:"warnings"`
	Errors         int              `json:"errors"`
}

// SafetyDefaults are the security and privacy defaults in effect.
type SafetyDefaults struct {
	RedactionMode              string   `json:"redaction_mode"`
	RedactionAllowlistEnabled  bool     `json:"redaction_allowlist_enabled"`
	RedactionAllowlistCount    int      `json:"redaction_allowlist_count"`
	PrivacyDefaultEnabled      bool     `json:"privacy_default_enabled"`
	EncryptionAtRestEnabled    bool     `json:"encryption_at_rest_enabled"`
	PreflightDefaultStrict     bool     `json:"preflight_default_strict"`
	ClaudeAgentHooks           []string `json:"claude_agent_hooks"`
	ClaudePolicyHookRegistered bool     `json:"claude_policy_hook_registered"`
	ClaudeAgentHooksWarning    string   `json:"claude_agent_hooks_warning,omitempty"`
}

// InvariantCheck is one design invariant's result.
type InvariantCheck struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Status  string   `json:"status"` // "ok", "warning", "error", "unverified"
	Message string   `json:"message,omitempty"`
	Details []string `json:"details,omitempty"`
}

// ToolCheck is one tool's result.
type ToolCheck struct {
	Name         string   `json:"name"`
	Installed    bool     `json:"installed"`
	Version      string   `json:"version,omitempty"`
	Path         string   `json:"path,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Required     bool     `json:"required"`
	Status       string   `json:"status"`
	Message      string   `json:"message,omitempty"`
}

// DepCheck is one dependency's result.
type DepCheck struct {
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`
	Version    string `json:"version,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
}

// DaemonCheck is one daemon's result.
type DaemonCheck struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ConfigCheck is one configuration check's result.
type ConfigCheck struct {
	Name    string `json:"name"`
	Valid   bool   `json:"valid"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// doctorExitFailure: only an unhealthy report exits non-zero, in text and
// JSON alike; warnings never do.
func doctorExitFailure(overall string) bool { return overall == "unhealthy" }

func newDoctorCmd() *cobra.Command {
	var verbose, noMail, fit, placements bool
	var socket string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate the nhm ecosystem health",
		Long: `Validates that all ecosystem tools and dependencies are properly installed
and configured. Checks:

  - Tool detection (bv, br, am, cm, cass, s2p and the other flywheel tools)
  - Dependencies (herdr in tmux's place, go, and the optional grok and omp)
  - Daemon health (Agent Mail, the cm server)
  - Configuration (nhm's state, enrollment, database, owner, maintenance,
    identity files, kinds and guards)
  - Safety defaults and design invariants

This command helps diagnose issues before spawning sessions.`,
		// No Args, as ntm's: an extra word that is no sub-verb is ignored.
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if fit || placements {
				legacy := []string{}
				if fit {
					legacy = append(legacy, "--herdr-fit")
				}
				if placements {
					legacy = append(legacy, "--placements")
				}
				if noMail {
					legacy = append(legacy, "--no-mail")
				}
				if socket != "" {
					legacy = append(legacy, "--socket", socket)
				}
				if wantJSON(cmd) {
					legacy = append(legacy, "--json")
				}
				if code := runDoctor(legacy, out, cmd.ErrOrStderr()); code != exitOK {
					return verbExit{exitError}
				}
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			report := doctorReport(ctx, socket, !noMail)
			if wantJSON(cmd) {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
				if doctorExitFailure(report.Overall) {
					// The report is the failure: no second JSON document.
					return verbExit{exitError}
				}
				return nil
			}
			renderDoctorReport(out, report, verbose)
			if doctorExitFailure(report.Overall) {
				return errors.New("ecosystem is unhealthy")
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&verbose, "verbose", "v", false, "Show detailed output")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	fl.BoolVar(&noMail, "no-mail", false, "skip the Agent Mail check (nhm)")
	fl.BoolVar(&fit, "herdr-fit", false, "check that the running herdr fits nhm's interactive surfaces (nhm)")
	fl.BoolVar(&placements, "placements", false, "with --herdr-fit: also open a tab, a split and an overlay (sandbox, CI; nhm)")
	cmd.AddCommand(newDoctorContentionCmd())
	return cmd
}

// contentionScanner reads the processes of doctor contention (ntm's
// newDoctorContentionCmd); tests give a fake /proc.
var contentionScanner = contention.DefaultScanner

func newDoctorContentionCmd() *cobra.Command {
	var terminatePID int
	var confirm, force bool
	cmd := &cobra.Command{
		Use:   "contention",
		Short: "Find processes holding shared build and Beads resources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			holders, err := contentionScanner().Holders(ctx, cwd)
			if err != nil {
				return err
			}
			if terminatePID > 0 {
				if err := contention.Terminate(holders, terminatePID, confirm, force); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			if wantJSON(cmd) {
				return json.NewEncoder(out).Encode(struct {
					Holders []contention.Holder `json:"holders"`
				}{Holders: holders})
			}
			if len(holders) == 0 {
				fmt.Fprintln(out, "No shared-resource contention detected.")
				return nil
			}
			fmt.Fprintln(out, "PID\tAGE\tRESOURCE\tIN_SWARM\tCWD\tCOMMAND")
			for _, h := range holders {
				fmt.Fprintf(out, "%d\t%s\t%s\t%t\t%s\t%s\n", h.PID, h.Age, h.Resource, h.InSwarm, contentionCell(h.CWD), contentionCell(h.Command))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&terminatePID, "terminate-pid", 0, "Send SIGTERM to a listed PID (requires --confirm)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm termination of --terminate-pid")
	cmd.Flags().BoolVar(&force, "force", false, "Allow termination of an in-swarm PID (requires --confirm)")
	return cmd
}

// contentionCell is a holder's cwd or command made safe for one cell of
// the table: no control characters, and a newline or tab as a space.
func contentionCell(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, stripControl(s))
}

// doctorStatus is ntm's status word of one of nhm's check results.
func doctorStatus(result string) string {
	switch result {
	case checkOK:
		return "ok"
	case checkWarn:
		return "warning"
	case checkSkip:
		return "unknown" // not checked; its detail says why
	default:
		return "error"
	}
}

// doctorTools are ntm's registered tools, in its order: bv and br are
// required; the others are optional. bd is br first, as nhm deps looks.
var doctorTools = []struct {
	name     string
	commands []string
	required bool
}{
	{"bv", []string{"bv"}, true}, {"br", []string{"br", "bd"}, true}, {"am", []string{"mcp-agent-mail", "am"}, false},
	{"cm", []string{"cm"}, false}, {"cass", []string{"cass"}, false}, {"s2p", []string{"s2p"}, false},
	{"jfp", []string{"jfp"}, false}, {"dcg", []string{"dcg"}, false}, {"slb", []string{"slb"}, false},
	{"acfs", []string{"acfs"}, false}, {"ru", []string{"ru"}, false}, {"ms", []string{"ms"}, false},
	{"xf", []string{"xf"}, false}, {"giil", []string{"giil"}, false}, {"ubs", []string{"ubs"}, false},
	{"caam", []string{"caam"}, false}, {"rch", []string{"rch"}, false}, {"rano", []string{"rano"}, false},
	{"caut", []string{"caut"}, false}, {"pt", []string{"pt"}, false}, {"rust_proxy", []string{"rust_proxy"}, false},
}

// doctorToolChecks finds each tool, at once. nhm's health of a tool is its
// --version: one that exits 0 is ok, with its first line as its version;
// one that fails is ntm's "health check failed".
func doctorToolChecks(ctx context.Context) []ToolCheck {
	out := make([]ToolCheck, len(doctorTools))
	var wg sync.WaitGroup
	for i, t := range doctorTools {
		out[i] = ToolCheck{Name: t.name, Required: t.required}
		wg.Add(1)
		go func(c *ToolCheck, commands []string) {
			defer wg.Done()
			for _, name := range commands {
				if p, err := exec.LookPath(name); err == nil {
					c.Installed, c.Path = true, p
					break
				}
			}
			switch {
			case c.Installed:
				if v, err := runVersion(ctx, c.Path, []string{"--version"}, true); err == nil {
					c.Version, c.Status, c.Message = v, "ok", v
				} else {
					c.Status, c.Message = "warning", "health check failed"
				}
			case c.Required:
				c.Status, c.Message = "error", "required tool not found"
			default:
				c.Status, c.Message = "warning", "optional tool not found"
			}
		}(&out[i], t.commands)
	}
	wg.Wait()
	return out
}

func doctorDependencies(ctx context.Context, nhm []doctorCheck) []DepCheck {
	var out []DepCheck
	// herdr in tmux's place: installed, and its server on nhm's protocol.
	h := DepCheck{Name: "herdr", MinVersion: fmt.Sprintf("protocol %d", herdr.Protocol)}
	if p, err := exec.LookPath(herdrBin()); err == nil {
		h.Installed = true
		h.Version = depVersion(ctx, p, []string{"--version"}, true)
	}
	h.Status = "error"
	for _, c := range nhm {
		if c.Name == "herdr" {
			h.Status = doctorStatus(c.Result)
			if h.Status != "ok" {
				h.Message = withFix(c.Detail, c.Fix)
			}
		}
	}
	if !h.Installed && h.Message == "" {
		h.Message = "herdr is required for nhm"
	}
	out = append(out, h)

	g := DepCheck{Name: "go", MinVersion: "1.25"}
	if path, err := exec.LookPath("go"); err == nil {
		g.Installed = true
		cmd := exec.CommandContext(ctx, path, "version")
		cmd.WaitDelay = 2 * time.Second
		if o, err := cmd.Output(); err == nil {
			if parts := strings.Fields(string(o)); len(parts) >= 3 && parts[0] == "go" && parts[1] == "version" {
				g.Version = parts[2]
			} else {
				g.Version = "unknown"
			}
			g.Status = "ok"
		} else {
			g.Status, g.Message = "warning", "version check failed"
		}
	} else {
		g.Status, g.Message = "warning", "not found (needed for plugins)"
	}
	out = append(out, g)

	// Grok Build and Oh My Pi are optional: absence is healthy.
	for _, name := range []string{"grok", "omp"} {
		d := DepCheck{Name: name, Status: "ok"}
		if p, err := exec.LookPath(name); err == nil {
			d.Installed, d.Version = true, depVersion(ctx, p, []string{"--version"}, false)
		} else {
			d.Message = "optional agent not found"
		}
		out = append(out, d)
	}
	return out
}

// daemonVerdict is ntm's: a running daemon is ok; one that should run is a
// warning; any other is unknown.
func daemonVerdict(running, expected bool, port int) (status, message string) {
	switch {
	case running:
		return "ok", fmt.Sprintf("listening on port %d", port)
	case expected:
		return "warning", fmt.Sprintf("enabled in config but not listening on port %d", port)
	default:
		return "unknown", fmt.Sprintf("not running (port %d free)", port)
	}
}

func doctorDaemons(ctx context.Context, nhm []doctorCheck, withMail bool) []DaemonCheck {
	am := DaemonCheck{Name: "agent-mail", Port: 8765}
	if u, err := url.Parse(agentmail.DefaultURL()); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			am.Port = p
		}
	}
	switch {
	case !withMail:
		am.Status, am.Message = "unknown", "not checked (--no-mail)"
	default:
		am.Status, am.Message = "unknown", "not checked"
		for _, c := range nhm {
			if c.Name != "agent mail" {
				continue
			}
			am.Running = c.Result == checkOK
			// nhm needs Agent Mail: a down one is a warning.
			am.Status, am.Message = daemonVerdict(am.Running, true, am.Port)
			if !am.Running {
				am.Message = withFix(c.Detail, c.Fix)
			}
		}
	}
	cmd := DaemonCheck{Name: "cm-server", Port: 8766}
	dialer := &net.Dialer{Timeout: time.Second}
	if conn, err := dialer.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", cmd.Port)); err == nil {
		_ = conn.Close()
		cmd.Running = true
	}
	cmd.Status, cmd.Message = daemonVerdict(cmd.Running, false, cmd.Port)
	return []DaemonCheck{am, cmd}
}

func withFix(detail, fix string) string {
	if fix == "" {
		return detail
	}
	return detail + "; fix: " + fix
}

func doctorConfiguration(nhm []doctorCheck) []ConfigCheck {
	var out []ConfigCheck
	// nhm's state directory, in ~/.ntm's place.
	sd := ConfigCheck{Name: "nhm state directory"}
	if root, err := store.StateRoot(); err != nil {
		sd.Status, sd.Message = "error", "cannot determine the state directory"
	} else if info, err := os.Stat(root); err == nil && info.IsDir() {
		if f, err := os.CreateTemp(root, ".doctor_test"); err == nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			sd.Valid, sd.Status, sd.Message = true, "ok", "exists and writable"
		} else {
			sd.Status, sd.Message = "error", "not writable"
		}
	} else {
		sd.Status, sd.Message = "warning", "does not exist (will be created)"
	}
	out = append(out, sd)
	if _, err := os.Stat(".beads"); err == nil {
		out = append(out, ConfigCheck{Name: ".beads directory", Valid: true, Status: "ok", Message: "project has beads initialized"})
	} else {
		out = append(out, ConfigCheck{Name: ".beads directory", Status: "warning", Message: "no beads in current directory"})
	}
	for _, c := range nhm {
		if c.Name == "herdr" || c.Name == "agent mail" {
			continue // Dependencies and Daemons
		}
		cc := ConfigCheck{Name: c.Name, Valid: c.Result == checkOK, Status: doctorStatus(c.Result), Message: c.Detail}
		if !cc.Valid {
			cc.Message = withFix(c.Detail, c.Fix)
		}
		out = append(out, cc)
	}
	return out
}

// doctorSafetyDefaults are nhm's: the redaction mode of mail send's scan
// (ntm's default, warn, as nhm has no [redaction] config yet; nhm send
// does not scan the operator's prompt), no privacy mode, encryption at
// rest or strict preflight, and the guards nhm puts on Claude Code.
func doctorSafetyDefaults(ctx context.Context) SafetyDefaults {
	sd := SafetyDefaults{RedactionMode: "warn", ClaudeAgentHooks: []string{}}
	env := guards.Env{}
	if s, err := projectScope(ctx, "", ""); err == nil {
		env.ProjectRoot = s.ProjectRoot
	}
	for _, r := range guards.Check("claude", env) {
		if r.State == guards.Configured || r.State == guards.VerifiedForLaunch {
			sd.ClaudeAgentHooks = append(sd.ClaudeAgentHooks, r.Guard)
		}
	}
	if len(sd.ClaudeAgentHooks) == 0 {
		sd.ClaudeAgentHooksWarning = "no guard is configured for Claude Code (nhm doctor's guard claude lines say what to do)"
	}
	return sd
}

// The six invariants, ntm's ids and names, on nhm's evidence.
func doctorInvariants(r *DoctorReport, nhm []doctorCheck) []InvariantCheck {
	result := func(name string) (doctorCheck, bool) {
		for _, c := range nhm {
			if c.Name == name {
				return c, true
			}
		}
		return doctorCheck{}, false
	}
	var out []InvariantCheck

	// No Silent Data Loss: dcg guards the shell of every kind nhm spawns.
	// The guard's state decides: a kind that cannot take dcg is unguarded,
	// though its doctor line is ok.
	nsdl := InvariantCheck{ID: "no_silent_data_loss", Name: "No Silent Data Loss"}
	var unguarded []string
	for _, k := range kinds.Names() {
		c, ok := result("guard " + k + " dcg")
		if !ok {
			continue
		}
		nsdl.Details = append(nsdl.Details, fmt.Sprintf("dcg for %s: %s", k, c.Detail))
		if c.guard != guards.Configured && c.guard != guards.VerifiedForLaunch {
			unguarded = append(unguarded, k)
		}
	}
	switch {
	case len(nsdl.Details) == 0:
		nsdl.Status, nsdl.Message = "unverified", "no guard evidence available to check"
	case len(unguarded) > 0:
		nsdl.Status, nsdl.Message = "warning", "dcg does not guard: "+strings.Join(unguarded, ", ")
	default:
		nsdl.Status, nsdl.Message = "ok", "dcg guards every agent kind"
	}
	out = append(out, nsdl)

	// Graceful Degradation: every unavailable tool is reported (ntm's rule).
	gd := InvariantCheck{ID: "graceful_degradation", Name: "Graceful Degradation"}
	var unavailable, silent []string
	for _, t := range r.Tools {
		if t.Installed {
			continue
		}
		unavailable = append(unavailable, t.Name)
		if t.Status == "ok" || t.Message == "" {
			silent = append(silent, t.Name)
		}
	}
	gd.Details = append(gd.Details, fmt.Sprintf("%d external tool(s) registered, %d unavailable", len(r.Tools), len(unavailable)))
	if len(unavailable) > 0 {
		gd.Details = append(gd.Details, "unavailable and still running: "+strings.Join(unavailable, ", "))
	}
	switch {
	case len(silent) > 0:
		gd.Details = append(gd.Details, "degraded without a warning: "+strings.Join(silent, ", "))
		gd.Status, gd.Message = "error", "tools unavailable without any warning: "+strings.Join(silent, ", ")
	case len(unavailable) == 0:
		gd.Status, gd.Message = "ok", fmt.Sprintf("all %d external tools available", len(r.Tools))
	default:
		gd.Status, gd.Message = "ok", fmt.Sprintf("%d of %d tools unavailable, each reported", len(unavailable), len(r.Tools))
	}
	out = append(out, gd)

	// Idempotent Orchestration: nhm's store keys each send by its
	// operation id, so a retry with the same id does not send twice.
	idem := InvariantCheck{ID: "idempotent_orchestration", Name: "Idempotent Orchestration"}
	if c, ok := result("database"); ok && c.Result == checkOK {
		idem.Status, idem.Message = "ok", "send idempotency constraint present"
		idem.Details = []string{"nhm's store keys each operation by its op id (" + c.Detail + ")"}
	} else {
		idem.Status, idem.Message = "unverified", "state schema could not be inspected"
	}
	out = append(out, idem)

	// Recoverable State: herdr keeps the sessions; nhm's bindings and
	// nhm admin recover find their agents again.
	rs := InvariantCheck{ID: "recoverable_state", Name: "Recoverable State"}
	if c, ok := result("herdr"); ok && c.Result == checkOK {
		rs.Status, rs.Message = "ok", "state recovery mechanisms available"
		rs.Details = []string{"herdr serves the sessions (" + c.Detail + "); they survive nhm's process"}
	} else {
		rs.Status, rs.Message = "error", "herdr not available, sessions cannot be recovered"
	}
	out = append(out, rs)

	// No nhm evidence yet.
	out = append(out,
		InvariantCheck{ID: "auditable_actions", Name: "Auditable Actions", Status: "unverified",
			Message: "not checked on nhm (deferred: nhm-2p6.19)"},
		InvariantCheck{ID: "safe_by_default", Name: "Safe-by-Default", Status: "unverified",
			Message: "not checked on nhm (deferred: nhm-2p6.20)"})
	return out
}

// doctorReport is ntm's report over nhm's checks.
func doctorReport(ctx context.Context, socket string, withMail bool) *DoctorReport {
	nhm := doctorChecks(ctx, socket, withMail)
	r := &DoctorReport{Timestamp: time.Now(), Overall: "healthy"}
	r.SafetyDefaults = doctorSafetyDefaults(ctx)
	r.Tools = doctorToolChecks(ctx)
	r.Dependencies = doctorDependencies(ctx, nhm)
	r.Daemons = doctorDaemons(ctx, nhm, withMail)
	r.Configuration = doctorConfiguration(nhm)
	r.Invariants = doctorInvariants(r, nhm)
	count := func(status string) {
		switch status {
		case "error":
			r.Errors++
		case "warning":
			r.Warnings++
		}
	}
	for _, t := range r.Tools {
		count(t.Status)
	}
	for _, d := range r.Dependencies {
		count(d.Status)
	}
	for _, d := range r.Daemons {
		count(d.Status)
	}
	for _, c := range r.Configuration {
		count(c.Status)
	}
	for _, i := range r.Invariants {
		count(i.Status)
	}
	switch {
	case r.Errors > 0:
		r.Overall = "unhealthy"
	case r.Warnings > 0:
		r.Overall = "warning"
	}
	return r
}

// renderDoctorReport is ntm's doctor text.
func renderDoctorReport(w io.Writer, r *DoctorReport, verbose bool) {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("99"))
	section := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75")).MarginTop(1)
	okS := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnS := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errS := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1).MarginTop(1)
	icon := func(s string) string {
		switch s {
		case "ok":
			return okS.Render("✓")
		case "warning":
			return warnS.Render("⚠")
		case "error":
			return errS.Render("✗")
		default:
			return muted.Render("?")
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, title.Render("NHM Doctor"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, section.Render("Tools:"))
	for _, t := range r.Tools {
		msg := muted.Render(t.Message)
		if !t.Installed && t.Required {
			msg = errS.Render(t.Message)
		}
		caps := ""
		if t.Installed && verbose && len(t.Capabilities) > 0 {
			caps = fmt.Sprintf(" (%v)", t.Capabilities)
		}
		fmt.Fprintf(w, "  %s %s %s%s\n", icon(t.Status), t.Name, msg, caps)
	}
	fmt.Fprintln(w, section.Render("Dependencies:"))
	for _, d := range r.Dependencies {
		version := ""
		if d.Version != "" {
			version = muted.Render(fmt.Sprintf(" (%s)", d.Version))
		}
		fmt.Fprintf(w, "  %s %s%s\n", icon(d.Status), d.Name, version)
		if d.Status != "ok" && d.Message != "" {
			// nhm's: why herdr fails, and what to do.
			fmt.Fprintf(w, "      %s\n", muted.Render(d.Message))
		}
	}
	fmt.Fprintln(w, section.Render("Daemons:"))
	for _, d := range r.Daemons {
		fmt.Fprintf(w, "  %s %s %s\n", icon(d.Status), d.Name, muted.Render(d.Message))
	}
	fmt.Fprintln(w, section.Render("Configuration:"))
	for _, c := range r.Configuration {
		fmt.Fprintf(w, "  %s %s %s\n", icon(c.Status), c.Name, muted.Render(c.Message))
	}
	fmt.Fprintln(w, section.Render("Safety Defaults:"))
	sd := r.SafetyDefaults
	redaction := icon("ok")
	if sd.RedactionMode == "off" {
		redaction = icon("warning")
	}
	allowlist := "none"
	if sd.RedactionAllowlistEnabled {
		allowlist = fmt.Sprintf("%d entries", sd.RedactionAllowlistCount)
	}
	fmt.Fprintf(w, "  %s Redaction mode: %s (allowlist: %s)\n", redaction, muted.Render(sd.RedactionMode), muted.Render(allowlist))
	onOff := func(b bool, on, off string) string {
		if b {
			return on
		}
		return off
	}
	fmt.Fprintf(w, "  %s Privacy default: %s\n", icon("ok"), muted.Render(onOff(sd.PrivacyDefaultEnabled, "enabled", "disabled")))
	fmt.Fprintf(w, "  %s Encryption at rest: %s\n", icon("ok"), muted.Render(onOff(sd.EncryptionAtRestEnabled, "enabled", "disabled")))
	fmt.Fprintf(w, "  %s Prompt preflight: %s\n", icon("ok"), muted.Render(onOff(sd.PreflightDefaultStrict, "strict=on", "strict=off")))
	hooks, label := icon("ok"), ""
	switch {
	case sd.ClaudeAgentHooksWarning != "":
		hooks, label = icon("warning"), "unprotected: "+sd.ClaudeAgentHooksWarning
	default:
		label = strings.Join(sd.ClaudeAgentHooks, ", ") + " (nhm's guards)"
	}
	fmt.Fprintf(w, "  %s Claude agent hooks: %s\n", hooks, muted.Render(label))
	fmt.Fprintln(w, section.Render("Design Invariants:"))
	for _, i := range r.Invariants {
		fmt.Fprintf(w, "  %s %s %s\n", icon(i.Status), i.Name, muted.Render(i.Message))
		if verbose {
			for _, d := range i.Details {
				fmt.Fprintf(w, "      %s\n", muted.Render(d))
			}
		}
	}
	var msg string
	style := okS
	switch r.Overall {
	case "healthy":
		msg = "Overall: HEALTHY"
	case "warning":
		msg, style = fmt.Sprintf("Overall: HEALTHY (%d warnings)", r.Warnings), warnS
	case "unhealthy":
		msg, style = fmt.Sprintf("Overall: UNHEALTHY (%d errors, %d warnings)", r.Errors, r.Warnings), errS
	}
	fmt.Fprintln(w, box.Render(style.Render(msg)))
	fmt.Fprintln(w)
}
