package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// Herdr is what lifecycle needs from herdr; *herdr.Client has it.
type Herdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	AgentList(ctx context.Context) ([]herdr.AgentInfo, error)
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentStart(ctx context.Context, p herdr.AgentStartParams) (herdr.AgentStarted, error)
	AgentWait(ctx context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error)
	AgentPrompt(ctx context.Context, p herdr.AgentPromptParams) (herdr.AgentInfo, error)
	AgentRename(ctx context.Context, target, name string) (herdr.AgentInfo, error)
	PaneProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
	PaneReportMetadata(ctx context.Context, p herdr.ReportMetadataParams) error
	PaneClose(ctx context.Context, paneID string) error
	WorkspaceClose(ctx context.Context, workspaceID string) error
}

// Mail is what lifecycle needs from Agent Mail; *agentmail.Client has it.
type Mail interface {
	Call(ctx context.Context, tool string, args, out any) error
}

// Env is what the lifecycle verbs run against.
type Env struct {
	DB          *store.DB
	Bindings    *bindings.Store
	Herdr       Herdr
	Mail        Mail
	Facts       Facts
	InstanceDir string
	// Home is where the kinds keep their transcripts.
	Home string
	// MailConfigHome holds Agent Mail's pane identity files.
	MailConfigHome string
	// Caller is the calling pane, never retired.
	Caller string
	// Send delivers recycle's kickoff (send.Send with the instance's env).
	Send func(ctx context.Context, req send.Request) (send.Result, error)
	// Handoff composes recycle's handoff; nil sends none.
	Handoff HandoffFunc
	// LaunchSettings is the hook settings JSON a kind with a settings
	// flag gets at its start (spawn.HookSettings).
	LaunchSettings string
	// Index indexes a preserved transcript in CASS (plan-v8 2.13); nil
	// skips it.
	Index     func(ctx context.Context, path, project, key string) (string, error)
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

// RetireRequest names the agent and the options.
type RetireRequest struct {
	// Target is a mail name, a herdr name or a pane id.
	Target     string
	ProjectKey string
	// KeepIdentity leaves the Agent Mail identity active.
	KeepIdentity bool
	// CleanupWorktrees removes the agent's clean, merged worktrees; they
	// are only listed otherwise.
	CleanupWorktrees bool
	DryRun           bool
	OperationID      string
	// Reason is recorded: retire, reap or kill.
	Reason string
}

// RetireResult is what retire did, or why it refused.
type RetireResult struct {
	OperationID     string     `json:"operation_id,omitempty"`
	MailName        string     `json:"mail_name"`
	BindingID       string     `json:"binding_id"`
	PaneID          string     `json:"pane_id,omitempty"`
	Refusals        []string   `json:"refusals"`
	Actions         []string   `json:"actions"`
	Done            bool       `json:"done"`
	DryRun          bool       `json:"dry_run,omitempty"`
	Transcript      *Preserved `json:"transcript,omitempty"`
	ClosedBeads     []string   `json:"closed_beads,omitempty"`
	IdentityRetired bool       `json:"identity_retired"`
	Worktrees       []string   `json:"worktrees,omitempty"`
	Removed         []string   `json:"worktrees_removed,omitempty"`
	Warnings        []string   `json:"warnings"`
}

// ErrRefused means a retire check failed; the result lists why.
var ErrRefused = errors.New("refused")

// unsettled are the agent states retire never acts on.
var unsettled = map[herdr.AgentStatus]bool{herdr.StatusWorking: true, herdr.StatusBlocked: true, herdr.StatusUnknown: true}

// retirePlan is what the checks found.
type retirePlan struct {
	b        bindings.Binding
	agent    *herdr.AgentInfo
	merged   []string
	token    string
	original string
}

// Retire closes an agent's pane, preserves its transcript and retires its
// Agent Mail identity, after every check of plan-v8 2.10 passed. A
// refusal changes nothing and returns ErrRefused with the reasons.
func Retire(ctx context.Context, env Env, req RetireRequest) (RetireResult, error) {
	res := RetireResult{Refusals: []string{}, Actions: []string{}, Warnings: []string{}, DryRun: req.DryRun}
	// A retry of an operation reports its outcome, before any check
	// (which would refuse a binding the first run retired).
	if !req.DryRun {
		if op, ok, err := priorOperation(ctx, env, req.OperationID); err != nil {
			return res, err
		} else if ok {
			res.OperationID = op.ID
			res.Done = op.Status == "done"
			return res, replayed(op)
		}
	}
	b, err := find(ctx, env, req.Target, req.ProjectKey)
	if err != nil {
		return res, err
	}
	res.MailName, res.BindingID, res.PaneID = b.MailName, b.ID, b.PaneID
	if req.Reason == "" {
		req.Reason = "retire"
	}
	// The binding's lock: a recycle or a send in progress refuses.
	lctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	unlock, lerr := send.LockTarget(lctx, env.InstanceDir, b.ID)
	cancel()
	if lerr != nil {
		res.Refusals = append(res.Refusals, b.MailName+" is being recycled or sent to; try again when that ends")
		return res, ErrRefused
	}
	defer unlock()
	// Under the lock, the binding as it is now: the plan carries its
	// generation to the compare-and-set at the end.
	if b, err = env.Bindings.Get(ctx, b.ID); err != nil {
		return res, err
	}

	plan := check(ctx, env, req, b, &res)
	if len(res.Refusals) > 0 {
		return res, ErrRefused
	}
	if req.DryRun {
		return res, nil
	}
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: req.Reason,
		Scope: map[string]string{"binding_id": b.ID, "mail_project_key": b.MailProjectKey},
		Request: map[string]any{"binding_id": b.ID, "generation": b.Generation, "keep_identity": req.KeepIdentity,
			"cleanup_worktrees": req.CleanupWorktrees}}, env.now())
	if err != nil {
		return res, err
	}
	if claim.Existing {
		res.OperationID, res.Done = claim.Operation.ID, claim.Operation.Status == "done"
		return res, replayed(claim.Operation)
	}
	res.OperationID = claim.Operation.ID
	if err := execute(ctx, env, req, plan, &res); err != nil {
		_ = env.DB.SetOperationStatus(ctx, res.OperationID, "failed", true, env.now())
		return res, err
	}
	res.Done = true
	return res, env.DB.SetOperationStatus(ctx, res.OperationID, "done", true, env.now())
}

