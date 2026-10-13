package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

// Recycle stages, reported when one fails.
const (
	StageCheck    = "check"
	StagePreserve = "preserve"
	StageHandoff  = "handoff"
	StageStop     = "stop"
	StageStart    = "start"
	StageKickoff  = "kickoff"
	StageDone     = "done"
)

// stopWait bounds the wait for the old agent to exit.
const stopWait = 30 * time.Second

// readyWait bounds the wait for the successor to be ready.
const readyWait = 60 * time.Second

// RecycleRequest names the agent and its kickoff.
type RecycleRequest struct {
	Target     string
	ProjectKey string
	// Template is a template or palette key; Prompt is the kickoff text;
	// with neither, ACFS's default_new_agent prompt.
	Template, Prompt string
	// BeadID fills the kickoff template's bead_* placeholders, as send's
	// --bead does. Without it, a template that has them takes the agent's
	// bead in progress: the one the handoff reads.
	BeadID      string
	NoHandoff   bool
	OperationID string
	// WaitTurn, when not 0, is how long recycle waits for the agent to end
	// its turn (idle or done) before its check: nhm recycle --self, where
	// the agent asks for its own recycle as its last step (nhm-abn). 0
	// refuses a working agent at once.
	WaitTurn time.Duration
	// ExpectedGeneration is the generation the caller reviewed; 0 is
	// unset. When set, recycle stops at its check, under the binding's
	// lock and before any effect, when the binding is at another one.
	ExpectedGeneration int64
}

// RecycleResult is how far a recycle got. HandoffBead is the bead in
// progress the handoff is about: the one with the agent's newest handoff
// comment, else the first.
type RecycleResult struct {
	OperationID   string     `json:"operation_id,omitempty"`
	MailName      string     `json:"mail_name"`
	BindingID     string     `json:"binding_id"`
	PaneID        string     `json:"pane_id"`
	Generation    int64      `json:"generation"`
	Stage         string     `json:"stage"`
	Error         string     `json:"error,omitempty"`
	Transcript    *Preserved `json:"transcript,omitempty"`
	Handoff       *Handoff   `json:"handoff,omitempty"`
	HandoffBead   string     `json:"handoff_bead,omitempty"`
	KickoffOp     string     `json:"kickoff_operation,omitempty"`
	KickoffStatus string     `json:"kickoff_submission,omitempty"`
	Warnings      []string   `json:"warnings"`
}

