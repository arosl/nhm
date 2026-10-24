package spawn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

// Verb is the operation verb of a spawn.
const Verb = "spawn"

// Target statuses (plan-v8 2.2, partial failure).
const (
	StatusPlanned          = "planned"
	StatusIdentityCreated  = "identity_created"
	StatusPaneCreated      = "pane_created"
	StatusReady            = "ready"
	StatusKickoffSubmitted = "kickoff_submitted"
	StatusUnresolved       = "unresolved"
	StatusFailed           = "failed"
	StatusNotStarted       = "not_started"
)

// shellTries and shellPoll bound the wait for a new pane's shell prompt.
const (
	shellTries = 50
	shellPoll  = 200 * time.Millisecond
)

// dialogPoll is how often spawn reads herdr's state after it answered the
// folder-trust dialog (leftDialog).
const dialogPoll = 100 * time.Millisecond

// Herdr is what spawn needs from herdr; *herdr.Client has it.
type Herdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	WorkspaceCreate(ctx context.Context, p herdr.WorkspaceCreateParams) (herdr.Created, error)
	TabCreate(ctx context.Context, p herdr.TabCreateParams) (herdr.Created, error)
	TabRename(ctx context.Context, tabID, label string) (herdr.TabInfo, error)
	PaneSplit(ctx context.Context, p herdr.PaneSplitParams) (herdr.PaneInfo, error)
	PaneLayout(ctx context.Context, paneID string) (herdr.PaneLayout, error)
	PaneProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
	PaneRename(ctx context.Context, paneID, label string) (herdr.PaneInfo, error)
	PaneReportMetadata(ctx context.Context, p herdr.ReportMetadataParams) error
	PaneClose(ctx context.Context, paneID string) error
	AgentList(ctx context.Context) ([]herdr.AgentInfo, error)
	AgentStart(ctx context.Context, p herdr.AgentStartParams) (herdr.AgentStarted, error)
	AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error)
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentRead(ctx context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error)
	AgentSendKeys(ctx context.Context, target string, keys ...string) error
}

// Mail is what spawn needs from Agent Mail; *agentmail.Client has it.
type Mail interface {
	Call(ctx context.Context, tool string, args, out any) error
}

