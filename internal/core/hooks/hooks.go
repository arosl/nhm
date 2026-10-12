// Package hooks is nhm's side of the hooks (plan-v8 2.7): the agent CLIs'
// PostToolUse and SessionStart hooks, and herdr's plugin event hooks
// pane.agent_detected and pane.agent_status_changed.
//
// One rule: hooks report evidence; nhm reconciles it. An agent hook
// writes a bounded candidate row and then runs the pane's reconcile. The
// reconcile verifies each pending candidate against herdr (the pane's
// agent and its native session) and only then creates, continues or
// displaces a binding. A plugin event runs the same reconcile. herdr
// drops plugin hooks under a burst (ACFS exp.1), so a hook is a hint:
// the next hook or nhm maintain catches up.
//
// What herdr does with names (ACFS exp.4): a name that agent.start gave
// survives /compact and /clear while the agent runs; a name that
// agent.rename gave is cleared at a new session; every name goes when the
// agent process exits. The reconcile applies the binding's name again
// whenever herdr shows another, so a restart in place keeps its name.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds/claude"
	"github.com/arosl/nhm/internal/integrations/kinds/codex"
	"github.com/arosl/nhm/internal/store"
)

// Herdr is what the hooks need from herdr; *herdr.Client has it.
type Herdr interface {
	bindings.Herdr
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	PaneRename(ctx context.Context, paneID, label string) (herdr.PaneInfo, error)
	TabRename(ctx context.Context, tabID, label string) (herdr.TabInfo, error)
}

// Env is an enrolled instance, opened, and the hook's environment.
type Env struct {
	DB          *store.DB
	Bindings    *bindings.Store
	Herdr       Herdr
	InstanceDir string
	// ConfigHome holds Agent Mail's identity files; "" means
	// agentmail.ConfigHome().
	ConfigHome string
	Getenv     func(string) string
	Now        func() time.Time
	// LockWait bounds the wait for the owner lock; a hook never waits
	// long (0 means one second).
	LockWait time.Duration
	// Settle is how long a hook polls herdr for its session to appear
	// (0 means 700 ms).
	Settle time.Duration
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return ""
}

// AgentHook is one call of `nhm-hook posttooluse` or `nhm-hook
// sessionstart`.
type AgentHook struct {
	Payload AgentPayload
	// EventID identifies this hook call; with the pane and the event name
	// it makes a candidate unique.
	EventID string
}

// Result is what a hook did.
type Result struct {
	// Skipped says why the hook did nothing; "" when it ran.
	Skipped string `json:"skipped,omitempty"`
	// Candidate is the row the hook wrote; 0 when none.
	Candidate int64 `json:"candidate,omitempty"`
	// Reminder is the text a SessionStart hook prints into the agent's
	// context: who the agent is.
	Reminder string    `json:"reminder,omitempty"`
	Report   Reconcile `json:"reconcile"`
}

// Reconcile is what a pane's reconcile did.
type Reconcile struct {
	PaneID   string   `json:"pane_id"`
	Ran      bool     `json:"ran"`
	Owner    string   `json:"owner,omitempty"`
	Binding  string   `json:"binding_id,omitempty"`
	Actions  []string `json:"actions"`
	Warnings []string `json:"warnings"`
}

func (r *Reconcile) act(format string, args ...any) {
	r.Actions = append(r.Actions, fmt.Sprintf(format, args...))
}