// Recycle gives the same agent a fresh session in the same pane, with the
// same name, tab and slot, and a handoff (plan-v8 2.10). The binding's
// lock is held from the check to the successor's start, so sends and
// wakes skip the agent, and the old agent is stopped before the new one
// starts: one owner of the identity at every moment. On a failure the
// stage is reported, the recovery material is kept, and nothing starts
// twice.
func Recycle(ctx context.Context, env Env, req RecycleRequest) (RecycleResult, error) {
	res := RecycleResult{Stage: StageCheck, Warnings: []string{}}
	// A retry of an operation reports its outcome and starts nothing.
	if op, ok, err := priorOperation(ctx, env, req.OperationID); err != nil {
		return res, err
	} else if ok {
		res.OperationID = op.ID
		if op.Status == "done" {
			res.Stage = StageDone
		}
		return res, replayed(op)
	}
	b, err := find(ctx, env, req.Target, req.ProjectKey)
	if err != nil {
		return res, err
	}
	res.MailName, res.BindingID, res.PaneID, res.Generation = b.MailName, b.ID, b.PaneID, b.Generation
	fail := func(stage string, err error) (RecycleResult, error) {
		res.Stage, res.Error = stage, err.Error()
		if res.OperationID != "" {
			_ = env.DB.SetOperationStatus(ctx, res.OperationID, "failed_at_"+stage, true, env.now())
		}
		return res, fmt.Errorf("recycle stopped at %s: %w", stage, err)
	}
	a := kinds.Lookup(b.Kind)
	// A bad kickoff is refused before the wait. Without --bead, it is
	// rendered again under the lock, from the agent's beads as they are
	// then.
	kick, err := recycleStatic(ctx, env, req, b)
	if err != nil {
		return fail(StageCheck, err)
	}
	if env.Caller != "" && b.PaneID == env.Caller {
		return fail(StageCheck, fmt.Errorf("%s runs in this pane: stopping it would stop this recycle (nhm recycle --self runs it as a job of its own)", b.MailName))
	}
	if err := waitTurn(ctx, env, req, b); err != nil {
		return fail(StageCheck, err)
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	unlock, err := send.LockTarget(lctx, env.InstanceDir, b.ID)
	cancel()
	if err != nil {
		return fail(StageCheck, fmt.Errorf("%s is being sent to or recycled", b.MailName))
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	// Under the lock, the binding as it is now.
	if b, err = env.Bindings.Get(ctx, b.ID); err != nil {
		return fail(StageCheck, err)
	}
	res.PaneID, res.Generation = b.PaneID, b.Generation
	if why := generationRefusal(b, req.ExpectedGeneration); why != "" {
		return fail(StageCheck, errors.New(why))
	}
	if b.Status != bindings.Active {
		return fail(StageCheck, fmt.Errorf("%s is %s", b.MailName, b.Status))
	}
	// An agent that already exited leaves its pane at the shell: recycle
	// skips the stop and starts the successor there (nhm-lsa). A pane that
	// is gone, or that runs another program, is refused.
	agent, err := env.Herdr.AgentGet(ctx, b.PaneID)
	exited := false
	switch {
	case noAgent(err):
		// herdr does not list a stopped (ctrl+z) or background agent
		// either: a job of the shell that may still be it refuses.
		shell, why := atShell(ctx, env, b.PaneID)
		if why == "" {
			why = jobLeft(env, shell, b.PaneID, a.Name)
		}
		if why != "" {
			return fail(StageCheck, fmt.Errorf("herdr: %w, and %s", err, why))
		}
		exited = true
		res.Warnings = append(res.Warnings, b.MailName+" had already exited; recycle starts its successor without a stop")
	case err != nil:
		return fail(StageCheck, fmt.Errorf("herdr: %w", err))
	case agent.AgentStatus != herdr.StatusIdle && agent.AgentStatus != herdr.StatusDone:
		return fail(StageCheck, fmt.Errorf("%s is %s; an agent is recycled only between turns (interrupt it first)", b.MailName, agent.AgentStatus))
	case agent.Agent != "" && agent.Agent != b.Kind:
		return fail(StageCheck, fmt.Errorf("pane %s holds a %s agent; the binding is %s", b.PaneID, agent.Agent, b.Kind))
	}
	// The agent's beads as they are after the wait, read at most once:
	// the kickoff's bead_* placeholders (without --bead) and the handoff
	// take the same bead.
	beads := sync.OnceValues(assignedReader(ctx, env, b))
	if req.BeadID == "" {
		// When that render fails, the kickoff checked before the wait
		// stands: a --self recycle that stopped here would tell nobody.
		if again, err := recycleKickoff(ctx, env, req, b, beads); err == nil {
			kick = again
		} else {
			res.Warnings = append(res.Warnings, "the kickoff keeps the bead read before the wait: "+err.Error())
		}
	}
	claimed := map[string]any{"binding_id": b.ID, "generation": b.Generation, "template": req.Template, "prompt": req.Prompt, "no_handoff": req.NoHandoff}
	if req.BeadID != "" {
		claimed["bead_id"] = req.BeadID
	}
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: "recycle",
		Scope: map[string]string{"binding_id": b.ID}, Request: claimed}, env.now())
	if err != nil {
		return fail(StageCheck, err)
	}
	if claim.Existing {
		res.OperationID = claim.Operation.ID
		return res, replayed(claim.Operation)
	}
	res.OperationID = claim.Operation.ID

	// Preserve the transcript and the native resume information (the
	// transcripts row keeps the session id). This copy is the recovery
	// material if the stop fails; the copy after the exit is the final
	// one, and the one CASS indexes.
	res.Stage = StagePreserve
	original := ""
	if p, err := Locate(env.Home, a, b.NativeSession); err == nil {
		original = p
		if pr, err := Preserve(ctx, env.DB, env.InstanceDir, b, p, "recycle", env.now()); err == nil {
			res.Transcript = &pr
		} else {
			res.Warnings = append(res.Warnings, "transcript not preserved: "+err.Error())
		}
	} else {
		res.Warnings = append(res.Warnings, "transcript: "+err.Error())
	}

	res.Stage = StageHandoff
	if !req.NoHandoff && env.Handoff != nil {
		var bead *Bead
		if list, err := beads(); err == nil {
			bead = handoffBead(list)
			for _, x := range list {
				if x.HandoffUndated {
					res.Warnings = append(res.Warnings, "beads: a handoff comment on "+x.ID+" has a time that does not read; the newest handoff may be on another bead")
				}
			}
		} else {
			res.Warnings = append(res.Warnings, "beads: "+err.Error())
		}
		h := env.Handoff(ctx, b, bead, original)
		res.Handoff = &h
		if bead != nil {
			res.HandoffBead = bead.ID
		}
	}

	// Stop the old agent, then wait for the shell prompt and the cleared
	// alias. An agent that already exited needs no stop.
	res.Stage = StageStop
	if !exited {
		if _, err := env.Herdr.AgentPrompt(ctx, herdr.AgentPromptParams{Target: b.PaneID, Text: a.ExitCommand}); err != nil &&
			herdr.ErrorCode(err) != herdr.CodeAgentPromptStalled {
			return fail(StageStop, fmt.Errorf("typing %s: %w", a.ExitCommand, err))
		}
		if !waitExit(ctx, env, b) {
			return fail(StageStop, fmt.Errorf("%s did not exit within %s; nothing started, the pane is as it was", b.MailName, stopWait))
		}
	}
	// Providers write the last turn on exit: the final copy, indexed.
	if original != "" {
		if pr, err := Preserve(ctx, env.DB, env.InstanceDir, b, original, "recycle-exit", env.now()); err == nil {
			res.Transcript = &pr
			index(ctx, env, b, original, &res.Warnings)
		} else {
			res.Warnings = append(res.Warnings, "the final transcript copy: "+err.Error()+"; the copy before the exit stays")
		}
	}

	// Start the successor in the same pane, under the same name, in a
	// fresh context, and bump the generation.
	res.Stage = StageStart
	argv := spawn.LaunchArgv(a, b.Model, b.Effort, env.LaunchSettings)
	ms := readyWait.Milliseconds()
	alias := b.HerdrAlias
	if alias == "" {
		alias = strings.ToLower(b.MailName)
	}
	if _, err := env.Herdr.AgentStart(ctx, herdr.AgentStartParams{Name: alias, Kind: a.Name, PaneID: b.PaneID, Args: argv[1:], TimeoutMS: &ms}); err != nil {
		return fail(StageStart, err)
	}
	info, err := env.Herdr.AgentWait(ctx, herdr.AgentWaitParams{Target: b.PaneID,
		Until: []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone, herdr.StatusBlocked}, TimeoutMS: &ms})
	if err != nil {
		return fail(StageStart, err)
	}
	if info.AgentStatus == herdr.StatusBlocked {
		return fail(StageStart, fmt.Errorf("the successor waits at a dialog in pane %s; answer it there", b.PaneID))
	}
	occ := bindings.Occupant{WorkspaceID: b.WorkspaceID, TabID: b.TabID, PaneID: b.PaneID, HerdrAlias: alias, Kind: b.Kind,
		Model: b.Model, Effort: b.Effort, LaunchID: b.LaunchID, Evidence: []bindings.Evidence{{Kind: "recycle", Detail: "successor of generation " + fmt.Sprint(b.Generation)}}}
	if info.AgentSession != nil {
		s := info.AgentSession
		occ.NativeSession = &bindings.NativeSession{Source: s.Source, Agent: s.Agent, Kind: s.Kind, Value: s.Value}
	}
	if p, err := env.Herdr.PaneProcessInfo(ctx, b.PaneID); err == nil && len(p.ForegroundProcesses) > 0 {
		occ.ForegroundPID = int64(p.ForegroundProcesses[0].PID)
		if env.ProcStart != nil {
			occ.ForegroundStart, _ = env.ProcStart(occ.ForegroundPID)
		}
	}
	nb, err := env.Bindings.Occupy(ctx, b.ID, b.Generation, occ)
	if err != nil {
		return fail(StageStart, fmt.Errorf("binding: %w", err))
	}
	res.Generation = nb.Generation
	// The predecessor's marks may never have been set (a spawn that
	// stopped at a dialog, nhm-hae).
	res.Warnings = append(res.Warnings, keepMarks(ctx, env, nb)...)
	unlock()
	locked = false

	// The kickoff with the handoff, through the normal send path.
	res.Stage = StageKickoff
	if env.Send != nil {
		text := spawn.IdentityPreamble(b.MailName, b.MailProjectKey, alias, b.WorkspaceID)
		if res.Handoff != nil && res.Handoff.Text != "" {
			text += "\n\n" + res.Handoff.Text
		}
		text += "\n\n" + kick
		sr, err := env.Send(ctx, send.Request{Session: b.WorkspaceID, WorkspaceID: b.WorkspaceID, ProjectRoot: b.MailProjectKey,
			Selector: send.Selector{Names: []string{b.MailName}}, Message: text, WhenAny: true, Source: history.SourceCLI})
		if err != nil {
			return fail(StageKickoff, err)
		}
		if len(sr.Targets) > 0 {
			res.KickoffOp, res.KickoffStatus = sr.OperationID, sr.Targets[0].Status
		}
	}
	res.Stage = StageDone
	return res, env.DB.SetOperationStatus(ctx, res.OperationID, "done", true, env.now())
}

