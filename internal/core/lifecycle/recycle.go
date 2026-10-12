package lifecycle

import (
	"context"
	"fmt"
	"strings"
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
	NoHandoff        bool
	OperationID      string
}

// RecycleResult is how far a recycle got.
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
	switch {
	case b.Status != bindings.Active:
		return fail(StageCheck, fmt.Errorf("%s is %s", b.MailName, b.Status))
	case a.ExitCommand == "":
		return fail(StageCheck, fmt.Errorf("nhm knows no exit command for %s; retire it and spawn anew", b.Kind))
	case env.Caller != "" && b.PaneID == env.Caller:
		return fail(StageCheck, fmt.Errorf("%s runs in this pane", b.MailName))
	}
	kick, err := recycleKickoff(req, b)
	if err != nil {
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
	if b.Status != bindings.Active {
		return fail(StageCheck, fmt.Errorf("%s is %s", b.MailName, b.Status))
	}
	agent, err := env.Herdr.AgentGet(ctx, b.PaneID)
	if err != nil {
		return fail(StageCheck, fmt.Errorf("herdr: %w", err))
	}
	if agent.AgentStatus != herdr.StatusIdle && agent.AgentStatus != herdr.StatusDone {
		return fail(StageCheck, fmt.Errorf("%s is %s; an agent is recycled only between turns (interrupt it first)", b.MailName, agent.AgentStatus))
	}
	if agent.Agent != "" && agent.Agent != b.Kind {
		return fail(StageCheck, fmt.Errorf("pane %s holds a %s agent; the binding is %s", b.PaneID, agent.Agent, b.Kind))
	}
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: "recycle",
		Scope:   map[string]string{"binding_id": b.ID},
		Request: map[string]any{"binding_id": b.ID, "generation": b.Generation, "template": req.Template, "prompt": req.Prompt, "no_handoff": req.NoHandoff}},
		env.now())
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
		if beads, err := env.Facts.Assigned(ctx, b.MailProjectKey, b.MailName); err == nil && len(beads) > 0 {
			bead = &beads[0]
		} else if err != nil {
			res.Warnings = append(res.Warnings, "beads: "+err.Error())
		}
		h := env.Handoff(ctx, b, bead, original)
		res.Handoff = &h
	}

	// Stop the old agent, then wait for the shell prompt and the cleared
	// alias.
	res.Stage = StageStop
	if _, err := env.Herdr.AgentPrompt(ctx, herdr.AgentPromptParams{Target: b.PaneID, Text: a.ExitCommand}); err != nil &&
		herdr.ErrorCode(err) != herdr.CodeAgentPromptStalled {
		return fail(StageStop, fmt.Errorf("typing %s: %w", a.ExitCommand, err))
	}
	if !waitExit(ctx, env, b) {
		return fail(StageStop, fmt.Errorf("%s did not exit within %s; nothing started, the pane is as it was", b.MailName, stopWait))
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
	unlock()
	locked = false

	// The kickoff with the handoff, through the normal send path.
	res.Stage = StageKickoff
	if env.Send != nil {
		text := fmt.Sprintf("Your Agent Mail identity is already registered: name %s, project key %s (use it; don't register a new one). Your herdr name is %s, in herdr workspace %s.",
			b.MailName, b.MailProjectKey, alias, b.WorkspaceID)
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

// waitExit waits until the pane's shell holds the foreground again and
// herdr no longer lists an agent in the pane.
func waitExit(ctx context.Context, env Env, b bindings.Binding) bool {
	deadline := env.now().Add(stopWait)
	for env.now().Before(deadline) {
		info, err := env.Herdr.PaneProcessInfo(ctx, b.PaneID)
		shell := err == nil && info.ShellPID != nil && info.ForegroundProcessGroupID != nil && *info.ShellPID == *info.ForegroundProcessGroupID
		_, aerr := env.Herdr.AgentGet(ctx, b.PaneID)
		gone := herdr.ErrorCode(aerr) == herdr.CodeAgentNotFound || herdr.ErrorCode(aerr) == herdr.CodeAgentNotRunning
		if shell && gone {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		env.sleep(ctx, 250*time.Millisecond)
	}
	return false
}

// recycleKickoff is the successor's prompt: --prompt as typed, a
// template, or ACFS's default_new_agent.
func recycleKickoff(req RecycleRequest, b bindings.Binding) (string, error) {
	if req.Prompt != "" && req.Template != "" {
		return "", fmt.Errorf("give --prompt or -t, not both")
	}
	if req.Prompt != "" {
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
	return t.Execute(templates.ExecutionContext{Session: b.WorkspaceID, Project: b.MailProjectKey, AgentName: b.MailName,
		HerdrName: b.HerdrAlias, Model: b.Model, AgentType: b.Kind, AgentPane: b.PaneID})
}