func (r *Reconcile) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// RunAgentHook handles an agent CLI's hook: it filters the call, records
// a candidate and reconciles the pane.
func RunAgentHook(ctx context.Context, env Env, h AgentHook) (Result, error) {
	p := h.Payload
	pane := env.getenv("HERDR_PANE_ID")
	if pane == "" {
		return Result{Skipped: "not in a herdr pane"}, nil
	}
	// A subagent passes the session check, since it shares its parent's
	// session; it must never bind the pane or speak for its parent.
	if p.AgentID != "" || p.AgentType != "" {
		return Result{Skipped: "subagent call (agent_id " + p.AgentID + ")"}, nil
	}
	facts := CandidateFacts{HookEventName: p.HookEventName, SessionID: p.SessionID, Source: p.Source,
		Model: p.Model, Entrypoint: env.getenv("CLAUDE_CODE_ENTRYPOINT")}
	var token string
	switch p.HookEventName {
	case "PostToolUse":
		if RegistrationTool(p.ToolName) == "" {
			return Result{Skipped: "not an Agent Mail registration"}, nil
		}
		reg, err := ParseRegistration(p)
		if err != nil {
			return Result{Skipped: err.Error()}, nil
		}
		facts.ToolName, facts.MailName, facts.MailProject, token = p.ToolName, reg.Name, reg.ProjectKey, reg.Token
	case "SessionStart", "PreCompact", "PostCompact":
		// A nested `claude -p` inherits the pane's environment; drop it
		// before any work (ACFS exp.4).
		if claude.Nested(claude.Event{Entrypoint: facts.Entrypoint}) {
			return Result{Skipped: "nested run (entrypoint " + facts.Entrypoint + ")"}, nil
		}
	default:
		return Result{Skipped: p.HookEventName + " is not a hook nhm reads"}, nil
	}
	c := Candidate{PaneID: pane, LaunchID: env.getenv("NHM_LAUNCH_ID"), Session: p.SessionID,
		HookEvent: p.HookEventName, HookEventID: h.EventID, Facts: facts, ReceivedAt: env.now()}
	id, err := recordCandidate(ctx, env.DB.SQL(), c)
	if err != nil {
		return Result{}, err
	}
	res := Result{Candidate: id}
	var early []string
	if token != "" {
		ref := fmt.Sprintf("cand-%d", id)
		if err := store.WriteSecret(env.InstanceDir, ref, token); err != nil {
			early = append(early, "registration token not kept: "+err.Error())
		} else if err := setTokenRef(ctx, env, id, ref); err != nil {
			early = append(early, "registration token not kept: "+err.Error())
			_ = store.DeleteSecret(env.InstanceDir, ref)
		}
	}
	// Pruning keeps the table bounded under any owner and changes no
	// binding. It is a write, so it runs on one candidate in pruneEvery,
	// not on every hook.
	if id%pruneEvery == 0 {
		if refs, err := prune(ctx, env.DB.SQL(), env.now()); err != nil {
			early = append(early, err.Error())
		} else {
			for _, ref := range refs {
				_ = store.DeleteSecret(env.InstanceDir, ref)
			}
		}
	}
	res.Report, err = ReconcilePane(ctx, env, pane)
	res.Report.Warnings = append(early, res.Report.Warnings...)
	if err != nil {
		return res, err
	}
	if p.HookEventName == "SessionStart" {
		res.Reminder = reminder(ctx, env, pane, p.SessionID)
	}
	return res, nil
}

func setTokenRef(ctx context.Context, env Env, id int64, ref string) error {
	_, err := env.DB.SQL().ExecContext(ctx, `UPDATE hook_candidates
		SET payload_json = json_set(payload_json, '$.token_ref', ?) WHERE candidate_id = ?`, ref, id)
	return err
}

// reminder is the line a Claude agent reads at SessionStart when its
// pane holds an active binding for this session. It costs context
// tokens; the pane name costs none. Codex gets no reminder: what Codex
// does with a SessionStart hook's stdout is not proven.
func reminder(ctx context.Context, env Env, pane, session string) string {
	b, err := env.Bindings.ActiveByPane(ctx, pane)
	if err != nil || b.Kind != "claude" {
		return ""
	}
	if b.NativeSession == nil || b.NativeSession.Value != session {
		return ""
	}
	if b.HerdrAlias != "" {
		return fmt.Sprintf("You are %s (herdr %s).", b.MailName, b.HerdrAlias)
	}
	return fmt.Sprintf("You are %s.", b.MailName)
}

// RunPluginEvent handles pane.agent_detected or pane.agent_status_changed:
// it reconciles the event's pane. herdr runs at most 32 plugin commands at
// a time, so this does one pane's work and returns.
func RunPluginEvent(ctx context.Context, env Env, ev PaneEvent) (Reconcile, error) {
	pane := ev.PaneID
	if pane == "" {
		pane = env.getenv("HERDR_PANE_ID")
	}
	if pane == "" {
		return Reconcile{Actions: []string{}, Warnings: []string{"the event names no pane"}}, nil
	}
	return ReconcilePane(ctx, env, pane)
}