// Env is what a spawn runs against.
type Env struct {
	DB          *store.DB
	Bindings    *bindings.Store
	Herdr       Herdr
	Mail        Mail
	InstanceDir string
	Admission   AdmissionEnv
	// Send delivers the kickoff (send.Send with the instance's env).
	Send func(ctx context.Context, req send.Request) (send.Result, error)
	// IdentityFile writes Agent Mail's pane identity file (2.7).
	IdentityFile func(projectKey, paneID, name string) error
	// ProcStart reads a process's start time (bindings.ProcStart).
	ProcStart func(pid int64) (int64, error)
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration)
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	if e.Sleep != nil {
		e.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Target is one agent of a spawn and how far it got.
type Target struct {
	Index      int    `json:"index"`
	Kind       string `json:"kind"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	Slot       string `json:"slot"`
	MailName   string `json:"mail_name,omitempty"`
	HerdrName  string `json:"herdr_name,omitempty"`
	TabID      string `json:"tab_id,omitempty"`
	PaneID     string `json:"pane_id,omitempty"`
	BindingID  string `json:"binding_id,omitempty"`
	LaunchID   string `json:"launch_id,omitempty"`
	Provenance string `json:"identity_provenance,omitempty"`
	Status     string `json:"status"`
	Ready      bool   `json:"ready"`
	StartupMS  int64  `json:"startup_ms"`
	Error      string `json:"error,omitempty"`
	// Cleanup says what spawn removed after a failure, or what is left.
	Cleanup string `json:"cleanup,omitempty"`
	// Kickoff is the kickoff send's operation and its submission.
	KickoffOp     string `json:"kickoff_operation,omitempty"`
	KickoffStatus string `json:"kickoff_submission,omitempty"`
	// DelayMS is the stagger offset from the first start.
	DelayMS int64 `json:"delay_ms"`
	// newTab: the target opens a tab. Otherwise its pane is split from
	// a pane of its tab, at the tab's next grid cell (splitTarget): a tab
	// that exists (anchor is one of its panes) or the one target tabFirst
	// opened.
	newTab   bool
	anchor   string
	tabFirst int
	token    string
}

// Result is a spawn's outcome.
type Result struct {
	OperationID     string    `json:"operation_id,omitempty"`
	Replayed        bool      `json:"replayed,omitempty"`
	DryRun          bool      `json:"dry_run,omitempty"`
	Session         string    `json:"session"`
	WorkspaceID     string    `json:"workspace_id,omitempty"`
	WorkspaceNew    bool      `json:"workspace_created"`
	ProjectRoot     string    `json:"working_dir"`
	Admission       Admission `json:"admission"`
	StaggerMode     string    `json:"stagger_mode"`
	StaggerDelayMS  int64     `json:"stagger_delay_ms"`
	Targets         []Target  `json:"agents"`
	Warnings        []string  `json:"warnings"`
	CreatedAt       time.Time `json:"created_at"`
	TotalStartupMS  int64     `json:"total_startup_ms"`
	KickoffTemplate string    `json:"kickoff_template,omitempty"`
}

// Errors.
var (
	ErrRefused          = errors.New("admission refused the spawn")
	ErrNoWorkspace      = errors.New("no such workspace")
	ErrAmbiguousSession = errors.New("the session label names more than one workspace")
	// ErrSessionExists: Request.Safety, and the session's workspace exists.
	ErrSessionExists = errors.New("spawn safety")
)

// Ready reports whether every agent became ready.
func (r Result) Ready() bool {
	for _, t := range r.Targets {
		if !t.Ready {
			return false
		}
	}
	return true
}

// Spawn runs a spawn, add or new. Admission refuses before anything is
// created (ErrRefused, with the result's Admission). A dry run creates
// nothing and claims no operation. A repeated operation id returns the
// stored outcome and starts nothing.
func Spawn(ctx context.Context, env Env, req Request) (Result, error) {
	res := Result{Session: req.Session, ProjectRoot: req.ProjectRoot, StaggerMode: req.StaggerMode,
		StaggerDelayMS: req.StaggerDelay.Milliseconds(), Targets: []Target{}, Warnings: []string{}}
	if err := req.Validate(); err != nil {
		return res, err
	}
	res.StaggerMode = req.StaggerMode
	if req.OperationID != "" && !req.DryRun {
		if stored, ok, err := replay(ctx, env.DB, req); ok || err != nil {
			return stored, err
		}
	}
	ws, err := findWorkspace(ctx, env.Herdr, req.Session)
	if err != nil {
		return res, err
	}
	if ws == "" && req.Add {
		return res, fmt.Errorf("%w: %s (nhm add needs an existing workspace; nhm spawn creates it)", ErrNoWorkspace, req.Session)
	}
	if ws != "" && req.Safety {
		// ntm's words (internal/robot/spawn.go, setSpawnSafetyConflict).
		return res, fmt.Errorf("%w: session '%s' already exists (--spawn-safety mode prevents reuse; use 'nhm kill %s' first)",
			ErrSessionExists, req.Session, req.Session)
	}
	res.WorkspaceID, res.WorkspaceNew = ws, ws == ""
	// Before anything is created: an existing workspace may hold other
	// projects' agents only (nhm-8ze).
	if others, err := env.Bindings.OtherProjectsIn(ctx, ws, req.ProjectRoot); err != nil {
		res.Warnings = append(res.Warnings, "the workspace's projects: "+err.Error())
	} else if others != nil {
		res.Warnings = append(res.Warnings, bindings.MixedWorkspaceWarning(ws, req.ProjectRoot, others))
	}
	kickoff, err := kickoffBody(req)
	if err != nil {
		return res, err
	}
	if !req.NoKickoff && req.Prompt == "" {
		res.KickoffTemplate = defaultKickoff
	}
	res.Admission = Decide(ctx, env.Admission, req)
	targets, err := plan(ctx, env, ws, req)
	if err != nil {
		return res, err
	}
	res.Targets, res.DryRun = targets, req.DryRun
	if res.Admission.Decision != Admit {
		return res, fmt.Errorf("%w: %s", ErrRefused, res.Admission.Reason)
	}
	if req.DryRun {
		return res, nil
	}

	now := env.now()
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: Verb,
		Scope:   map[string]any{"scope": req.Scope, "session": req.Session, "working_dir": req.ProjectRoot},
		Request: fingerprint(req)}, now)
	if err != nil {
		return res, err
	}
	if claim.Existing {
		return stored(ctx, env.DB, claim.Operation.ID)
	}
	res.OperationID, res.CreatedAt = claim.Operation.ID, claim.Operation.CreatedAt
	for i := range res.Targets {
		res.Targets[i].LaunchID = fmt.Sprintf("%s.%d", res.OperationID, i)
		if err := writeTarget(ctx, env.DB, res.OperationID, res.Targets[i], now); err != nil {
			return res, err
		}
	}

	start := env.now()
	var prevStart time.Time
	stopped := false
	for i := range res.Targets {
		t := &res.Targets[i]
		if stopped {
			t.Status = StatusNotStarted
			_ = writeTarget(ctx, env.DB, res.OperationID, *t, env.now())
			continue
		}
		if i > 0 {
			stagger(ctx, env, req, prevStart)
		}
		prevStart = env.now()
		t.DelayMS = prevStart.Sub(start).Milliseconds()
		ok := startOne(ctx, env, req, &res, t, kickoff)
		_ = writeTarget(ctx, env.DB, res.OperationID, *t, env.now())
		if !ok {
			stopped = true
		}
	}
	res.TotalStartupMS = env.now().Sub(start).Milliseconds()
	status := "completed"
	if stopped {
		status = "partial"
	}
	if err := env.DB.SetOperationStatus(ctx, res.OperationID, status, true, env.now()); err != nil {
		return res, err
	}
	return res, nil
}

// stagger waits before the next start: fixed sleeps D; smart waits for
// at least D since the previous start (that agent is already ready, since
// startOne waits for it), never for its kickoff turn, capped at 120 s.
func stagger(ctx context.Context, env Env, req Request, prevStart time.Time) {
	switch req.StaggerMode {
	case StaggerFixed:
		env.sleep(ctx, req.StaggerDelay)
	case StaggerSmart:
		d := min(req.StaggerDelay-env.now().Sub(prevStart), smartCap)
		env.sleep(ctx, d)
	}
}

// startOne runs one target through identity, pane, binding, start,
// labels and kickoff. It returns false when the spawn must stop.
func startOne(ctx context.Context, env Env, req Request, res *Result, t *Target, kick kickoff) bool {
	begin := env.now()
	a := kinds.Lookup(t.Kind)
	fail := func(status string, err error) bool {
		t.Status, t.Error = status, err.Error()
		return false
	}

	// 1. Identity first.
	name, token, err := createIdentity(ctx, env.Mail, req.ProjectRoot, a.Program, t.Model)
	if err != nil {
		return fail(StatusFailed, fmt.Errorf("Agent Mail identity: %w", err))
	}
	t.MailName, t.Provenance, t.token, t.Status = name, string(bindings.Created), token, StatusIdentityCreated
	_ = writeTarget(ctx, env.DB, res.OperationID, *t, env.now())

	// 2. A fresh pane with the launch id in its environment, and the
	// name in AGENT_MAIL_AGENT, which ACFS's Agent Mail hook and am read
	// first: in a tab several agents share, the tab label is not the
	// agent's name (nhm-4c4). The kind's guard env goes there too, as
	// herdr's agent.start takes no env.
	paneEnv := map[string]string{"NHM_LAUNCH_ID": t.LaunchID, "NHM_AGENT": name, "AGENT_MAIL_AGENT": name}
	for k, v := range a.GuardEnv {
		paneEnv[k] = v
	}
	if err := createPane(ctx, env, req, res, t, paneEnv); err != nil {
		t.Cleanup = retireIdentity(ctx, env, req.ProjectRoot, t)
		return fail(StatusFailed, fmt.Errorf("pane: %w", err))
	}
	t.Status = StatusPaneCreated
	_ = writeTarget(ctx, env.DB, res.OperationID, *t, env.now())
	if !waitShell(ctx, env, t.PaneID) {
		t.Cleanup = cleanupPane(ctx, env, req, t)
		return fail(StatusFailed, fmt.Errorf("the shell in %s never reached its prompt", t.PaneID))
	}

	// 3. The binding, generation 1, and its token.
	alias, err := freeAlias(ctx, env.Herdr, name)
	if err != nil {
		t.Cleanup = cleanupPane(ctx, env, req, t)
		return fail(StatusFailed, err)
	}
	t.HerdrName = alias
	b, err := env.Bindings.Create(ctx, bindings.New{MailName: name, MailProjectKey: req.ProjectRoot, ProjectID: req.ProjectID,
		Provenance: bindings.Created, CreatedByOp: res.OperationID, Role: req.Role, Occupant: bindings.Occupant{WorkspaceID: res.WorkspaceID,
			TabID: t.TabID, PaneID: t.PaneID, HerdrAlias: alias, Kind: t.Kind, Model: t.Model, Effort: t.Effort, LaunchID: t.LaunchID,
			Evidence: []bindings.Evidence{{Kind: "spawn", Detail: "created by " + res.OperationID}}}})
	if err != nil {
		t.Cleanup = cleanupPane(ctx, env, req, t)
		return fail(StatusFailed, fmt.Errorf("binding: %w", err))
	}
	t.BindingID, t.Slot = b.ID, b.Slot
	if token != "" {
		if err := store.WriteSecret(env.InstanceDir, b.ID, token); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: the registration token was not kept: %v", name, err))
		}
	}

	// 4. Start the agent and wait until it is ready for input.
	if err := startAgent(ctx, env, req, t, a); err != nil {
		switch herdr.ErrorCode(err) {
		case codeAgentPaneBusy, herdr.CodeInvalidAgentName, herdr.CodeAgentNameTaken:
			// herdr started nothing: it refuses a busy pane and a name
			// before it launches anything, so the pane holds only its
			// shell (nhm-4xt).
			t.Cleanup = cleanupBinding(ctx, env, req, t, b)
			return fail(StatusFailed, err)
		}
		t.Cleanup = "left in place: the agent may have started; run nhm admin recover inspect"
		return fail(StatusUnresolved, err)
	}
	t.Ready, t.Status = true, StatusReady
	t.StartupMS = env.now().Sub(begin).Milliseconds()
	record(ctx, env, t, b)

	// 5. Labels: the pane, the tab, the sidebar tokens, Agent Mail's
	// pane identity file.
	label(ctx, env, req, res, t)

	// 6. The kickoff, through send.
	if !kick.empty() {
		sendKickoff(ctx, env, req, res, t, kick.text(req, t), nil)
	}
	return true
}

// Kickoff sends a ready target of a finished spawn its kickoff, the
// identity preamble then task, and records it in the operation's receipt.
// spawn --assign calls it after the assignment for an agent that got no
// bead (nhm-cvw). A failure is a warning in res.
func Kickoff(ctx context.Context, env Env, req Request, res *Result, t *Target, task string) {
	sendKickoff(ctx, env, req, res, t, task, nil)
	_ = writeTarget(ctx, env.DB, res.OperationID, *t, env.now())
}

// KickoffGated is Kickoff for an agent that a replay of spawn --assign
// found untouched (nhm-vcf): the send's delivery gate must find it ready,
// and its binding at FirstGeneration, so an agent that got a prompt
// since, or a later occupant, gets nothing.
func KickoffGated(ctx context.Context, env Env, req Request, res *Result, t *Target, task string) {
	sendKickoff(ctx, env, req, res, t, task, map[string]int64{t.BindingID: FirstGeneration})
	_ = writeTarget(ctx, env.DB, res.OperationID, *t, env.now())
}

// sendKickoff sends t its kickoff through send: the identity preamble,
// then task. With expect nil the send takes the agent in any state;
// otherwise the gate applies, and expect freezes the binding.
func sendKickoff(ctx context.Context, env Env, req Request, res *Result, t *Target, task string, expect map[string]int64) {
	if env.Send == nil {
		return
	}
	text := IdentityPreamble(t.MailName, req.ProjectRoot, t.HerdrName, res.WorkspaceID) + "\n\n" + task
	// CM and CASS are asked about the task, not the identity preamble:
	// with the preamble's start as the query, cm drops every rule as
	// irrelevant (acfs-9hkh.87).
	enr := req.Enrich
	if enr.Query == "" {
		enr.Query = task
	}
	sr, err := env.Send(ctx, send.Request{Session: req.Session, WorkspaceID: res.WorkspaceID, ProjectRoot: req.ProjectRoot,
		Selector: send.Selector{Names: []string{t.MailName}}, Message: text, WhenAny: expect == nil, Expect: expect,
		Source: history.SourceCLI, Scope: req.Scope, Enrich: enr})
	switch {
	case err != nil:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: kickoff not sent: %v", t.MailName, err))
	case len(sr.Targets) > 0:
		t.KickoffOp, t.KickoffStatus = sr.OperationID, sr.Targets[0].Status
		if sr.Targets[0].Status == send.StatusSubmitted || sr.Targets[0].Status == send.StatusPossiblySubmitted {
			t.Status = StatusKickoffSubmitted
		}
	}
}

// codeAgentPaneBusy is herdr's refusal of agent.start when the pane's
// shell is not at its prompt: nothing was started.
const codeAgentPaneBusy = "agent_pane_busy"

func createPane(ctx context.Context, env Env, req Request, res *Result, t *Target, paneEnv map[string]string) error {
	switch {
	case res.WorkspaceID == "":
		// A session may have appeared since the check at the start: look
		// again just before it is made, as ntm does (internal/robot/
		// spawn.go:1130-1148). herdr allows two workspaces of one label.
		if req.Safety {
			if ws, err := findWorkspace(ctx, env.Herdr, req.Session); err != nil {
				return err
			} else if ws != "" {
				return fmt.Errorf("%w: session '%s' appeared while the spawn ran (--spawn-safety mode prevents reuse; use 'nhm kill %s' first)",
					ErrSessionExists, req.Session, req.Session)
			}
		}
		c, err := env.Herdr.WorkspaceCreate(ctx, herdr.WorkspaceCreateParams{Cwd: req.ProjectRoot, Label: req.Session,
			Env: paneEnv, Focus: ptr(false)})
		if err != nil {
			return err
		}
		if c.Workspace != nil {
			res.WorkspaceID = c.Workspace.WorkspaceID
		} else {
			res.WorkspaceID = c.Tab.WorkspaceID
		}
		t.TabID, t.PaneID = c.Tab.TabID, c.RootPane.PaneID
	case t.newTab:
		c, err := env.Herdr.TabCreate(ctx, herdr.TabCreateParams{WorkspaceID: res.WorkspaceID, Cwd: req.ProjectRoot,
			Label: tabLabel(req, t), Env: paneEnv, Focus: ptr(false)})
		if err != nil {
			return err
		}
		t.TabID, t.PaneID = c.Tab.TabID, c.RootPane.PaneID
	default:
		anchor := t.anchor
		if anchor == "" {
			anchor = res.Targets[t.tabFirst].PaneID
		}
		target, dir, ratio := splitTarget(ctx, env.Herdr, anchor, req.PerTab)
		p, err := env.Herdr.PaneSplit(ctx, herdr.PaneSplitParams{Direction: dir, TargetPaneID: target,
			WorkspaceID: res.WorkspaceID, Cwd: req.ProjectRoot, Env: paneEnv, Focus: ptr(false), Ratio: ptr(ratio)})
		if err != nil {
			return err
		}
		t.TabID, t.PaneID = p.TabID, p.PaneID
		// The tab now holds several of the project's agents: it takes the
		// project's label at once, so a later failure of this agent leaves
		// no tab of several panes under one agent's name (nhm-4c4).
		if req.Label == "" && req.Group == "" {
			if _, err := env.Herdr.TabRename(ctx, t.TabID, bindings.ProjectTabLabel(req.ProjectRoot)); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: tab label: %v", t.MailName, err))
			}
		}
	}
	return nil
}

func tabLabel(req Request, t *Target) string {
	switch {
	case req.Label != "":
		return req.Label
	case req.Group != "":
		return req.Group
	}
	return t.MailName
}

// waitShell waits until the pane's shell holds the foreground with no
// command running, which agent.start needs.
func waitShell(ctx context.Context, env Env, pane string) bool {
	for i := 0; i < shellTries; i++ {
		info, err := env.Herdr.PaneProcessInfo(ctx, pane)
		if err == nil && info.ShellPID != nil && info.ForegroundProcessGroupID != nil &&
			*info.ShellPID == *info.ForegroundProcessGroupID {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		env.sleep(ctx, shellPoll)
	}
	return false
}

// leftDialog waits until herdr's state of the pane moves on from blocked,
// the answer that showed the folder-trust dialog: a later
// state_change_seq, or a state other than blocked. herdr keeps the
// dialog's blocked for a moment after the keys (about 0.3 s, nhm-913), and
// agent.wait has no baseline, so a wait at once returns that blocked. It
// reads agent.get every dialogPoll, for at most timeout; false: the state
// did not move.
func leftDialog(ctx context.Context, env Env, pane string, blocked herdr.AgentInfo, timeout time.Duration) (bool, error) {
	for i := int64(0); i <= int64(timeout/dialogPoll); i++ {
		a, err := env.Herdr.AgentGet(ctx, pane)
		if err != nil {
			return false, err
		}
		if a.AgentStatus != herdr.StatusBlocked ||
			a.StateChangeSeq != nil && blocked.StateChangeSeq != nil && *a.StateChangeSeq > *blocked.StateChangeSeq {
			return true, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		env.sleep(ctx, dialogPoll)
	}
	return false, nil
}

// IdentityPreamble opens the kickoff that spawn and recycle send: the
// agent's Agent Mail identity, which nhm registered, and its herdr name.
// The prompt that follows may be ACFS's palette text, which says only
// "register": the agent registers again with exactly this name and key,
// as Agent Mail picks a new name when none is given, and a second key
// makes a second identity (nhm-lm4).
func IdentityPreamble(mailName, projectKey, alias, workspaceID string) string {
	return fmt.Sprintf("Your Agent Mail identity is already registered: name %s, project key %s (use it; don't register a new one). "+
		"When the prompt below says to register, register with exactly this name and project key. "+
		"Your herdr name is %s, in herdr workspace %s.", mailName, projectKey, alias, workspaceID)
}

// freeAlias is the lowercase mail name, or with a short suffix when that
// name is live elsewhere (another project). The pane label keeps the
// exact mail name.
func freeAlias(ctx context.Context, h Herdr, name string) (string, error) {
	if _, err := bindings.Alias(name); err != nil {
		return "", err
	}
	agents, err := h.AgentList(ctx)
	if err != nil {
		return "", err
	}
	// The one rule that adopt, import and the hooks' bind use too
	// (nhm-4xt). A new pane holds no agent of its own yet.
	return bindings.FreeAlias(name, agents, "")
}

// startAgent runs agent.start and waits until the agent is ready (idle
// or done, launch not pending). A pane whose shell was not ready is
// retried once. A first-run folder-trust dialog is answered only with
// --trust-folder.
func startAgent(ctx context.Context, env Env, req Request, t *Target, a kinds.Adapter) error {
	argv := LaunchArgv(a, t.Model, t.Effort, env.Admission.Settings[a.Name])
	ms := req.ReadyTimeout.Milliseconds()
	params := herdr.AgentStartParams{Name: t.HerdrName, Kind: a.Name, PaneID: t.PaneID, Args: argv[1:], TimeoutMS: &ms}
	_, err := env.Herdr.AgentStart(ctx, params)
	if herdr.ErrorCode(err) == codeAgentPaneBusy && waitShell(ctx, env, t.PaneID) {
		_, err = env.Herdr.AgentStart(ctx, params)
	}
	if err != nil {
		return err
	}
	ready := func() (herdr.AgentInfo, error) {
		info, err := env.Herdr.AgentWait(ctx, herdr.AgentWaitParams{Target: t.PaneID,
			Until: []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone, herdr.StatusBlocked}, TimeoutMS: &ms})
		if herdr.ErrorCode(err) == herdr.CodeTimeout && a.Dialog != nil {
			err = NamedDialog(ctx, env.Herdr, t.PaneID, a, err)
		}
		return info, err
	}
	info, err := ready()
	if err != nil {
		return err
	}
	if info.AgentStatus == herdr.StatusBlocked {
		if !req.TrustFolder || a.TrustKeys == nil {
			return fmt.Errorf("the agent waits at a dialog (a first-run question such as 'Trust this folder?'): answer it in pane %s%s",
				t.PaneID, trustHint(req, a))
		}
		// The pane's last lines, its scrollback too: Claude Code draws the
		// dialog inline, so in a pane shorter than the dialog (40x12) the
		// question scrolls off the screen while the answers stay on it
		// (nhm-exg).
		n := 40
		screen, err := env.Herdr.AgentRead(ctx, herdr.AgentReadParams{Target: t.PaneID, Source: herdr.ReadRecent, Lines: &n})
		if err != nil {
			return err
		}
		keys := a.TrustKeys(screen.Text)
		if keys == nil {
			return fmt.Errorf("the agent waits at a dialog that is not the folder-trust question: answer it in pane %s", t.PaneID)
		}
		if err := env.Herdr.AgentSendKeys(ctx, t.PaneID, keys...); err != nil {
			return err
		}
		if moved, err := leftDialog(ctx, env, t.PaneID, info, req.ReadyTimeout); err != nil {
			return err
		} else if !moved {
			return fmt.Errorf("the agent still waits at a dialog after nhm answered the folder-trust question: answer it in pane %s", t.PaneID)
		}
		if info, err = ready(); err != nil {
			return err
		}
		if info.AgentStatus == herdr.StatusBlocked {
			return fmt.Errorf("the agent waits at another dialog: answer it in pane %s", t.PaneID)
		}
	}
	if info.LaunchPending != nil && *info.LaunchPending {
		return fmt.Errorf("herdr still reports the launch as pending")
	}
	return nil
}

// ScreenReader reads an agent's pane; spawn's and lifecycle's Herdr have
// it.
type ScreenReader interface {
	AgentRead(ctx context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error)
}

// NamedDialog names the dialog that the agent waits at when the ready
// wait ran out (err) and the kind knows it on the pane's last lines
// (kinds.Adapter.Dialog): herdr reads some dialogs as unknown, not blocked,
// so agent.wait never returns for them (nhm-5as). Otherwise it is err.
// Spawn and recycle's start call it.
func NamedDialog(ctx context.Context, h ScreenReader, pane string, a kinds.Adapter, err error) error {
	n := 40
	screen, rerr := h.AgentRead(ctx, herdr.AgentReadParams{Target: pane, Source: herdr.ReadRecent, Lines: &n})
	if rerr != nil {
		return err
	}
	if d := a.Dialog(screen.Text); d != "" {
		return fmt.Errorf("the agent waits at %s, which herdr does not report: answer it in pane %s (%w)", d, pane, err)
	}
	return err
}

func trustHint(req Request, a kinds.Adapter) string {
	if !req.TrustFolder && a.TrustKeys != nil {
		return " (--trust-folder answers the folder-trust dialog, when you trust the project)"
	}
	return ""
}

// record completes the binding: the native session and the process.
func record(ctx context.Context, env Env, t *Target, b bindings.Binding) {
	c := bindings.Continuation{Evidence: []bindings.Evidence{{Kind: "spawn", Detail: "ready"}}}
	if info, err := env.Herdr.AgentGet(ctx, t.PaneID); err == nil && info.AgentSession != nil {
		s := info.AgentSession
		c.NativeSession = &bindings.NativeSession{Source: s.Source, Agent: s.Agent, Kind: s.Kind, Value: s.Value}
	}
	if p, err := env.Herdr.PaneProcessInfo(ctx, t.PaneID); err == nil && len(p.ForegroundProcesses) > 0 {
		c.ForegroundPID = int64(p.ForegroundProcesses[0].PID)
		if env.ProcStart != nil {
			c.ForegroundStart, _ = env.ProcStart(c.ForegroundPID)
		}
	}
	_, _ = env.Bindings.Continue(ctx, b.ID, b.Generation, c)
}

func label(ctx context.Context, env Env, req Request, res *Result, t *Target) {
	model := t.Model
	if model == "" {
		model = t.Kind
	}
	warn := func(what string, err error) {
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %s: %v", t.MailName, what, err))
		}
	}
	_, err := env.Herdr.PaneRename(ctx, t.PaneID, t.MailName+" "+model)
	warn("pane label", err)
	// A split pane's tab took the project's label in createPane.
	if t.newTab {
		_, err = env.Herdr.TabRename(ctx, t.TabID, tabLabel(req, t))
		warn("tab label", err)
	}
	tokens := map[string]string{"mail": t.MailName, "model": model}
	if req.Group != "" {
		tokens["group"] = req.Group
	}
	warn("pane metadata", env.Herdr.PaneReportMetadata(ctx, herdr.ReportMetadataParams{PaneID: t.PaneID, Source: "nhm",
		Agent: t.Kind, Tokens: tokens}))
	if env.IdentityFile != nil {
		warn("Agent Mail identity file", env.IdentityFile(req.ProjectRoot, t.PaneID, t.MailName))
	}
}

// createIdentity is ensure_project then create_agent_identity, as ACFS's
// Bash does: the server answers nothing for a project it does not have.
func createIdentity(ctx context.Context, m Mail, project, program, model string) (name, token string, err error) {
	if m == nil {
		return "", "", errors.New("no Agent Mail client")
	}
	if err := m.Call(ctx, "ensure_project", map[string]any{"human_key": project}, nil); err != nil {
		return "", "", err
	}
	if model == "" {
		model = "default"
	}
	var out struct {
		Name  string `json:"name"`
		Token string `json:"registration_token"`
	}
	if err := m.Call(ctx, "create_agent_identity", map[string]any{"project_key": project, "program": program, "model": model}, &out); err != nil {
		return "", "", err
	}
	if out.Name == "" {
		return "", "", errors.New("create_agent_identity returned no name")
	}
	return out.Name, out.Token, nil
}

// retireIdentity soft-retires an identity this spawn created and no agent
// uses. Without its token it stays, and the cleanup says so.
func retireIdentity(ctx context.Context, env Env, project string, t *Target) string {
	if t.token == "" {
		return "the Agent Mail identity " + t.MailName + " has no agent and no kept token; am agents reap removes it"
	}
	err := env.Mail.Call(ctx, "retire_agent", map[string]any{"project_key": project, "agent_name": t.MailName,
		"registration_token": t.token}, nil)
	if err != nil {
		return "the Agent Mail identity " + t.MailName + " has no agent; retiring it failed: " + err.Error()
	}
	return "retired the unused Agent Mail identity " + t.MailName
}

// cleanupPane closes the pane this spawn created, when only its shell
// runs there, and retires the identity.
func cleanupPane(ctx context.Context, env Env, req Request, t *Target) string {
	parts := []string{}
	if info, err := env.Herdr.PaneProcessInfo(ctx, t.PaneID); err == nil && info.ShellPID != nil &&
		info.ForegroundProcessGroupID != nil && *info.ShellPID == *info.ForegroundProcessGroupID {
		if err := env.Herdr.PaneClose(ctx, t.PaneID); err == nil {
			parts = append(parts, "closed its empty pane "+t.PaneID)
		} else {
			parts = append(parts, "could not close pane "+t.PaneID+": "+err.Error())
		}
	} else {
		parts = append(parts, "left pane "+t.PaneID+": something runs there")
	}
	parts = append(parts, retireIdentity(ctx, env, req.ProjectRoot, t))
	return strings.Join(parts, "; ")
}

// cleanupBinding also retires the binding this spawn created and drops
// its token.
func cleanupBinding(ctx context.Context, env Env, req Request, t *Target, b bindings.Binding) string {
	msg := cleanupPane(ctx, env, req, t)
	if _, err := env.Bindings.Retire(ctx, b.ID, b.Generation, "spawn: the agent did not start"); err != nil {
		msg += "; the binding stays: " + err.Error()
	}
	_ = store.DeleteSecret(env.InstanceDir, b.ID)
	return msg
}

func findWorkspace(ctx context.Context, h Herdr, session string) (string, error) {
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == session {
			return w.WorkspaceID, nil
		}
		if w.Label == session {
			ids = append(ids, w.WorkspaceID)
		}
	}
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	}
	return "", fmt.Errorf("%w: %q is %s; give the workspace id", ErrAmbiguousSession, session, strings.Join(ids, ", "))
}

// plan lays out the targets with the slots they will get. Agents of one
// project share a tab, up to PerTab (nhm-4c4): a target first goes into
// a tab of the project with room (joinable), lowest N first, then into a
// tab this spawn opened, then into a new tab with the workspace's next N.
// M is one more than the tab's highest, as allocateSlot gives it.
func plan(ctx context.Context, env Env, workspaceID string, req Request) ([]Target, error) {
	maxTab := -1
	tabN, maxPane := map[string]int{}, map[int]int{}
	if workspaceID != "" {
		rows, err := env.DB.SQL().QueryContext(ctx, `SELECT slot, tab_id FROM slots WHERE workspace_id = ?`, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("spawn: slots: %w", err)
		}
		for rows.Next() {
			var text string
			var tab sql.NullString
			if err := rows.Scan(&text, &tab); err != nil {
				_ = rows.Close()
				return nil, err
			}
			s, err := bindings.ParseSlot(text)
			if err != nil {
				continue
			}
			maxTab = max(maxTab, s.Tab)
			maxPane[s.Tab] = max(maxPane[s.Tab], s.Pane)
			if tab.String != "" {
				tabN[tab.String] = s.Tab
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("spawn: slots: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	rooms, err := joinable(ctx, env, workspaceID, req, tabN, maxPane)
	if err != nil {
		return nil, err
	}
	var out []Target
	i := 0
	for _, w := range req.Agents {
		for n := 0; n < w.Count; n++ {
			var r *tabRoom
			for _, c := range rooms {
				if c.agents < req.PerTab {
					r = c
					break
				}
			}
			if r == nil {
				maxTab++
				r = &tabRoom{n: maxTab, first: i}
				rooms = append(rooms, r)
			}
			r.agents++
			r.maxPane++
			out = append(out, Target{Index: i, Kind: w.Kind, Model: w.Model, Effort: effortOf(req, w), Slot: fmt.Sprintf("%d.%d", r.n, r.maxPane),
				Status: StatusPlanned, newTab: r.tabID == "" && r.first == i, anchor: r.anchor, tabFirst: r.first})
			i++
		}
	}
	return out, nil
}

// fingerprint is the claimed request: the caller's input, not the names
// Agent Mail will give.
func fingerprint(req Request) map[string]any {
	fp := map[string]any{"session": req.Session, "add": req.Add, "project": req.ProjectRoot, "agents": req.Agents, "effort": req.Effort,
		"label": req.Label, "group": req.Group, "per_tab": req.PerTab, "prompt": req.Prompt, "no_kickoff": req.NoKickoff,
		// The next_bead kickoff of the old --assign is gone (nhm-cvw). The
		// key is false without --assign, as before; with it, the choices
		// below stand beside it, so an operation of the old --assign never
		// matches a new one.
		"assign": req.AssignOptions != "", "stagger": req.StaggerMode, "delay_ms": req.StaggerDelay.Milliseconds(),
		"force": req.Force, "allow_unguarded": req.AllowUnguarded, "require_observations": req.RequireObservations,
		paneEnvKey: true}
	// Only when set, so every fingerprint stored before stays the same.
	if req.Safety {
		fp["safety"] = true
	}
	if req.AssignOptions != "" {
		fp["assign_options"] = req.AssignOptions
	}
	return fp
}

// writeTarget stores a target's progress as the operation's receipt row.
func writeTarget(ctx context.Context, db *store.DB, opID string, t Target, now time.Time) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = db.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation, status, result_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (operation_id, target_index) DO UPDATE SET binding_id = excluded.binding_id,
		generation = excluded.generation, status = excluded.status, result_json = excluded.result_json, updated_at = excluded.updated_at`,
		opID, t.Index, nullStr(t.BindingID), nullGen(t.BindingID), t.Status, string(data), store.FormatTime(now))
	if err != nil {
		return fmt.Errorf("spawn: receipt %s/%d: %w", opID, t.Index, err)
	}
	return nil
}