// index indexes a transcript in place in CASS (exp.5), so the session
// becomes memory; a failure is a warning: the preserved copy stays.
func index(ctx context.Context, env Env, b bindings.Binding, path string, warnings *[]string) {
	if env.Index == nil {
		return
	}
	key := b.ID
	if b.NativeSession != nil {
		key += "-" + b.NativeSession.Value
	}
	if outcome, err := env.Index(ctx, path, b.MailProjectKey, key); err != nil {
		*warnings = append(*warnings, "cass index: "+err.Error())
	} else if outcome != "indexed" {
		*warnings = append(*warnings, "cass index: "+outcome)
	}
}

// namesAgent reports a worktree path with a part that is the agent's
// name: a path element, or one of its words split at '-', '_' and '.'
// ("/data/tmp/icyknoll-check.x1" names icyknoll; "/data/tmp/icyknollx"
// does not).
func namesAgent(path, alias string) bool {
	for _, el := range strings.Split(strings.ToLower(path), "/") {
		for _, word := range strings.FieldsFunc(el, func(r rune) bool { return r == '-' || r == '_' || r == '.' }) {
			if word == alias {
				return true
			}
		}
	}
	return false
}

// priorOperation reads the operation a retry names; ok is false for a new
// or empty id.
func priorOperation(ctx context.Context, env Env, id string) (store.Operation, bool, error) {
	if id == "" {
		return store.Operation{}, false, nil
	}
	op, err := env.DB.Operation(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.Operation{}, false, nil
	case err != nil:
		return store.Operation{}, false, err
	}
	return op, true, nil
}

// replayed is a retry's outcome: nil when the operation is done, else an
// error naming its state, so the retry never exits 0 for work not done.
func replayed(op store.Operation) error {
	switch {
	case op.Status == "done":
		return nil
	case op.CompletedAt == nil:
		return fmt.Errorf("operation %s (%s) is still in progress (%s), or was cut off: see nhm admin recover inspect", op.ID, op.Verb, op.Status)
	default:
		return fmt.Errorf("operation %s (%s) ended %s; nothing done again: retry with a new --op-id", op.ID, op.Verb, op.Status)
	}
}

func find(ctx context.Context, env Env, target, projectKey string) (bindings.Binding, error) {
	if strings.HasPrefix(target, "bnd-") {
		return env.Bindings.Get(ctx, target)
	}
	r, err := bindings.Resolve(ctx, env.Bindings, nil, bindings.Query{Target: target, ProjectKey: projectKey}, os.Getenv)
	if err != nil {
		return bindings.Binding{}, err
	}
	return r.Binding, nil
}

