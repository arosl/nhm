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
	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// Herdr is what the hooks need from herdr; *herdr.Client has it.
type Herdr interface {
	bindings.EnvHerdr
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	PaneGet(ctx context.Context, paneID string) (herdr.PaneInfo, error)
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
	// AcfsHome is where ACFS's Bash keeps its state, for the import of
	// Bash-spawned agents; "" means bindings.DefaultAcfsHome().
	AcfsHome string
	Getenv   func(string) string
	Now      func() time.Time
	// LockWait bounds the wait for the owner lock; a hook never waits
	// long (0 means one second).
	LockWait time.Duration
	// Settle is how long a hook polls herdr for its session to appear
	// (0 means 700 ms).
	Settle time.Duration
	// TabLabels makes the reconcile also name a bound agent's tab that
	// still has herdr's default label. nhm maintain's name pass sets it;
	// a hook does not, to keep each hook cheap.
	TabLabels bool
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
		if kinds.Nested(kinds.Event{Entrypoint: facts.Entrypoint}) {
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

// reminder is the line an agent reads at SessionStart when its pane holds
// an active binding for this session, for a kind whose SessionStart
// output reaches its context (the registry's SessionStartContext: Claude;
// for Codex it is not proven). It costs context tokens; the pane name
// costs none.
func reminder(ctx context.Context, env Env, pane, session string) string {
	b, err := env.Bindings.ActiveByPane(ctx, bindings.CallerPane(ctx, env.Bindings, env.Herdr, pane))
	if err != nil || !kinds.Lookup(b.Kind).SessionStartContext {
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
		// nhm's own board is no agent, whatever a hook or herdr says of
		// its pane (plan-v4 section 7): no import, no binding.
		board, err := uilaunch.IsBoardPane(ctx, uilaunch.Env{InstanceDir: env.InstanceDir, Now: env.Now}, env.Herdr, pane)
		switch {
		case err != nil:
			r.warn("cannot check that the pane is not nhm's board (%v); the candidates wait", err)
			return nil
		case board:
			r.warn("the pane is nhm's board, not an agent; nothing is reconciled")
			return nil
		}
		if o.Owner != core.OwnerNhm {
			// The Bash owns the instance: the candidates wait, and the
			// pane's Bash-spawned agent is imported (acfs-9hkh.41).
			importPane(ctx, env, pane, &r)
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
	// An agent hook names its pane by HERDR_PANE_ID, which a moved agent
	// keeps at the old id; herdr knows the pane by its new one (nhm-5qa).
	at := pane
	if len(cands) > 0 {
		at = bindings.CallerPane(ctx, env.Bindings, env.Herdr, pane)
	}
	st, err := readPane(ctx, env.Herdr, at)
	if err != nil {
		return fmt.Errorf("hooks: herdr: %w", err)
	}
	// herdr's own SessionStart hook reports the session beside nhm's; give
	// it a moment when a fresh candidate names a session herdr lacks.
	st, err = settle(ctx, env, at, st, cands)
	if err != nil {
		return fmt.Errorf("hooks: herdr: %w", err)
	}
	b, err := env.Bindings.ActiveByPane(ctx, at)
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
			// candidate pending; finish those writes now. A token not yet
			// stored keeps the candidate pending, so a later reconcile
			// tries again and the token's only copy stays.
			if !repairBinding(env, c, *b, r) {
				return "", errPending
			}
			return "already bound as " + b.ID, nil
		}
		return fmt.Sprintf("refused: the pane is bound to %s (%s); use nhm adopt", b.MailName, b.ID), nil
	}
	root := canonical(projectKey)
	occ := occupant(ctx, env, st, name, c.Facts.Model)
	occ.Evidence = append([]bindings.Evidence{{Kind: "posttooluse",
		Detail: fmt.Sprintf("%s returned %s for %s in session %s", c.Facts.ToolName, name, root, c.Session)}}, occ.Evidence...)
	// A shell that names another agent goes into the binding's evidence:
	// nhm resolve shows it, and nhm-hook's own output reaches no one.
	envWarning := bindings.WithMailAgentWarning(name, &occ)
	b, err := env.Bindings.Create(ctx, bindings.New{MailName: name, MailProjectKey: root, ProjectID: core.ProjectID(root),
		Provenance: bindings.Adopted, Occupant: occ})
	if errors.Is(err, bindings.ErrIdentityBusy) || errors.Is(err, bindings.ErrPaneBusy) {
		return "refused: " + err.Error(), nil
	}
	if err != nil {
		return "", err
	}
	kept := repairBinding(env, c, b, r)
	*bp = &b
	r.act("bound %s to %s as %s", b.PaneID, b.MailName, b.ID)
	for _, w := range bindings.Label(ctx, env.Herdr, b) {
		r.warn("%s", w)
	}
	if envWarning != "" {
		r.warn("%s", envWarning)
	}
	if !kept {
		// The binding stands; the candidate stays pending until its token
		// is stored (the already-bound path above).
		return "", errPending
	}
	return "bound " + b.ID, nil
}

// repairBinding makes the writes that follow a registration's bind, when
// they are missing: the token under the binding id, and Agent Mail's
// identity file for the pane. It is idempotent, so a reconcile can run
// it for a binding that a stopped hook created. It reports whether the
// candidate's token file may go (see keepToken).
func repairBinding(env Env, c Candidate, b bindings.Binding, r *Reconcile) bool {
	kept := keepToken(env, c, b, r)
	rec, ok, err := agentmail.ReadIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID)
	if err == nil && ok && strings.EqualFold(rec.Name, b.MailName) {
		return kept
	}
	if _, err := agentmail.WriteIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID, b.MailName, env.now()); err != nil {
		r.warn("Agent Mail identity file: %v", err)
	}
	return kept
}