// keepMarks gives the pane of b's new occupant the marks it lacks
// (bindings.KeepMarks): herdr keeps a pane's label and tokens while one
// agent exits and the next starts, so a label that the user set stays.
// It returns the warnings.
func keepMarks(ctx context.Context, env Env, b bindings.Binding) []string {
	p, err := env.Herdr.PaneGet(ctx, b.PaneID)
	if err != nil {
		return []string{fmt.Sprintf("marks of %s not read: %v", b.PaneID, err)}
	}
	_, warnings := bindings.KeepMarks(ctx, env.Herdr, b, p)
	return warnings
}

// waitExit waits until the pane's shell holds the foreground again and
// herdr no longer lists an agent in the pane.
func waitExit(ctx context.Context, env Env, b bindings.Binding) bool {
	deadline := env.now().Add(stopWait)
	for env.now().Before(deadline) {
		_, why := atShell(ctx, env, b.PaneID)
		if _, aerr := env.Herdr.AgentGet(ctx, b.PaneID); why == "" && noAgent(aerr) {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		env.sleep(ctx, 250*time.Millisecond)
	}
	return false
}

// noAgent reports herdr's answer that a pane holds no agent: none is
// found, or it no longer runs.
func noAgent(err error) bool {
	code := herdr.ErrorCode(err)
	return code == herdr.CodeAgentNotFound || code == herdr.CodeAgentNotRunning
}

// atShell returns the pane's shell when the shell holds the foreground.
// Otherwise it says why not: the pane is gone, herdr cannot read it, or
// another program holds its foreground.
func atShell(ctx context.Context, env Env, pane string) (int, string) {
	info, err := env.Herdr.PaneProcessInfo(ctx, pane)
	switch {
	case herdr.ErrorCode(err) == herdr.CodePaneNotFound:
		return 0, "pane " + pane + " is gone"
	case err != nil:
		return 0, "pane " + pane + " cannot be read: " + err.Error()
	case info.ShellPID == nil || info.ForegroundProcessGroupID == nil || *info.ShellPID != *info.ForegroundProcessGroupID:
		return 0, "another program holds the foreground of pane " + pane
	}
	return *info.ShellPID, ""
}

// jobLeft names a job of the pane's shell that may still be the agent: a
// stopped one (ctrl+z), or one that runs the kind's command (a job in the
// background). Such an agent still owns its identity, so no successor
// may start. A leftover helper, such as an MCP server, is no such job.
// "" when there is none.
func jobLeft(env Env, shellPID int, pane, command string) string {
	jobs, err := env.jobs(shellPID)
	if err != nil {
		return "nhm cannot tell whether the agent still runs as a job of the shell in pane " + pane + ": " + err.Error()
	}
	for _, j := range jobs {
		if !j.Stopped() && !j.Runs(command) {
			continue
		}
		name := "?"
		if len(j.Argv) > 0 {
			name = filepath.Base(j.Argv[0])
		}
		return fmt.Sprintf("a job of the shell remains in pane %s: pid %d, %s (state %c); end it, then recycle",
			pane, j.PID, name, j.State)
	}
	return ""
}

// CheckRecycle runs recycle's checks that need neither the agent's turn
// nor its lock: the binding is active, nhm knows its kind's exit
// command, and the kickoff renders. nhm recycle --self runs them before
// it hands the recycle to a job of its own, so a bad -t or a missing bead
// is refused while the agent can still read the answer.
func CheckRecycle(ctx context.Context, env Env, req RecycleRequest) (bindings.Binding, error) {
	b, err := find(ctx, env, req.Target, req.ProjectKey)
	if err != nil {
		return bindings.Binding{}, err
	}
	_, err = recycleStatic(ctx, env, req, b)
	return b, err
}

// recycleStatic checks the binding and its kind and renders the kickoff.
func recycleStatic(ctx context.Context, env Env, req RecycleRequest, b bindings.Binding) (string, error) {
	switch {
	case b.Status != bindings.Active:
		return "", fmt.Errorf("%s is %s", b.MailName, b.Status)
	case kinds.Lookup(b.Kind).ExitCommand == "":
		return "", fmt.Errorf("nhm knows no exit command for %s; retire it and spawn anew", b.Kind)
	}
	return recycleKickoff(ctx, env, req, b, assignedReader(ctx, env, b))
}

// assignedReader reads the agent's beads in progress.
func assignedReader(ctx context.Context, env Env, b bindings.Binding) func() ([]Bead, error) {
	return func() ([]Bead, error) {
		if env.Facts == nil {
			return nil, errors.New("nhm has no bead facts here")
		}
		return env.Facts.Assigned(ctx, b.MailProjectKey, b.MailName)
	}
}

// waitTurn waits up to req.WaitTurn for the agent to end its turn: idle
// or done. 0 does not wait. An agent that already exited has no turn to
// wait for; the check under the lock decides about its pane.
func waitTurn(ctx context.Context, env Env, req RecycleRequest, b bindings.Binding) error {
	if req.WaitTurn <= 0 {
		return nil
	}
	ms := req.WaitTurn.Milliseconds()
	info, err := env.Herdr.AgentWait(ctx, herdr.AgentWaitParams{Target: b.PaneID,
		Until: []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone}, TimeoutMS: &ms})
	switch {
	case noAgent(err):
		return nil
	case err != nil:
		return fmt.Errorf("%s did not end its turn within %s: %w", b.MailName, req.WaitTurn, err)
	case info.AgentStatus != herdr.StatusIdle && info.AgentStatus != herdr.StatusDone:
		return fmt.Errorf("%s did not end its turn within %s (it is %s)", b.MailName, req.WaitTurn, info.AgentStatus)
	}
	return nil
}

