// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/lifecycle.go (the exit timings, sendExitChoreography,
// waitForBareShell, refreshLifecyclePane, verifyLifecycleRelaunchShell,
// relaunchAgentCLI and GetExitCLI's per-pane step) and
// internal/tmux/session.go (SendOmpExitContext and its pauses) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the keys go through the send core (send.Keys) under herdr's
// names; a bare shell is herdr's process info (the shell holds the
// foreground) with no job of the agent left in the shell's session, as
// recycle reads it; the relaunch is recycle's start stage on the
// binding's launch (kind, model, effort, nhm's guard settings), under the
// target lock, where ntm replays a saved command line.

package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// ntm's exit timings. Claude Code needs the second Ctrl+C within roughly
// 0.1-0.3 s of the first to treat the pair as "exit" instead of "clear
// input"; other CLIs tolerate the same cadence.
const (
	exitDoubleTapGap = 200 * time.Millisecond
	exitTimeout      = 6 * time.Second
	exitPoll         = 250 * time.Millisecond
	exitRefreshTries = 3
	// relaunchRelease bounds the wait for herdr to release the old
	// agent's identity, ntm's lifecycleRelaunchBoot.
	relaunchRelease = 10 * time.Second
	// omp's exit: Escape ends a turn, Ctrl+C clears the draft, Ctrl+D
	// quits an empty composer.
	ompExitInterruptSettle = 500 * time.Millisecond
	ompClearVerifyWait     = 1200 * time.Millisecond
)

// ExitKeys is ntm's exit choreography for an agent of ntm type ntmType,
// in herdr's key names, with the pause before each key: Ctrl+C, 200 ms,
// Ctrl+C; omp gets its own sequence, as its Ctrl+C clears the draft
// rather than quits.
func ExitKeys(ntmType string) (keys []string, pauses []time.Duration) {
	if ntmType == "omp" {
		return []string{"esc", "ctrl+c", "ctrl+d"}, []time.Duration{0, ompExitInterruptSettle, ompClearVerifyWait}
	}
	return []string{"ctrl+c", "ctrl+c"}, []time.Duration{0, exitDoubleTapGap}
}

// ExitRequest is one pane of --robot-exit-cli.
type ExitRequest struct {
	PaneID string
	// Binding is the pane's active binding; nil for an agent that nhm
	// did not bind.
	Binding *bindings.Binding
	// Kind is the agent's kind (herdr's name, the program a job of the
	// shell would run); NTMType is its ntm type.
	Kind, NTMType string
}

// ExitOutcome is one pane's exit, as ntm reports it.
type ExitOutcome struct {
	ShellPID       int
	Exited         bool
	ShellPreserved bool
	// VerificationFailed: the pane could not be read again, so
	// ShellPreserved is unverified, not false.
	VerificationFailed bool
	Detail             string
}

// paneLookup is the outcome of reading a pane again after an action.
type paneLookup int

const (
	paneFound paneLookup = iota
	// paneAbsent: herdr answered that the pane does not exist: the only
	// answer that proves it gone.
	paneAbsent
	// paneLookupFailed: herdr could not be asked, so nothing is proven.
	paneLookupFailed
)

// ExitAgent ends the agent CLI in a pane and keeps the pane and its shell:
// ntm's GetExitCLI for one pane. A pane already at a bare shell gets no
// keys. Otherwise up to two rounds of the exit keys go through the send
// core, each followed by up to 6 s for a bare shell; then the pane is read
// again.
func ExitAgent(ctx context.Context, env Env, req ExitRequest) ExitOutcome {
	var out ExitOutcome
	if info, err := env.Herdr.PaneProcessInfo(ctx, req.PaneID); err == nil && info.ShellPID != nil {
		out.ShellPID = *info.ShellPID
	}
	if bareShell(ctx, env, req) {
		out.Exited, out.ShellPreserved = true, true
		out.Detail = "agent CLI already exited; pane is at a bare shell"
		return out
	}
	keys, pauses := ExitKeys(req.NTMType)
	kenv := send.KeysEnv{Herdr: env.Herdr, Bindings: env.Bindings, InstanceDir: env.InstanceDir}
	for attempt := 0; attempt < 2 && !out.Exited; attempt++ {
		if _, err := send.Keys(ctx, kenv, send.KeysRequest{PaneID: req.PaneID, Binding: req.Binding, Keys: keys, Pauses: pauses}); err != nil {
			out.Detail = joinDetail(out.Detail, fmt.Sprintf("exit keystrokes failed: %v", err))
			break
		}
		out.Exited = waitBareShell(ctx, env, req, exitTimeout)
	}
	info, lookup := refreshPane(ctx, env, req.PaneID)
	out.ShellPreserved = lookup == paneFound
	switch lookup {
	case paneLookupFailed:
		out.VerificationFailed = true
		out.Detail = joinDetail(out.Detail, "could not re-read the pane after exit; shell_preserved is unverified, not false")
	case paneFound:
		if !out.Exited {
			out.Detail = joinDetail(out.Detail, fmt.Sprintf(
				"agent CLI still in foreground (%s) after two graceful exit attempts; escalate with --robot-kill-agent", foreground(info)))
		}
	}
	return out
}