// ReconcilePane verifies a pane's pending candidates against herdr and
// keeps its binding true: it binds a registration, records a
// continuation, displaces a binding whose occupant changed, and applies
// the binding's herdr name again. It changes bindings only when nhm owns
// the instance; otherwise the candidates wait.
func ReconcilePane(ctx context.Context, env Env, pane string) (Reconcile, error) {
	r := Reconcile{PaneID: pane, Actions: []string{}, Warnings: []string{}}
	wait := env.LockWait
	if wait <= 0 {
		wait = time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	err := core.WithOwnerShared(lctx, env.InstanceDir, func(o core.Owner) error {
		r.Owner = o.Owner
		if o.Owner != core.OwnerNhm {
			return nil
		}
		r.Ran = true
		return reconcile(ctx, env, pane, &r)
	})
	if errors.Is(err, core.ErrOwnerLockTimeout) {
		r.warn("owner lock busy; the candidates wait for the next reconcile")
		return r, nil
	}
	return r, err
}

// paneState is herdr's view of a pane, read once per reconcile and again
// while herdr catches up with a new session.
type paneState struct {
	agent    herdr.AgentInfo
	hasAgent bool
}

func (p paneState) session() string {
	if !p.hasAgent || p.agent.AgentSession == nil {
		return ""
	}
	return p.agent.AgentSession.Value
}

func readPane(ctx context.Context, h Herdr, pane string) (paneState, error) {
	a, err := h.AgentGet(ctx, pane)
	if err != nil {
		if herdr.ErrorCode(err) == herdr.CodeAgentNotFound || herdr.ErrorCode(err) == herdr.CodePaneNotFound {
			return paneState{}, nil
		}
		return paneState{}, err
	}
	return paneState{agent: a, hasAgent: a.Agent != ""}, nil
}

func reconcile(ctx context.Context, env Env, pane string, r *Reconcile) error {
	cands, err := pendingCandidates(ctx, env.DB.SQL(), pane)
	if err != nil {
		return err
	}
	st, err := readPane(ctx, env.Herdr, pane)
	if err != nil {
		return fmt.Errorf("hooks: herdr: %w", err)
	}
	// herdr's own SessionStart hook reports the session beside nhm's; give
	// it a moment when a fresh candidate names a session herdr lacks.
	st, err = settle(ctx, env, pane, st, cands)
	if err != nil {
		return fmt.Errorf("hooks: herdr: %w", err)
	}
	b, err := env.Bindings.ActiveByPane(ctx, pane)
	var bp *bindings.Binding
	switch {
	case err == nil:
		bp = &b
	case errors.Is(err, bindings.ErrNotFound):
	default:
		return err
	}
	for _, c := range cands {
		result, err := apply(ctx, env, c, st, &bp, r)
		if errors.Is(err, errPending) {
			continue
		}
		if err != nil {
			result = "error: " + err.Error()
			r.warn("candidate %d: %v", c.ID, err)
		}
		if c.Facts.TokenRef != "" {
			_ = store.DeleteSecret(env.InstanceDir, c.Facts.TokenRef)
		}
		if err := closeCandidate(ctx, env.DB.SQL(), c.ID, result, env.now()); err != nil {
			r.warn("%v", err)
		}
	}
	if bp == nil {
		return nil
	}
	r.Binding = bp.ID
	return keepTrue(ctx, env, st, bp, r)
}

func settle(ctx context.Context, env Env, pane string, st paneState, cands []Candidate) (paneState, error) {
	// The hook's budget is two seconds: this wait, the lock wait and a
	// few socket calls must fit in it.
	wait := env.Settle
	if wait <= 0 {
		wait = 700 * time.Millisecond
	}
	deadline := time.Now().Add(wait)
	for {
		lagging := false
		for _, c := range cands {
			if c.Session != "" && c.Session != st.session() && env.now().Sub(c.ReceivedAt) < settleWindow {
				lagging = true
			}
		}
		if !lagging || time.Now().After(deadline) {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, nil
		case <-time.After(100 * time.Millisecond):
		}
		next, err := readPane(ctx, env.Herdr, pane)
		if err != nil {
			return st, err
		}
		st = next
	}
}

// apply verifies one candidate and acts on it. errPending leaves it for a
// later reconcile.
func apply(ctx context.Context, env Env, c Candidate, st paneState, bp **bindings.Binding, r *Reconcile) (string, error) {
	if !st.hasAgent {
		return "", errPending
	}
	// The pane's session must be the hook's: that also rejects a nested
	// run that inherited the pane's environment. Claude Code and Codex
	// always send a session id; a payload without one proves nothing.
	if c.Session == "" {
		return "ignored: the hook sent no session id", nil
	}
	if st.session() != c.Session {
		if env.now().Sub(c.ReceivedAt) < settleWindow {
			return "", errPending
		}
		return fmt.Sprintf("ignored: herdr reports session %q for the pane, the hook %q (a nested run?)", st.session(), c.Session), nil
	}
	switch c.HookEvent {
	case "PostToolUse":
		return applyRegistration(ctx, env, c, st, bp, r)
	default:
		return applyContinuation(ctx, env, c, st, bp, r)
	}
}

func applyRegistration(ctx context.Context, env Env, c Candidate, st paneState, bp **bindings.Binding, r *Reconcile) (string, error) {
	name, projectKey := c.Facts.MailName, c.Facts.MailProject
	if b := *bp; b != nil {
		if strings.EqualFold(b.MailName, name) && sameProject(b.MailProjectKey, projectKey) {
			// A hook stopped between the create and its writes leaves the
			// candidate pending; finish those writes now.
			repairBinding(env, c, *b, r)
			return "already bound as " + b.ID, nil
		}
		return fmt.Sprintf("refused: the pane is bound to %s (%s); use nhm adopt", b.MailName, b.ID), nil
	}
	root := canonical(projectKey)
	occ := occupant(ctx, env, st, name, c.Facts.Model)
	occ.Evidence = append([]bindings.Evidence{{Kind: "posttooluse",
		Detail: fmt.Sprintf("%s returned %s for %s in session %s", c.Facts.ToolName, name, root, c.Session)}}, occ.Evidence...)
	b, err := env.Bindings.Create(ctx, bindings.New{MailName: name, MailProjectKey: root, ProjectID: core.ProjectID(root),
		Provenance: bindings.Adopted, Occupant: occ})
	if errors.Is(err, bindings.ErrIdentityBusy) || errors.Is(err, bindings.ErrPaneBusy) {
		return "refused: " + err.Error(), nil
	}
	if err != nil {
		return "", err
	}
	repairBinding(env, c, b, r)
	*bp = &b
	r.act("bound %s to %s as %s", b.PaneID, b.MailName, b.ID)
	labelPane(ctx, env, b, r)
	return "bound " + b.ID, nil
}

// repairBinding makes the writes that follow a registration's bind, when
// they are missing: the token under the binding id, and Agent Mail's
// identity file for the pane. It is idempotent, so a reconcile can run
// it for a binding that a stopped hook created.
func repairBinding(env Env, c Candidate, b bindings.Binding, r *Reconcile) {
	if c.Facts.TokenRef != "" {
		if _, err := store.ReadSecret(env.InstanceDir, b.ID); errors.Is(err, store.ErrNoSecret) {
			if tok, err := store.ReadSecret(env.InstanceDir, c.Facts.TokenRef); err == nil {
				if err := store.WriteSecret(env.InstanceDir, b.ID, tok); err != nil {
					r.warn("registration token for %s not kept: %v", b.ID, err)
				}
			}
		}
	}
	rec, ok, err := agentmail.ReadIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID)
	if err == nil && ok && strings.EqualFold(rec.Name, b.MailName) {
		return
	}
	if _, err := agentmail.WriteIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID, b.MailName, env.now()); err != nil {
		r.warn("Agent Mail identity file: %v", err)
	}
}

