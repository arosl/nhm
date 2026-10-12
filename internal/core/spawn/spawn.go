package spawn

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/templates"
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

// Herdr is what spawn needs from herdr; *herdr.Client has it.
type Herdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	WorkspaceCreate(ctx context.Context, p herdr.WorkspaceCreateParams) (herdr.Created, error)
	TabCreate(ctx context.Context, p herdr.TabCreateParams) (herdr.Created, error)
	TabRename(ctx context.Context, tabID, label string) (herdr.TabInfo, error)
	PaneSplit(ctx context.Context, p herdr.PaneSplitParams) (herdr.PaneInfo, error)
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
	// newTab: the target opens a tab; split: the pane is split from
	// the previous target's pane.
	newTab bool
	token  string
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
	res.WorkspaceID, res.WorkspaceNew = ws, ws == ""
	kickoff, err := kickoffBody(req)
	if err != nil {
		return res, err
	}
	if !req.NoKickoff && req.Prompt == "" {
		res.KickoffTemplate = defaultKickoff
	}
	res.Admission = Decide(ctx, env.Admission, req)
	targets, err := plan(ctx, env.DB, ws, req)
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

	// 2. A fresh pane with the launch id in its environment.
	paneEnv := map[string]string{"NHM_LAUNCH_ID": t.LaunchID, "NHM_AGENT": name}
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
		Provenance: bindings.Created, CreatedByOp: res.OperationID, Occupant: bindings.Occupant{WorkspaceID: res.WorkspaceID,
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
		if herdr.ErrorCode(err) == codeAgentPaneBusy {
			// herdr started nothing: the pane holds only its shell.
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
	if !kick.empty() && env.Send != nil {
		text := fmt.Sprintf("Your Agent Mail identity is already registered: name %s, project key %s (use it; don't register a new one). Your herdr name is %s, in herdr workspace %s.\n\n%s",
			name, req.ProjectRoot, alias, res.WorkspaceID, kick.text(req, t))
		sr, err := env.Send(ctx, send.Request{Session: req.Session, WorkspaceID: res.WorkspaceID, ProjectRoot: req.ProjectRoot,
			Selector: send.Selector{Names: []string{name}}, Message: text, WhenAny: true, Source: history.SourceCLI,
			Scope: req.Scope, Enrich: req.Enrich})
		switch {
		case err != nil:
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: kickoff not sent: %v", name, err))
		case len(sr.Targets) > 0:
			t.KickoffOp, t.KickoffStatus = sr.OperationID, sr.Targets[0].Status
			if sr.Targets[0].Status == send.StatusSubmitted || sr.Targets[0].Status == send.StatusPossiblySubmitted {
				t.Status = StatusKickoffSubmitted
			}
		}
	}
	return true
}

// codeAgentPaneBusy is herdr's refusal of agent.start when the pane's
// shell is not at its prompt: nothing was started.
const codeAgentPaneBusy = "agent_pane_busy"

func createPane(ctx context.Context, env Env, req Request, res *Result, t *Target, paneEnv map[string]string) error {
	switch {
	case res.WorkspaceID == "":
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
		prev := res.Targets[t.Index-1]
		p, err := env.Herdr.PaneSplit(ctx, herdr.PaneSplitParams{Direction: "right", TargetPaneID: prev.PaneID,
			WorkspaceID: res.WorkspaceID, Cwd: req.ProjectRoot, Env: paneEnv, Focus: ptr(false)})
		if err != nil {
			return err
		}
		t.TabID, t.PaneID = p.TabID, p.PaneID
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

var aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// freeAlias is the lowercase mail name, or with a short suffix when that
// name is live elsewhere (another project). The pane label keeps the
// exact mail name.
func freeAlias(ctx context.Context, h Herdr, name string) (string, error) {
	base := strings.ToLower(name)
	if !aliasPattern.MatchString(base) {
		return "", fmt.Errorf("the Agent Mail name %q makes no valid herdr name", name)
	}
	agents, err := h.AgentList(ctx)
	if err != nil {
		return "", err
	}
	live := map[string]bool{}
	for _, a := range agents {
		live[a.Name] = true
	}
	if !live[base] {
		return base, nil
	}
	for i := 2; i <= 9; i++ {
		c := fmt.Sprintf("%s-%d", base, i)
		if len(c) <= 32 && !live[c] {
			return c, nil
		}
	}
	return "", fmt.Errorf("the herdr name %s and its suffixes are taken", base)
}

// startAgent runs agent.start and waits until the agent is ready (idle
// or done, launch not pending). A pane whose shell was not ready is
// retried once. A first-run folder-trust dialog is answered only with
// --trust-folder.
func startAgent(ctx context.Context, env Env, req Request, t *Target, a kinds.Adapter) error {
	argv := LaunchArgv(a, t.Model, t.Effort, env.Admission.Settings)
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
		return env.Herdr.AgentWait(ctx, herdr.AgentWaitParams{Target: t.PaneID,
			Until: []herdr.AgentStatus{herdr.StatusIdle, herdr.StatusDone, herdr.StatusBlocked}, TimeoutMS: &ms})
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
		n := 40
		screen, err := env.Herdr.AgentRead(ctx, herdr.AgentReadParams{Target: t.PaneID, Source: herdr.ReadVisible, Lines: &n})
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
	if t.newTab || t.Index == 0 {
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

// plan lays out the targets with the slots they will get: the next tab
// numbers of the workspace, M from 1 within a tab.
func plan(ctx context.Context, db *store.DB, workspaceID string, req Request) ([]Target, error) {
	maxTab := -1
	if workspaceID != "" {
		rows, err := db.SQL().QueryContext(ctx, `SELECT slot FROM slots WHERE workspace_id = ?`, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("spawn: slots: %w", err)
		}
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if s, err := bindings.ParseSlot(text); err == nil && s.Tab > maxTab {
				maxTab = s.Tab
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	var out []Target
	i := 0
	for _, w := range req.Agents {
		for n := 0; n < w.Count; n++ {
			pane := i%req.PerTab + 1
			if pane == 1 {
				maxTab++
			}
			out = append(out, Target{Index: i, Kind: w.Kind, Model: w.Model, Effort: effortOf(req, w), Slot: fmt.Sprintf("%d.%d", maxTab, pane),
				Status: StatusPlanned, newTab: pane == 1})
			i++
		}
	}
	return out, nil
}

// fingerprint is the claimed request: the caller's input, not the names
// Agent Mail will give.
func fingerprint(req Request) map[string]any {
	return map[string]any{"session": req.Session, "add": req.Add, "project": req.ProjectRoot, "agents": req.Agents, "effort": req.Effort,
		"label": req.Label, "group": req.Group, "per_tab": req.PerTab, "prompt": req.Prompt, "no_kickoff": req.NoKickoff,
		"assign": req.Assign, "stagger": req.StaggerMode, "delay_ms": req.StaggerDelay.Milliseconds(),
		"force": req.Force, "allow_unguarded": req.AllowUnguarded, "require_observations": req.RequireObservations}
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

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullGen is generation 1 for a binding this spawn created.
func nullGen(bindingID string) any {
	if bindingID == "" {
		return nil
	}
	return 1
}

func ptr[T any](v T) *T { return &v }

// The kickoff (plan-v8 2.2 step 6).
const (
	defaultKickoff = "default_new_agent"
	assignKey      = "next_bead"
)

// kickoff is the kickoff's text: the operator's --prompt goes out as
// typed, as a plain send does; the palette prompts (default_new_agent,
// next_bead) are templates whose per-agent placeholders are filled.
type kickoff struct {
	raw     string
	palette string
}

func (k kickoff) empty() bool { return k.raw == "" && k.palette == "" }

// kickoffBody reads the kickoff: --prompt, or ACFS's default_new_agent
// palette prompt, with next_bead appended after the operator's yes
// (--assign). It refuses before anything is created when a palette
// prompt cannot be found or has a placeholder nothing fills.
func kickoffBody(req Request) (kickoff, error) {
	var k kickoff
	if req.NoKickoff {
		if req.Assign {
			return k, errors.New("--assign needs a kickoff")
		}
		return k, nil
	}
	loader := templates.NewLoader(req.ProjectRoot)
	var palette []string
	k.raw = req.Prompt
	if k.raw == "" {
		t, err := loader.Load(defaultKickoff)
		if err != nil {
			return k, fmt.Errorf("the kickoff: %w (give --prompt)", err)
		}
		palette = append(palette, t.Body)
	}
	if req.Assign {
		t, err := loader.Load(assignKey)
		if err != nil {
			return k, fmt.Errorf("--assign: %w", err)
		}
		palette = append(palette, t.Body)
	}
	k.palette = strings.Join(palette, "\n\n")
	check := &templates.Template{Name: "kickoff", Body: k.palette}
	if _, err := check.ExecuteShared(templates.ExecutionContext{Session: req.Session, Project: req.ProjectRoot}); err != nil {
		return k, fmt.Errorf("the kickoff: %w", err)
	}
	return k, nil
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