func replay(ctx context.Context, db *store.DB, req Request) (Result, bool, error) {
	op, err := db.Operation(ctx, req.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	data, _ := json.Marshal(fingerprint(req))
	if op.Verb != Verb || op.Fingerprint != store.Fingerprint(Verb, data) {
		return Result{}, false, fmt.Errorf("%w: %s was claimed by %s with a different request", store.ErrOperationIDConflict, req.OperationID, op.Verb)
	}
	res, err := stored(ctx, db, req.OperationID)
	return res, true, err
}

// stored reads a spawn's outcome from its receipts.
func stored(ctx context.Context, db *store.DB, opID string) (Result, error) {
	op, err := db.Operation(ctx, opID)
	if err != nil {
		return Result{}, err
	}
	var scope struct {
		Session    string `json:"session"`
		WorkingDir string `json:"working_dir"`
	}
	_ = json.Unmarshal([]byte(op.ScopeJSON), &scope)
	res := Result{OperationID: opID, Replayed: true, Session: scope.Session, ProjectRoot: scope.WorkingDir,
		CreatedAt: op.CreatedAt, Targets: []Target{}, Warnings: []string{}, Admission: Admission{Decision: Admit, Reasons: []string{}, Warnings: []string{}}}
	rows, err := db.SQL().QueryContext(ctx, `SELECT result_json FROM receipts WHERE operation_id = ? ORDER BY target_index`, opID)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return res, err
		}
		var t Target
		if err := json.Unmarshal([]byte(text), &t); err != nil {
			return res, err
		}
		res.Targets = append(res.Targets, t)
	}
	return res, rows.Err()
}