func applyContinuation(ctx context.Context, env Env, c Candidate, st paneState, bp **bindings.Binding, r *Reconcile) (string, error) {
	b := *bp
	if b == nil {
		return "no binding for the pane", nil
	}
	bound := ""
	if b.NativeSession != nil {
		bound = b.NativeSession.Value
	}
	launch := c.LaunchID != "" && c.LaunchID == b.LaunchID
	var verdict, reason string
	var compacted, resumed, take bool
	switch b.Kind {
	case "claude":
		d := claude.Classify(claude.Event{HookEvent: c.HookEvent, Source: c.Facts.Source, SessionID: c.Session,
			Entrypoint: c.Facts.Entrypoint}, bound, launch)
		verdict, reason, compacted, resumed, take = d.Verdict, d.Reason, d.Compacted, d.Resumed, d.TakeSession
	case "codex":
		d := codex.Classify(codex.Event{HookEvent: c.HookEvent, Source: c.Facts.Source, SessionID: c.Session}, bound, launch)
		verdict, reason, compacted, resumed, take = d.Verdict, d.Reason, d.Compacted, d.Resumed, d.TakeSession
	default:
		return "ignored: no continuation rule for kind " + b.Kind, nil
	}
	// Both rules spell their verdicts "continue", "displace", "ignore".
	switch verdict {
	case verdictContinue:
		cont := bindings.Continuation{Evidence: []bindings.Evidence{{Kind: strings.ToLower(c.HookEvent), Detail: reason}}}
		at := c.ReceivedAt
		if compacted {
			cont.CompactedAt = &at
		}
		if resumed {
			cont.ResumedAt = &at
		}
		if take && st.agent.AgentSession != nil {
			s := st.agent.AgentSession
			cont.NativeSession = &bindings.NativeSession{Source: s.Source, Agent: s.Agent, Kind: s.Kind, Value: s.Value}
		}
		nb, err := env.Bindings.Continue(ctx, b.ID, b.Generation, cont)
		if err != nil {
			return "", err
		}
		*bp = &nb
		r.act("%s continues in %s: %s", nb.MailName, nb.PaneID, reason)
		return "continue: " + reason, nil
	case verdictDisplace:
		if err := displace(ctx, env, b, st, reason, r); err != nil {
			return "", err
		}
		*bp = nil
		return "displace: " + reason, nil
	default:
		return "ignored: " + reason, nil
	}
}