// bareShell reports whether the pane's shell holds the foreground and no
// job of the agent is left in its session (a stopped one either).
func bareShell(ctx context.Context, env Env, req ExitRequest) bool {
	shell, why := atShell(ctx, env, req.PaneID)
	return why == "" && jobLeft(env, shell, req.PaneID, req.Kind) == ""
}

// waitBareShell polls until the pane is at a bare shell, for at most
// timeout. A pane that herdr no longer has ends the wait; a read that
// fails is tried again until the deadline.
func waitBareShell(ctx context.Context, env Env, req ExitRequest, timeout time.Duration) bool {
	deadline := env.now().Add(timeout)
	for {
		_, err := env.Herdr.PaneProcessInfo(ctx, req.PaneID)
		switch {
		case herdr.ErrorCode(err) == herdr.CodePaneNotFound:
			return false
		case err == nil && bareShell(ctx, env, req):
			return true
		}
		if !env.now().Before(deadline) || ctx.Err() != nil {
			return false
		}
		env.sleep(ctx, exitPoll)
	}
}

// refreshPane reads the pane again; a failed read is tried again briefly,
// as the motivating case is transient.
func refreshPane(ctx context.Context, env Env, pane string) (herdr.ProcessInfo, paneLookup) {
	for attempt := 0; attempt < exitRefreshTries; attempt++ {
		if attempt > 0 {
			if ctx.Err() != nil {
				break
			}
			env.sleep(ctx, exitPoll)
		}
		info, err := env.Herdr.PaneProcessInfo(ctx, pane)
		switch {
		case err == nil:
			return info, paneFound
		case herdr.ErrorCode(err) == herdr.CodePaneNotFound:
			return herdr.ProcessInfo{}, paneAbsent
		}
	}
	return herdr.ProcessInfo{}, paneLookupFailed
}

// foreground names the pane's foreground program, as tmux's
// pane_current_command does.
func foreground(info herdr.ProcessInfo) string {
	if len(info.ForegroundProcesses) == 0 {
		return ""
	}
	return info.ForegroundProcesses[0].Name
}

func joinDetail(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + "; " + extra
}

// RelaunchCheck is ntm's prepareLifecycleRelaunch on nhm: before any agent
// exits, every pane must have what its relaunch needs, a binding (its
// launch: kind, model, effort) and, for a kind whose guard is nhm's launch
// settings, those settings.
func RelaunchCheck(env Env, reqs []ExitRequest) error {
	for _, r := range reqs {
		b := r.Binding
		if b == nil {
			return fmt.Errorf("pane %s holds an agent that nhm did not bind, so nhm has no launch to relaunch it with", r.PaneID)
		}
		a := kinds.Lookup(b.Kind)
		if a.GuardSupported && a.GuardSettings == "" && a.SettingsFlag != "" && env.LaunchSettings[a.Name] == "" {
			return fmt.Errorf("%s's agent would start without nhm's %s guard (nhm doctor names what is missing)", b.MailName, a.Name)
		}
	}
	return nil
}

// Relaunch starts the binding's agent again in its pane, after ExitAgent
// left the pane at its shell: ntm's relaunchAgentCLI on recycle's start
// stage, without a handoff or a kickoff. Under the target lock, the
// binding must still hold the pane, and the pane must be at a bare shell,
// the same shell (shellPID) as before the exit.
func Relaunch(ctx context.Context, env Env, req ExitRequest, shellPID int) (bool, string) {
	b := req.Binding
	if b == nil {
		return false, "relaunch withheld: preflighted launch specification is unavailable"
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	unlock, err := send.LockTarget(lctx, env.InstanceDir, b.ID)
	if err != nil {
		return false, "relaunch withheld: " + err.Error()
	}
	defer unlock()
	cur, err := env.Bindings.Get(ctx, b.ID)
	if err != nil || cur.Generation != b.Generation || cur.Status != bindings.Active || cur.PaneID != req.PaneID {
		return false, "relaunch withheld: original agent pane identity could not be verified"
	}
	shell, why := atShell(ctx, env, req.PaneID)
	switch {
	case why == "" && (shellPID <= 0 || shell != shellPID):
		return false, "relaunch withheld: original agent pane identity could not be verified"
	case why != "" || jobLeft(env, shell, req.PaneID, req.Kind) != "":
		return false, "relaunch withheld: pane is no longer at an idle shell"
	}
	// herdr keeps the old agent's identity, and so its name, for about a
	// second after the exit (the sandbox, 2026-10-25); a start before
	// herdr releases it is agent_name_taken. Recycle's stop stage waits
	// for the same.
	for deadline := env.now().Add(relaunchRelease); ; env.sleep(ctx, exitPoll) {
		if _, err := env.Herdr.AgentGet(ctx, req.PaneID); noAgent(err) {
			break
		}
		if !env.now().Before(deadline) || ctx.Err() != nil {
			return false, fmt.Sprintf("relaunch withheld: herdr still holds the agent of pane %s after %s", req.PaneID, relaunchRelease)
		}
	}
	_, marks, err := startInPlace(ctx, env, cur, kinds.Lookup(cur.Kind),
		bindings.Evidence{Kind: "relaunch", Detail: fmt.Sprintf("--robot-exit-cli --relaunch after generation %d", cur.Generation)})
	if err != nil {
		return false, "relaunch failed: " + err.Error()
	}
	return true, strings.Join(marks, "; ")
}