// recycleKickoff is the successor's prompt: --prompt as typed, a
// template, or ACFS's default_new_agent. A template's bead_* placeholders
// come from --bead, else from the agent's bead in progress, which beads
// reads (nhm-e8c); a bead is read only when --bead names one or the
// template needs one.
func recycleKickoff(ctx context.Context, env Env, req RecycleRequest, b bindings.Binding, beads func() ([]Bead, error)) (string, error) {
	if req.Prompt != "" && req.Template != "" {
		return "", fmt.Errorf("give --prompt or -t, not both")
	}
	if req.Prompt != "" {
		if req.BeadID != "" {
			return "", fmt.Errorf("--bead fills a template's bead_* placeholders; give -t, not --prompt")
		}
		return req.Prompt, nil
	}
	key := req.Template
	if key == "" {
		key = "default_new_agent"
	}
	t, err := templates.NewLoader(b.MailProjectKey).Load(key)
	if err != nil {
		return "", fmt.Errorf("the kickoff: %w", err)
	}
	ec := templates.ExecutionContext{Session: b.WorkspaceID, Project: b.MailProjectKey, AgentName: b.MailName,
		HerdrName: b.HerdrAlias, Model: b.Model, AgentType: b.Kind, AgentPane: b.PaneID}
	if req.BeadID == "" {
		text, err := t.Execute(ec)
		var unresolved *templates.UnresolvedVariablesError
		if !errors.As(err, &unresolved) || !slices.ContainsFunc(unresolved.Names, func(n string) bool { return strings.HasPrefix(n, "bead_") }) {
			return text, err
		}
	}
	bead, err := kickoffBead(ctx, env, req, b, beads)
	if err != nil {
		return "", err
	}
	return t.Execute(ec.WithBead(bead.ID, bead.Title, fmt.Sprintf("P%d", bead.Priority), bead.Description, bead.Status, bead.IssueType))
}