// The verdicts of both rules; TestRulesSpellVerdictsAlike keeps the two
// packages in step.
const (
	verdictContinue = claude.Continue
	verdictDisplace = claude.Displace
)

// keepTrue runs after the candidates: it displaces a binding whose pane
// now holds another kind, records a restart in place (the same session,
// a new process), and applies the herdr name again.
func keepTrue(ctx context.Context, env Env, st paneState, b *bindings.Binding, r *Reconcile) error {
	if !st.hasAgent {
		// The agent left; a resume in place may follow. Nothing to do yet.
		return nil
	}
	if st.agent.Agent != b.Kind {
		return displace(ctx, env, b, st, fmt.Sprintf("herdr sees a %s agent, the binding has %s", st.agent.Agent, b.Kind), r)
	}
	if b.NativeSession == nil || st.session() != b.NativeSession.Value {
		// A new session waits for its SessionStart candidate; a session
		// herdr has not reported yet (Codex before its first turn) too.
		return nil
	}
	if pid, start, moved := restarted(ctx, env.Herdr, b); moved {
		nb, err := env.Bindings.Continue(ctx, b.ID, b.Generation, bindings.Continuation{ForegroundPID: pid, ForegroundStart: start,
			Evidence: []bindings.Evidence{{Kind: "restart", Detail: fmt.Sprintf("session %s, now pid %d", b.NativeSession.Value, pid)}}})
		if err != nil {
			return err
		}
		*b = nb
		r.act("%s restarted in %s as pid %d", b.MailName, b.PaneID, pid)
	}
	if b.HerdrAlias != "" && st.agent.Name != b.HerdrAlias {
		if _, err := env.Herdr.AgentRename(ctx, b.PaneID, b.HerdrAlias); err != nil {
			r.warn("herdr name %s not applied to %s: %v", b.HerdrAlias, b.PaneID, err)
			return nil
		}
		r.act("named %s %s", b.PaneID, b.HerdrAlias)
	}
	return nil
}

// displace marks a binding displaced and takes its marks off the pane:
// Agent Mail's identity file, and the herdr name, which agent.start
// names keep across /clear (ACFS exp.4) and which would send mail
// addressed to the old identity to the new occupant.
func displace(ctx context.Context, env Env, b *bindings.Binding, st paneState, reason string, r *Reconcile) error {
	if _, err := env.Bindings.Displace(ctx, b.ID, b.Generation, reason); err != nil {
		return err
	}
	r.act("displaced %s (%s) from %s: %s", b.MailName, b.ID, b.PaneID, reason)
	if _, err := agentmail.RemoveIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID, b.MailName); err != nil {
		r.warn("Agent Mail identity file: %v", err)
	}
	if b.HerdrAlias != "" && st.hasAgent && st.agent.Name == b.HerdrAlias {
		if _, err := env.Herdr.AgentRename(ctx, b.PaneID, ""); err != nil {
			r.warn("herdr name %s not cleared from %s: %v", b.HerdrAlias, b.PaneID, err)
		}
	}
	return nil
}