// check runs the refusals of plan-v8 2.10 and ACFS's Bash, and lists the
// actions. Any read that fails refuses.
func check(ctx context.Context, env Env, req RetireRequest, b bindings.Binding, res *RetireResult) retirePlan {
	plan := retirePlan{b: b}
	refuse := func(format string, args ...any) { res.Refusals = append(res.Refusals, fmt.Sprintf(format, args...)) }
	act := func(format string, args ...any) { res.Actions = append(res.Actions, fmt.Sprintf(format, args...)) }

	switch b.Status {
	case bindings.Active:
	case bindings.Displaced:
		refuse("%s is displaced (%s): adopt or import it first", b.MailName, b.StatusReason)
	default:
		refuse("%s is already %s", b.MailName, b.Status)
	}
	alias := b.HerdrAlias
	if alias == "" {
		alias = strings.ToLower(b.MailName)
	}
	if b.PaneID != "" {
		a, err := env.Herdr.AgentGet(ctx, b.PaneID)
		switch code := herdr.ErrorCode(err); {
		case err == nil:
			plan.agent = &a
			if a.Agent != "" && a.Agent != b.Kind {
				refuse("pane %s holds a %s agent; the binding is %s", b.PaneID, a.Agent, b.Kind)
			}
			if unsettled[a.AgentStatus] {
				refuse("%s is %s; retire it once its turn is done", b.MailName, a.AgentStatus)
			}
			act("close pane %s", b.PaneID)
		case code == herdr.CodePaneNotFound:
			res.Warnings = append(res.Warnings, "pane "+b.PaneID+" is gone; nothing to close")
		case code == herdr.CodeAgentNotFound:
			// herdr lost track of the agent, or it exited: only a shell at
			// its prompt is safe to leave; a process in the foreground may
			// be the agent, working.
			info, perr := env.Herdr.PaneProcessInfo(ctx, b.PaneID)
			switch {
			case perr != nil:
				refuse("herdr has no agent in pane %s, and its processes cannot be read: %v", b.PaneID, perr)
			case info.ShellPID == nil || info.ForegroundProcessGroupID == nil || *info.ForegroundProcessGroupID != *info.ShellPID:
				refuse("herdr has no agent in pane %s, but a process runs in its foreground: it may be %s, working", b.PaneID, b.MailName)
			default:
				res.Warnings = append(res.Warnings, "herdr has no agent in pane "+b.PaneID+"; its shell stays open")
			}
		default:
			refuse("herdr cannot be read: %v", err)
		}
	}
	if env.Caller != "" && b.PaneID == env.Caller {
		refuse("%s runs in this pane", b.MailName)
	}

	project := b.MailProjectKey
	active, err := env.Facts.Reservations(ctx, project)
	if err != nil {
		refuse("Agent Mail's reservations cannot be read: %v", err)
	}
	var held, others []string
	for _, r := range active {
		if r.Agent == b.MailName {
			held = append(held, r.Path)
		} else {
			others = append(others, r.Path)
		}
	}
	if len(held) > 0 {
		refuse("%s still holds reservations: %s", b.MailName, strings.Join(held, ", "))
	}
	if err == nil {
		ever, err := env.Facts.ReservedEver(ctx, project, b.MailName)
		if err != nil {
			refuse("Agent Mail's reservation history cannot be read: %v", err)
		}
		if len(ever) > 0 {
			dirty, err := env.Facts.DirtyFiles(ctx, project)
			if err != nil {
				refuse("git status cannot be read: %v", err)
			}
			var owned []string
			for _, f := range dirty {
				mine := false
				for _, p := range ever {
					if globMatch(p, f) {
						mine = true
						break
					}
				}
				for _, p := range others {
					if globMatch(p, f) {
						mine = false
						break
					}
				}
				if mine {
					owned = append(owned, f)
				}
			}
			if len(owned) > 0 {
				refuse("uncommitted changes in files %s reserved: %s", b.MailName, strings.Join(owned, ", "))
			}
		}
	}

	// Its worktrees: a path with a part that names the agent exactly.
	wts, err := env.Facts.Worktrees(ctx, project)
	if err != nil {
		refuse("git worktree list cannot be read: %v", err)
	}
	fetched := false
	var fetchErr error
	for _, wt := range wts {
		if wt == project || !namesAgent(wt, alias) {
			continue
		}
		res.Worktrees = append(res.Worktrees, wt)
		// "Merged" is about origin as it is now: fetch once, and refuse
		// when that fails rather than trust a stale origin/main.
		if !fetched {
			fetched, fetchErr = true, env.Facts.Fetch(ctx, project)
		}
		if fetchErr != nil {
			refuse("origin cannot be fetched, so whether worktree %s is merged is unknown: %v", wt, fetchErr)
			continue
		}
		info, err := env.Facts.WorktreeState(ctx, wt)
		switch {
		case err != nil:
			refuse("worktree %s cannot be read: %v", wt, err)
		case info.Dirty:
			refuse("worktree %s has uncommitted changes", wt)
		case !info.Merged:
			refuse("worktree %s is not merged to origin/main", wt)
		case len(info.Ignored) > 0:
			refuse("worktree %s holds ignored files that removing it would delete: %s", wt, strings.Join(info.Ignored, ", "))
		case req.CleanupWorktrees:
			plan.merged = append(plan.merged, wt)
			act("remove merged worktree %s (git worktree remove, never forced)", wt)
		default:
			res.Warnings = append(res.Warnings, "worktree "+wt+" is merged and clean; --cleanup-worktrees removes it")
		}
	}

	beads, err := env.Facts.Assigned(ctx, project, b.MailName)
	if err != nil {
		refuse("br cannot be read: %v", err)
	}
	for _, bead := range beads {
		if !bead.HasHandoff {
			refuse("bead %s is in progress for %s with no handoff comment", bead.ID, b.MailName)
		}
	}

	if path, err := Locate(env.Home, kinds.Lookup(b.Kind), b.NativeSession); err == nil {
		plan.original = path
		act("preserve the transcript %s", path)
	} else {
		res.Warnings = append(res.Warnings, "transcript: "+err.Error())
	}
	if !req.KeepIdentity {
		if tok, err := store.ReadSecret(env.InstanceDir, b.ID); err == nil && tok != "" {
			plan.token = tok
			// retire_agent goes through Agent Mail's server, which the
			// reads above (the am CLI) do not: a server that is down
			// would leave the identity active after the pane is gone.
			if herr := env.Mail.Call(ctx, "health_check", map[string]any{}, nil); herr != nil {
				refuse("Agent Mail's server cannot be reached, so the identity could not be retired: %v", herr)
			}
			act("soft-retire the Agent Mail identity %s (unretire_agent restores it)", b.MailName)
		} else {
			res.Warnings = append(res.Warnings, "no registration token kept for "+b.MailName+
				": the Agent Mail identity stays active; the agent can call retire_agent itself")
		}
	}
	return plan
}