// keepToken stores the candidate's registration token under the binding
// id. Agent Mail issues a new token at every register_agent and refuses
// the one before it (checked 2026-10-12: SENDER_TOKEN_MISMATCH), so a
// candidate's token replaces a different stored one; candidates are
// applied oldest first, so the newest token stays. It reports whether
// the candidate's own token file may be deleted: true when it held no
// token, or the binding's file holds the same token now. On false the
// candidate's file is the only copy and must stay.
func keepToken(env Env, c Candidate, b bindings.Binding, r *Reconcile) bool {
	if c.Facts.TokenRef == "" {
		return true
	}
	tok, err := store.ReadSecret(env.InstanceDir, c.Facts.TokenRef)
	if errors.Is(err, store.ErrNoSecret) {
		return true
	}
	if err != nil {
		r.warn("registration token of candidate %d unreadable: %v", c.ID, err)
		return false
	}
	cur, err := store.ReadSecret(env.InstanceDir, b.ID)
	if err == nil && cur == tok {
		return true
	}
	if werr := store.WriteSecret(env.InstanceDir, b.ID, tok); werr != nil {
		r.warn("registration token for %s not stored yet (the candidate keeps it): %v", b.ID, werr)
		return false
	}
	if err == nil {
		r.act("replaced %s's registration token with the newer one", b.ID)
	}
	return true
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
	rule := kinds.Lookup(b.Kind).Continue
	if rule == nil {
		return "ignored: no continuation rule for kind " + b.Kind, nil
	}
	d := rule(kinds.Event{HookEvent: c.HookEvent, Source: c.Facts.Source, SessionID: c.Session,
		Entrypoint: c.Facts.Entrypoint}, bound, launch)
	verdict, reason, compacted, resumed, take := d.Verdict, d.Reason, d.Compacted, d.Resumed, d.TakeSession
	// Every rule spells its verdicts "continue", "displace", "ignore".
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

// The verdicts of every rule; TestRulesSpellVerdictsAlike keeps the kind
// packages in step with the registry.
const (
	verdictContinue = kinds.Continue
	verdictDisplace = kinds.Displace
)

// keepTrue runs after the candidates: it follows a pane that herdr
// moved, displaces a binding whose pane now holds another kind, records
// a restart in place (the same session, a new process), and applies the
// herdr name again.
func keepTrue(ctx context.Context, env Env, st paneState, b *bindings.Binding, r *Reconcile) error {
	if !st.hasAgent || (st.agent.PaneID != "" && st.agent.PaneID != b.PaneID) {
		// The agent left (a resume in place may follow), or herdr moved
		// its pane to another workspace, which gives the pane a new id.
		return followMove(ctx, env, b, r)
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
	keepName(ctx, env, st, b, r)
	keepMarks(ctx, env, b, r)
	if env.TabLabels {
		keepTabLabel(ctx, env, st, b, r)
	}
	return nil
}

// followMove moves a binding to its agent's new pane after herdr moved
// the pane to another workspace (nhm-p1o; bindings.Moved has the rules).
// The occupant and the generation stay. Agent Mail's identity file is
// written for the new pane id; the old file stays, because the moved
// process keeps its old HERDR_PANE_ID and herdr never reuses a pane id.
// The herdr name follows, and the tab gets the agent's Mail name if it
// still has herdr's default label.
func followMove(ctx context.Context, env Env, b *bindings.Binding, r *Reconcile) error {
	a, ok, err := bindings.Moved(ctx, env.Herdr, env.Bindings, *b)
	if err != nil {
		r.warn("%s left %s: %v", b.MailName, b.PaneID, err)
		return nil
	}
	if !ok {
		return nil
	}
	return relocate(ctx, env, b, a, "herdr moved", r)
}

// FollowMove moves a binding to newPane after the caller moved its
// agent there with herdr's pane.move (nhm move). It refuses when newPane
// does not hold the binding's agent (bindings.SameOccupant). Then it
// does what the reconcile does for a moved pane: relocate with the same
// generation, Agent Mail's identity file for the new id, the herdr name,
// and the tab's label if herdr's default is still on it. The caller
// holds the owner lock and checked that nhm owns the instance.
func FollowMove(ctx context.Context, env Env, b bindings.Binding, newPane string) (Reconcile, error) {
	r := Reconcile{PaneID: newPane, Ran: true, Owner: core.OwnerNhm, Binding: b.ID, Actions: []string{}, Warnings: []string{}}
	a, err := env.Herdr.AgentGet(ctx, newPane)
	if err != nil {
		return r, fmt.Errorf("hooks: %s: %w", newPane, err)
	}
	if a.PaneID == "" {
		a.PaneID = newPane
	}
	if a.Agent != b.Kind || !bindings.SameOccupant(ctx, env.Herdr, b, a) {
		return r, fmt.Errorf("hooks: %s does not hold %s's %s agent", newPane, b.MailName, b.Kind)
	}
	return r, relocate(ctx, env, &b, a, "nhm move", &r)
}

// relocate records that the binding's agent is a, in another pane, and
// applies the binding's marks there; how says who moved the pane.
func relocate(ctx context.Context, env Env, b *bindings.Binding, a herdr.AgentInfo, how string, r *Reconcile) error {
	proof := fmt.Sprintf("pid %d", b.ForegroundPID)
	if b.ForegroundPID == 0 {
		proof = "session " + b.NativeSession.Value
	}
	from := b.PaneID
	nb, err := env.Bindings.Relocate(ctx, b.ID, b.Generation, bindings.Location{WorkspaceID: a.WorkspaceID, TabID: a.TabID,
		PaneID: a.PaneID}, bindings.Evidence{Kind: "move", Detail: fmt.Sprintf("%s %s to %s, same %s", how, from, a.PaneID, proof)})
	if err != nil {
		return err
	}
	*b = nb
	r.act("%s moved from %s to %s, slot %s", b.MailName, from, b.PaneID, b.Slot)
	if _, err := agentmail.WriteIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID, b.MailName, env.now()); err != nil {
		r.warn("Agent Mail identity file: %v", err)
	}
	st := paneState{agent: a, hasAgent: true}
	keepName(ctx, env, st, b, r)
	keepMarks(ctx, env, b, r)
	keepTabLabel(ctx, env, st, b, r)
	return nil
}

// keepMarks gives the binding's pane the marks it lacks: the label and
// the sidebar tokens (bindings.KeepMarks). A spawn that stopped at a
// dialog never set them (nhm-hae).
func keepMarks(ctx context.Context, env Env, b *bindings.Binding, r *Reconcile) {
	p, err := env.Herdr.PaneGet(ctx, b.PaneID)
	if err != nil {
		r.warn("marks of %s not read: %v", b.PaneID, err)
		return
	}
	done, warnings := bindings.KeepMarks(ctx, env.Herdr, *b, p)
	if len(done) > 0 {
		r.act("marked %s for %s: %s", b.PaneID, b.MailName, strings.Join(done, ", "))
	}
	for _, w := range warnings {
		r.warn("marks of %s: %s", b.PaneID, w)
	}
}

// keepName applies the binding's herdr name when herdr shows another.
func keepName(ctx context.Context, env Env, st paneState, b *bindings.Binding, r *Reconcile) {
	if b.HerdrAlias == "" || st.agent.Name == b.HerdrAlias {
		return
	}
	if _, err := env.Herdr.AgentRename(ctx, b.PaneID, b.HerdrAlias); err != nil {
		r.warn("herdr name %s not applied to %s: %v", b.HerdrAlias, b.PaneID, err)
		return
	}
	r.act("named %s %s", b.PaneID, b.HerdrAlias)
}

// keepTabLabel names a bound agent's tab "<Name>" when the tab holds
// only the agent's pane and still has herdr's default label, or the
// project label spawn gives a tab several of the project's agents share:
// the last agent of a shared tab gets its name back (nhm-4c4). Any other
// label that spawn, the user or a tool set stays.
func keepTabLabel(ctx context.Context, env Env, st paneState, b *bindings.Binding, r *Reconcile) {
	if st.agent.TabID == "" {
		return
	}
	tab, err := env.Herdr.TabGet(ctx, st.agent.TabID)
	if err != nil {
		r.warn("tab label of %s not read: %v", st.agent.TabID, err)
		return
	}
	if tab.PaneCount != 1 || (!defaultTabLabel(tab.Label) && tab.Label != bindings.ProjectTabLabel(b.MailProjectKey)) {
		return
	}
	if _, err := env.Herdr.TabRename(ctx, tab.TabID, b.MailName); err != nil {
		r.warn("tab label %s not applied to %s: %v", b.MailName, tab.TabID, err)
		return
	}
	r.act("labelled tab %s %s", tab.TabID, b.MailName)
}

// defaultTabLabel reports whether a tab label is herdr's default: the
// number the tab had when herdr made it ("5"), which herdr keeps when
// it numbers the tabs again.
func defaultTabLabel(label string) bool {
	for _, c := range label {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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
	// The name herdr shows when it fits (a suffixed one too), else a free
	// one: a same-name agent of another project may hold the bare name
	// (nhm-4xt). Without herdr's list, the bare name, as before.
	var live []herdr.AgentInfo
	if snap, err := env.Herdr.Snapshot(ctx); err == nil {
		live = snap.Agents
	}
	if alias, err := bindings.FreeAlias(mailName, live, a.Name, a.PaneID); err == nil {
		occ.HerdrAlias = alias
	}
	occ.Evidence = []bindings.Evidence{{Kind: "herdr_agent", Detail: fmt.Sprintf("%s agent in %s", a.Agent, a.PaneID)}}
	if s := a.AgentSession; s != nil {
		occ.NativeSession = &bindings.NativeSession{Source: s.Source, Agent: s.Agent, Kind: s.Kind, Value: s.Value}
	}
	occ.ForegroundPID, occ.ForegroundStart = processFacts(ctx, env.Herdr, a.PaneID, a.Agent)
	return occ
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