// ReachedBindings are the bindings of a spawn's targets that a receipt of
// another operation names: a send (a kickoff, a bead's dispatch, nhm
// send), a reap. send writes its receipt before it types anything, so a
// binding that is not in the set has had no prompt from nhm since the
// spawn (nhm-vcf), other than mail nudges. A nudge's receipt (nhm-qaa)
// does not count: a nudge gives no work, and nudges had no receipts
// before.
func ReachedBindings(ctx context.Context, db *store.DB, opID string) (map[string]bool, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT DISTINCT r.binding_id FROM receipts r
		JOIN receipts s ON s.binding_id = r.binding_id
		JOIN operations o ON o.operation_id = r.operation_id
		WHERE s.operation_id = ? AND r.operation_id != ? AND o.verb != ?`, opID, opID, wake.Verb)
	if err != nil {
		return nil, fmt.Errorf("spawn: receipts of %s's agents: %w", opID, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("spawn: receipts of %s's agents: %w", opID, err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// FirstGeneration is the generation of a binding that spawn creates
// (bindings.Create); a later occupant counts up.
const FirstGeneration = 1

// nullGen is FirstGeneration for a binding this spawn created.
func nullGen(bindingID string) any {
	if bindingID == "" {
		return nil
	}
	return FirstGeneration
}

func ptr[T any](v T) *T { return &v }

// The kickoff (plan-v8 2.2 step 6).
const defaultKickoff = "default_new_agent"

// kickoff is the kickoff's text: the operator's --prompt goes out as
// typed, as a plain send does; the palette prompt (default_new_agent) is a
// template whose per-agent placeholders are filled.
type kickoff struct {
	raw     string
	palette string
}

func (k kickoff) empty() bool { return k.raw == "" && k.palette == "" }

// kickoffBody reads the kickoff: --prompt, or ACFS's default_new_agent
// palette prompt. It refuses before anything is created when the palette
// prompt cannot be found or has a placeholder nothing fills.
func kickoffBody(req Request) (kickoff, error) {
	var k kickoff
	if req.NoKickoff {
		return k, nil
	}
	k.raw = req.Prompt
	if k.raw == "" {
		t, err := templates.NewLoader(req.ProjectRoot).Load(defaultKickoff)
		if err != nil {
			return k, fmt.Errorf("the kickoff: %w (give --prompt)", err)
		}
		k.palette = t.Body
	}
	check := &templates.Template{Name: "kickoff", Body: k.palette}
	if _, err := check.ExecuteShared(templates.ExecutionContext{Session: req.Session, Project: req.ProjectRoot}); err != nil {
		return k, fmt.Errorf("the kickoff: %w", err)
	}
	return k, nil
}

// AssignPrefix is the kickoff that spawn --assign sends ahead of each
// agent's work prompt, in the same delivery (nhm-cvw): the spawn itself
// sends no kickoff then, since a working agent refuses a second prompt.
// It is --prompt as typed, else ACFS's default_new_agent palette prompt
// with the session and project filled in; "" with NoKickoff. A palette
// placeholder that names one agent refuses: one text goes to every agent.
func AssignPrefix(req Request) (string, error) {
	if req.NoKickoff {
		return "", nil
	}
	if req.Prompt != "" {
		return req.Prompt, nil
	}
	t, err := templates.NewLoader(req.ProjectRoot).Load(defaultKickoff)
	if err != nil {
		return "", fmt.Errorf("the kickoff: %w (give --prompt or --no-kickoff)", err)
	}
	out, err := t.Execute(templates.ExecutionContext{Session: req.Session, Project: req.ProjectRoot})
	if err != nil {
		return "", fmt.Errorf("--assign sends one kickoff to every agent: %w (give --prompt)", err)
	}
	return out, nil
}

// text is the kickoff for one agent; kickoffBody checked the shared
// placeholders.
func (k kickoff) text(req Request, t *Target) string {
	parts := []string{}
	if k.raw != "" {
		parts = append(parts, k.raw)
	}
	if k.palette != "" {
		tm := &templates.Template{Name: "kickoff", Body: k.palette}
		out, err := tm.Execute(templates.ExecutionContext{Session: req.Session, Project: req.ProjectRoot,
			AgentName: t.MailName, HerdrName: t.HerdrName, Model: t.Model, AgentType: t.Kind, AgentPane: t.PaneID,
			AgentNum: t.Index + 1})
		if err != nil {
			out = k.palette
		}
		parts = append(parts, out)
	}
	return strings.Join(parts, "\n\n")
}