// execute does the retirement after the claim: revalidate, close, capture,
// evidence, identity, binding.
func execute(ctx context.Context, env Env, req RetireRequest, plan retirePlan, res *RetireResult) error {
	b := plan.b
	// Revalidate the occupant immediately before closing.
	if plan.agent != nil {
		a, err := env.Herdr.AgentGet(ctx, b.PaneID)
		if err != nil {
			return fmt.Errorf("revalidate %s: %w", b.PaneID, err)
		}
		if unsettled[a.AgentStatus] || (a.Agent != "" && a.Agent != b.Kind) {
			return fmt.Errorf("%s changed since the checks (now %s %s); nothing closed", b.MailName, a.Agent, a.AgentStatus)
		}
		if err := env.Herdr.PaneClose(ctx, b.PaneID); err != nil && herdr.ErrorCode(err) != herdr.CodePaneNotFound {
			return fmt.Errorf("close pane %s: %w", b.PaneID, err)
		}
	}
	// The final capture, after the agent exited.
	if plan.original != "" {
		env.sleep(ctx, 500*time.Millisecond)
		p, err := Preserve(ctx, env.DB, env.InstanceDir, b, plan.original, req.Reason, env.now())
		if err != nil {
			res.Warnings = append(res.Warnings, "transcript not preserved: "+err.Error())
		} else {
			res.Transcript = &p
			index(ctx, env, b, plan.original, &res.Warnings)
		}
	}
	if closed, err := env.Facts.Closed(ctx, b.MailProjectKey, b.MailName); err == nil {
		res.ClosedBeads = closed
	}
	for _, wt := range plan.merged {
		if err := env.Facts.RemoveWorktree(ctx, b.MailProjectKey, wt); err != nil {
			res.Warnings = append(res.Warnings, "worktree "+wt+" stays: "+err.Error())
			continue
		}
		res.Removed = append(res.Removed, wt)
	}
	if plan.token != "" {
		err := env.Mail.Call(ctx, "retire_agent", map[string]any{"project_key": b.MailProjectKey, "agent_name": b.MailName,
			"registration_token": plan.token}, nil)
		if err != nil {
			res.Warnings = append(res.Warnings, "the Agent Mail identity stays active: "+err.Error())
		} else {
			res.IdentityRetired = true
			_ = store.DeleteSecret(env.InstanceDir, b.ID)
		}
	}
	if env.MailConfigHome != "" && b.PaneID != "" {
		_, _ = agentmail.RemoveIdentity(env.MailConfigHome, b.MailProjectKey, b.PaneID, b.MailName)
	}
	if _, err := env.Bindings.Retire(ctx, b.ID, b.Generation, req.Reason); err != nil {
		return fmt.Errorf("binding %s: %w", b.ID, err)
	}
	return nil
}