// kickoffBead reads the bead for the kickoff's bead_* placeholders:
// --bead, else the agent's bead in progress with the newest handoff
// comment, the one the handoff reads.
func kickoffBead(ctx context.Context, env Env, req RecycleRequest, b bindings.Binding, beads func() ([]Bead, error)) (send.Bead, error) {
	id := req.BeadID
	if id == "" {
		if env.Facts == nil {
			return send.Bead{}, fmt.Errorf("the kickoff fills bead_* placeholders and nhm cannot read %s's beads: give --bead ID", b.MailName)
		}
		list, err := beads()
		if err != nil {
			return send.Bead{}, fmt.Errorf("the kickoff fills bead_* placeholders and %s's beads cannot be read: %w; give --bead ID", b.MailName, err)
		}
		if len(list) == 0 {
			return send.Bead{}, fmt.Errorf("the kickoff fills bead_* placeholders and %s has no bead in progress: give --bead ID", b.MailName)
		}
		id = handoffBead(list).ID
	}
	if env.Bead == nil {
		return send.Bead{}, fmt.Errorf("bead %s: nhm has no bead reader here", id)
	}
	bead, err := env.Bead(ctx, b.MailProjectKey, id)
	if err != nil {
		return send.Bead{}, fmt.Errorf("bead %s: %w", id, err)
	}
	return bead, nil
}