// occupant collects the facts of the agent in a pane for a new binding.
func occupant(ctx context.Context, env Env, st paneState, mailName, model string) bindings.Occupant {
	a := st.agent
	occ := bindings.Occupant{WorkspaceID: a.WorkspaceID, TabID: a.TabID, PaneID: a.PaneID, Kind: a.Agent, Model: model,
		LaunchID: env.getenv("NHM_LAUNCH_ID")}
	if alias, err := bindings.Alias(mailName); err == nil {
		occ.HerdrAlias = alias
	}
	occ.Evidence = []bindings.Evidence{{Kind: "herdr_agent", Detail: fmt.Sprintf("%s agent in %s", a.Agent, a.PaneID)}}
	if s := a.AgentSession; s != nil {
		occ.NativeSession = &bindings.NativeSession{Source: s.Source, Agent: s.Agent, Kind: s.Kind, Value: s.Value}
	}
	occ.ForegroundPID, occ.ForegroundStart = processFacts(ctx, env.Herdr, a.PaneID, a.Agent)
	return occ
}

// labelPane names a new binding's pane "<Name> <model>", and its tab
// "<Name>" when the tab holds only that pane. Failures are warnings.
func labelPane(ctx context.Context, env Env, b bindings.Binding, r *Reconcile) {
	label := b.MailName
	if b.Model != "" {
		label += " " + b.Model
	}
	if _, err := env.Herdr.PaneRename(ctx, b.PaneID, label); err != nil {
		r.warn("pane label: %v", err)
	}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		r.warn("tab label: %v", err)
		return
	}
	for _, t := range snap.Tabs {
		if t.TabID == b.TabID && t.PaneCount == 1 {
			if _, err := env.Herdr.TabRename(ctx, t.TabID, b.MailName); err != nil {
				r.warn("tab label: %v", err)
			}
		}
	}
}

// processFacts reads the agent's process in a pane and its start time:
// the foreground process named like the kind, else the first one. Zeros
// when herdr or /proc cannot tell.
func processFacts(ctx context.Context, h Herdr, pane, kind string) (int64, int64) {
	info, err := h.PaneProcessInfo(ctx, pane)
	if err != nil || len(info.ForegroundProcesses) == 0 {
		return 0, 0
	}
	p := info.ForegroundProcesses[0]
	for _, fp := range info.ForegroundProcesses {
		if isKind(fp, kind) {
			p = fp
			break
		}
	}
	pid := int64(p.PID)
	start, err := bindings.ProcStart(pid)
	if err != nil {
		return pid, 0
	}
	return pid, start
}

// isKind reports whether a process is the agent of a kind. The name
// alone does not tell: Claude Code's native binary is named after its
// version ("2.1.296") and runs through a "claude" link, so argv[0] and
// the command line count too.
func isKind(p herdr.Process, kind string) bool {
	if kind == "" {
		return false
	}
	if p.Name == kind || strings.HasPrefix(p.Name, kind) {
		return true
	}
	if len(p.Argv) > 0 && filepath.Base(p.Argv[0]) == kind {
		return true
	}
	first, _, _ := strings.Cut(p.Cmdline, " ")
	return filepath.Base(first) == kind
}

// restarted reports whether the binding's process left the pane while
// its session stayed (a restart in place, such as `claude --continue`),
// with the new process. A tool the agent runs may join the foreground;
// as long as the recorded process is still there, nothing moved.
func restarted(ctx context.Context, h Herdr, b *bindings.Binding) (int64, int64, bool) {
	info, err := h.PaneProcessInfo(ctx, b.PaneID)
	if err != nil || len(info.ForegroundProcesses) == 0 {
		return 0, 0, false
	}
	for _, fp := range info.ForegroundProcesses {
		if int64(fp.PID) != b.ForegroundPID {
			continue
		}
		if b.ForegroundStart == 0 {
			return 0, 0, false
		}
		if start, err := bindings.ProcStart(b.ForegroundPID); err != nil || start == b.ForegroundStart {
			return 0, 0, false
		}
	}
	// The recorded process is gone, its pid was reused, or none was
	// recorded.
	pid, start := processFacts(ctx, h, b.PaneID, b.Kind)
	return pid, start, pid != 0
}

func canonical(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		if abs, err := filepath.Abs(real); err == nil {
			return abs
		}
	}
	return filepath.Clean(p)
}

func sameProject(a, b string) bool { return canonical(a) == canonical(b) }
