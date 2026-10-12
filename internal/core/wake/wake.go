// Package wake tells an agent that it has new Agent Mail (plan-v8 2.8):
// "Check your Agent Mail inbox." once, when the agent is idle or done.
//
// The wake table holds the pending message ids per recipient binding. A
// row is pending until a nudge announced it (announced_at) or the
// recipient read it (last_outcome "read"). Three paths fill it and nudge
// from it, against the same table:
//
//   - mail-sent: the sender's PostToolUse hook on send_message or
//     reply_message records the recipients' ids and nudges each one that
//     is idle or done (MailSent);
//   - settle: herdr's pane.agent_status_changed, for kinds whose Stop
//     hook does not read mail, reads the inbox and nudges (Settle);
//   - maintenance: `nhm maintain` and `nhm wake` read every inbox without
//     consuming it, for mail that no hook saw, and deliver the due wakes
//     (Pass).
//
// Every path claims its attempt in SQLite before it prompts: the pending
// rows go to attempt_started in one immediate transaction, and only the
// path that moved them prompts. An attempt is a "wake" operation in the
// operations table; its request names the binding, the message ids and
// the sender-thread pairs that the rate limit counts. A nudge that herdr
// submitted, or possibly submitted, announces its ids: they never cause
// another nudge. A refused or failed attempt leaves them pending.
//
// The caller holds the owner lock and checks that nhm owns the instance;
// no SQLite transaction stays open across a herdr or Agent Mail call.
package wake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// Verb is the operation verb of a nudge.
const Verb = "wake"

// Prompt is the one line a nudge types.
const Prompt = "Check your Agent Mail inbox."

// Triggers: the path that made a nudge.
const (
	TriggerMailSent = "mail-sent"
	TriggerSettle   = "settle"
	TriggerMaintain = "maintain"
	TriggerManual   = "wake"
)

// Row outcomes in wake.last_outcome, besides the submission statuses.
const (
	// OutcomeRead: the recipient read the message; nothing announces it.
	OutcomeRead = "read"
)

// Decision actions.
const (
	ActionNudged  = "nudged"
	ActionSkipped = "skipped"
)

// ReasonNoPending is the skip reason of a recipient with nothing to
// announce; a pass leaves such recipients out of its report.
const ReasonNoPending = "no pending mail"

// Config holds the rules of 2.8.
type Config struct {
	// Cooldown is the least time between two nudges of one recipient.
	Cooldown time.Duration
	// RateLimit nudges per sender-recipient pair per thread per RateWindow.
	RateLimit  int
	RateWindow time.Duration
	// InFlight is how long an attempt may stay attempt_started; after it
	// the attempt counts as possibly submitted (the process that made it
	// died after the claim, as a hook does at its 1.9 s hard stop). A
	// prompt without a wait returns well inside it.
	InFlight time.Duration
	// InboxLimit bounds one fetch_inbox.
	InboxLimit int
	// Keep is how long a read or announced row stays, so a message is
	// never announced twice.
	Keep time.Duration
}

// DefaultConfig is plan-v8 2.8's rules.
func DefaultConfig() Config {
	return Config{Cooldown: 120 * time.Second, RateLimit: 3, RateWindow: time.Hour, InFlight: 10 * time.Second,
		InboxLimit: 200, Keep: 7 * 24 * time.Hour}
}

// Herdr is what a nudge needs from herdr; *herdr.Client has it.
type Herdr interface {
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentPrompt(ctx context.Context, p herdr.AgentPromptParams) (herdr.AgentInfo, error)
}

// Mail calls an Agent Mail tool; *agentmail.Client has it.
type Mail interface {
	Call(ctx context.Context, tool string, args, out any) error
}

// Env is what wake runs against.
type Env struct {
	DB       *store.DB
	Bindings *bindings.Store
	Herdr    Herdr
	// Mail reads inboxes; nil: no inbox is read.
	Mail   Mail
	Config Config
	Now    func() time.Time
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) config() Config {
	if e.Config.Cooldown == 0 && e.Config.RateLimit == 0 {
		return DefaultConfig()
	}
	return e.Config
}

// Message is one pending message for a recipient.
type Message struct {
	ID       int64
	ThreadID string
	Sender   string
}

// Decision is what one nudge attempt did for one recipient.
type Decision struct {
	BindingID string   `json:"binding_id"`
	MailName  string   `json:"mail_name"`
	PaneID    string   `json:"pane_id,omitempty"`
	Trigger   string   `json:"trigger"`
	Pending   []string `json:"pending"`
	Action    string   `json:"action"`
	// Outcome is the submission status of a nudge.
	Outcome     string     `json:"outcome,omitempty"`
	OperationID string     `json:"operation_id,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	WakeAfter   *time.Time `json:"wake_after,omitempty"`
}

func (d *Decision) skip(reason string) { d.Action, d.Reason = ActionSkipped, reason }

// Record adds pending rows for a recipient; a message it has already is
// left as it is. It returns how many rows are new.
func Record(ctx context.Context, env Env, b bindings.Binding, msgs []Message) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	ts := store.FormatTime(env.now())
	tx, err := env.DB.SQL().BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("wake: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	n := 0
	for _, m := range msgs {
		res, err := tx.ExecContext(ctx, `INSERT INTO wake (binding_id, message_id, thread_id, sender, first_seen_at)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT (binding_id, message_id) DO NOTHING`,
			b.ID, strconv.FormatInt(m.ID, 10), nullString(m.ThreadID), nullString(m.Sender), ts)
		if err != nil {
			return 0, fmt.Errorf("wake: record %d for %s: %w", m.ID, b.MailName, err)
		}
		if k, _ := res.RowsAffected(); k == 1 {
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("wake: %w", err)
	}
	return n, nil
}

// MarkRead marks a recipient's rows read: no nudge announces them.
func MarkRead(ctx context.Context, env Env, bindingID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	list, _ := json.Marshal(ids)
	_, err := env.DB.SQL().ExecContext(ctx, `UPDATE wake SET last_outcome = ?, announced_at = COALESCE(announced_at, ?)
		WHERE binding_id = ? AND message_id IN (SELECT value FROM json_each(?)) AND COALESCE(last_outcome, '') != ?`,
		OutcomeRead, store.FormatTime(env.now()), bindingID, string(list), OutcomeRead)
	if err != nil {
		return fmt.Errorf("wake: mark read: %w", err)
	}
	return nil
}

// pendingRow is one row that no nudge announced.
type pendingRow struct {
	id, thread, sender string
	inFlight           bool
}

func (r pendingRow) pair() string { return r.sender + "\n" + r.thread }

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func pending(ctx context.Context, db querier, bindingID string) ([]pendingRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT message_id, COALESCE(thread_id, ''), COALESCE(sender, ''),
		COALESCE(last_outcome, '') FROM wake WHERE binding_id = ? AND announced_at IS NULL ORDER BY first_seen_at, message_id`, bindingID)
	if err != nil {
		return nil, fmt.Errorf("wake: pending: %w", err)
	}
	defer rows.Close()
	var out []pendingRow
	for rows.Next() {
		var r pendingRow
		var outcome string
		if err := rows.Scan(&r.id, &r.thread, &r.sender, &outcome); err != nil {
			return nil, fmt.Errorf("wake: pending: %w", err)
		}
		r.inFlight = outcome == send.StatusAttemptStarted
		out = append(out, r)
	}
	return out, rows.Err()
}

// attempt is one wake operation of a binding.
type attempt struct {
	id      string
	status  string
	created time.Time
	pairs   []string
}

// nudgeRequest is a wake operation's request.
type nudgeRequest struct {
	BindingID  string   `json:"binding_id"`
	Generation int64    `json:"generation"`
	Trigger    string   `json:"trigger"`
	MessageIDs []string `json:"message_ids"`
	Pairs      []string `json:"pairs"`
}

// nudgeScope is a wake operation's scope.
type nudgeScope struct {
	BindingID string `json:"binding_id"`
	MailName  string `json:"mail_name"`
	PaneID    string `json:"pane_id"`
}

// recentAttempts reads a binding's newest wake operations.
func recentAttempts(ctx context.Context, db querier, bindingID string) ([]attempt, error) {
	rows, err := db.QueryContext(ctx, `SELECT operation_id, status, created_at, request_json FROM operations
		WHERE verb = ? AND json_extract(request_json, '$.binding_id') = ? ORDER BY rowid DESC LIMIT 200`, Verb, bindingID)
	if err != nil {
		return nil, fmt.Errorf("wake: attempts: %w", err)
	}
	defer rows.Close()
	var out []attempt
	for rows.Next() {
		var a attempt
		var created, request string
		if err := rows.Scan(&a.id, &a.status, &created, &request); err != nil {
			return nil, fmt.Errorf("wake: attempts: %w", err)
		}
		a.created, _ = time.Parse(store.TimeFormat, created)
		var req nudgeRequest
		_ = json.Unmarshal([]byte(request), &req)
		a.pairs = req.Pairs
		out = append(out, a)
	}
	return out, rows.Err()
}

// counts: an attempt that may have reached the agent.
func (a attempt) counts() bool {
	return a.status == send.StatusSubmitted || a.status == send.StatusPossiblySubmitted || a.status == send.StatusAttemptStarted
}

// Nudge prompts a recipient once for its pending mail, when the rules
// allow it: the agent is idle or done, the cooldown has passed, and at
// least one pending message's sender and thread are under the rate limit.
// A skipped recipient keeps its pending ids; a skip for cooldown or rate
// limit sets wake_after, when maintenance tries again.
func Nudge(ctx context.Context, env Env, b bindings.Binding, trigger string) (Decision, error) {
	cfg := env.config()
	d := Decision{BindingID: b.ID, MailName: b.MailName, PaneID: b.PaneID, Trigger: trigger, Pending: []string{}}
	db := env.DB.SQL()
	// An attempt whose process died between its claim and its outcome (a
	// hook's hard stop) must not hold the recipient until a maintenance
	// pass: past InFlight it is possibly submitted, and its ids are never
	// prompted again.
	if _, err := resolveStale(ctx, env, b.ID); err != nil {
		return d, err
	}
	rows, err := pending(ctx, db, b.ID)
	if err != nil {
		return d, err
	}
	for _, r := range rows {
		d.Pending = append(d.Pending, r.id)
	}
	switch {
	case len(rows) == 0:
		d.skip(ReasonNoPending)
		return d, nil
	case anyInFlight(rows):
		d.skip("another nudge for this mail is in flight")
		return d, nil
	case b.Status != bindings.Active || b.PaneID == "":
		d.skip(fmt.Sprintf("the binding is %s, not active in a pane", b.Status))
		return d, nil
	}
	agent, err := env.Herdr.AgentGet(ctx, b.PaneID)
	if err != nil {
		if herdr.ErrorCode(err) == "" {
			return d, fmt.Errorf("wake: herdr: %w", err)
		}
		d.skip("herdr: " + herdr.ErrorCode(err))
		return d, nil
	}
	if g, reason := send.DeliveryGate(b, &agent); g != send.GateReady {
		// Never prompt a working, blocked or unknown pane.
		d.skip(fmt.Sprintf("%s: %s", g, reason))
		return d, nil
	}
	now := env.now()
	attempts, err := recentAttempts(ctx, db, b.ID)
	if err != nil {
		return d, err
	}
	// A cheap first check; claim checks the same rules again inside its
	// transaction, where they hold.
	h := rules(rows, attempts, now, cfg)
	var opID string
	var claimed []string
	if h == nil {
		if opID, claimed, h, err = claim(ctx, env, b, trigger, now); err != nil {
			return d, err
		}
	}
	if h != nil {
		d.skip(h.reason)
		if h.after.IsZero() {
			return d, nil
		}
		d.WakeAfter = &h.after
		return d, setWakeAfter(ctx, env, b.ID, h.after)
	}
	d.OperationID, d.Pending = opID, claimed
	// Without a wait: the sender spends nothing, and the gate above saw
	// the agent idle or done.
	_, perr := env.Herdr.AgentPrompt(ctx, herdr.AgentPromptParams{Target: b.PaneID, Text: Prompt})
	status, code := send.Classify(perr)
	d.Action, d.Outcome = ActionNudged, status
	if perr != nil {
		d.Reason = code + ": " + perr.Error()
	}
	// The outcome is written even when the caller's deadline has passed:
	// a prompt that went out must not stay attempt_started.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	return d, finish(fctx, env, opID, b.ID, claimed, status, env.now())
}

func anyInFlight(rows []pendingRow) bool {
	for _, r := range rows {
		if r.inFlight {
			return true
		}
	}
	return false
}

// hold is why a recipient is not nudged now, and when to try again (zero:
// when the next path comes).
type hold struct {
	reason string
	after  time.Time
}

// rules applies the cooldown and the rate limit to a recipient's pending
// rows and its recent attempts.
func rules(rows []pendingRow, attempts []attempt, now time.Time, cfg Config) *hold {
	for _, a := range attempts {
		if a.counts() && now.Before(a.created.Add(cfg.Cooldown)) {
			return &hold{reason: "cooldown: the last nudge was at " + a.created.UTC().Format(time.RFC3339),
				after: a.created.Add(cfg.Cooldown)}
		}
	}
	if free, ok := rateLimited(rows, attempts, now, cfg); ok {
		return &hold{reason: fmt.Sprintf("rate limit: %d nudges per sender and thread per %s", cfg.RateLimit, cfg.RateWindow),
			after: free}
	}
	return nil
}

// rateLimited says whether every pending row's sender-thread pair has had
// RateLimit nudges in the window, and when the first of them frees.
func rateLimited(rows []pendingRow, attempts []attempt, now time.Time, cfg Config) (time.Time, bool) {
	if cfg.RateLimit <= 0 {
		return time.Time{}, false
	}
	var free time.Time
	for _, r := range rows {
		var times []time.Time
		for _, a := range attempts {
			if !a.counts() || !now.Before(a.created.Add(cfg.RateWindow)) {
				continue
			}
			for _, p := range a.pairs {
				if p == r.pair() {
					times = append(times, a.created)
					break
				}
			}
		}
		if len(times) < cfg.RateLimit {
			return time.Time{}, false
		}
		sort.Slice(times, func(i, j int) bool { return times[i].After(times[j]) })
		// The pair frees when the RateLimit-th newest nudge leaves the window.
		at := times[cfg.RateLimit-1].Add(cfg.RateWindow)
		if free.IsZero() || at.Before(free) {
			free = at
		}
	}
	return free, true
}

func setWakeAfter(ctx context.Context, env Env, bindingID string, after time.Time) error {
	_, err := env.DB.SQL().ExecContext(ctx, `UPDATE wake SET wake_after = ? WHERE binding_id = ? AND announced_at IS NULL`,
		store.FormatTime(after), bindingID)
	if err != nil {
		return fmt.Errorf("wake: wake_after: %w", err)
	}
	return nil
}

// claim moves the pending rows to attempt_started in one immediate
// transaction and then records the operation. Inside the transaction it
// reads the pending rows and the recent attempts again and applies the
// rules: two paths that both passed Nudge's first check cannot both
// prompt, and the cooldown and the rate limit hold under concurrency. A
// hold says why nothing was claimed.
//
// The operation row is inserted after the commit. Until then the claimed
// rows are attempt_started, which holds every other path here; after it,
// the operation counts for the cooldown.
func claim(ctx context.Context, env Env, b bindings.Binding, trigger string, now time.Time) (string, []string, *hold, error) {
	opID, err := store.NewOperationID(now)
	if err != nil {
		return "", nil, nil, err
	}
	ts := store.FormatTime(now)
	tx, err := env.DB.SQL().BeginTx(ctx, nil)
	if err != nil {
		return "", nil, nil, fmt.Errorf("wake: claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := pending(ctx, tx, b.ID)
	if err != nil {
		return "", nil, nil, err
	}
	switch {
	case anyInFlight(rows):
		return "", nil, &hold{reason: "another nudge for this mail is in flight"}, nil
	case len(rows) == 0:
		return "", nil, &hold{reason: "another path announced the pending mail"}, nil
	}
	attempts, err := recentAttempts(ctx, tx, b.ID)
	if err != nil {
		return "", nil, nil, err
	}
	if h := rules(rows, attempts, now, env.config()); h != nil {
		return "", nil, h, nil
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	list, _ := json.Marshal(ids)
	q, err := tx.QueryContext(ctx, `UPDATE wake SET last_outcome = ?, last_attempt_at = ?
		WHERE binding_id = ? AND announced_at IS NULL AND message_id IN (SELECT value FROM json_each(?))
		RETURNING message_id, COALESCE(thread_id, ''), COALESCE(sender, '')`,
		send.StatusAttemptStarted, ts, b.ID, string(list))
	if err != nil {
		return "", nil, nil, fmt.Errorf("wake: claim: %w", err)
	}
	var claimed []string
	pairSet := map[string]bool{}
	for q.Next() {
		var r pendingRow
		if err := q.Scan(&r.id, &r.thread, &r.sender); err != nil {
			_ = q.Close()
			return "", nil, nil, fmt.Errorf("wake: claim: %w", err)
		}
		claimed = append(claimed, r.id)
		pairSet[r.pair()] = true
	}
	if err := q.Close(); err != nil {
		return "", nil, nil, fmt.Errorf("wake: claim: %w", err)
	}
	if len(claimed) == 0 {
		return "", nil, &hold{reason: "another path announced the pending mail"}, nil
	}
	if err := tx.Commit(); err != nil {
		return "", nil, nil, fmt.Errorf("wake: claim: %w", err)
	}
	sort.Strings(claimed)
	pairs := make([]string, 0, len(pairSet))
	for p := range pairSet {
		pairs = append(pairs, p)
	}
	sort.Strings(pairs)
	// A crash between the commit above and this insert leaves rows
	// attempt_started with no operation; resolveStale settles them.
	_, err = env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: opID, Verb: Verb,
		Scope:   nudgeScope{BindingID: b.ID, MailName: b.MailName, PaneID: b.PaneID},
		Request: nudgeRequest{BindingID: b.ID, Generation: b.Generation, Trigger: trigger, MessageIDs: claimed, Pairs: pairs}}, now)
	if err != nil {
		_ = release(ctx, env, b.ID, claimed)
		return "", nil, nil, err
	}
	if err := env.DB.SetOperationStatus(ctx, opID, send.StatusAttemptStarted, false, now); err != nil {
		// No prompt went out: the operation closes as failed.
		_ = release(ctx, env, b.ID, claimed)
		_ = env.DB.SetOperationStatus(ctx, opID, send.StatusFailed, true, now)
		return "", nil, nil, err
	}
	return opID, claimed, nil, nil
}

// release gives claimed rows back when no prompt went out.
func release(ctx context.Context, env Env, bindingID string, ids []string) error {
	list, _ := json.Marshal(ids)
	_, err := env.DB.SQL().ExecContext(ctx, `UPDATE wake SET last_outcome = NULL WHERE binding_id = ?
		AND message_id IN (SELECT value FROM json_each(?)) AND last_outcome = ?`, bindingID, string(list), send.StatusAttemptStarted)
	return err
}

// finish records a nudge's outcome. Submitted and possibly submitted
// announce the ids; refused and failed leave them pending, and a later
// pass may try again.
//
// Both writes compare and set: the operation moves only from
// attempt_started, or from the possibly_submitted that resolveStale set
// when this prompt outlived InFlight (only resolveStale sets it before
// finish, and finish runs once per operation). Its rows change only when
// the operation did. So a prompt herdr refused after a stale settle makes
// its mail pending again (nothing was typed), and a late outcome never
// overwrites one that another path wrote.
func finish(ctx context.Context, env Env, opID, bindingID string, ids []string, status string, now time.Time) error {
	ts := store.FormatTime(now)
	tx, err := env.DB.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("wake: finish %s: %w", opID, err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE operations SET status = ?, updated_at = ?, completed_at = ?
		WHERE operation_id = ? AND verb = ? AND status IN (?, ?)`,
		status, ts, ts, opID, Verb, send.StatusAttemptStarted, send.StatusPossiblySubmitted)
	if err != nil {
		return fmt.Errorf("wake: finish %s: %w", opID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		// Another path settled the operation; its outcome stands.
		return err
	}
	list, _ := json.Marshal(ids)
	var announced any
	if status == send.StatusSubmitted || status == send.StatusPossiblySubmitted {
		announced = ts
	}
	_, err = tx.ExecContext(ctx, `UPDATE wake SET last_outcome = ?, announced_at = ?,
		attempts = attempts + 1, last_attempt_at = ?
		WHERE binding_id = ? AND message_id IN (SELECT value FROM json_each(?)) AND last_outcome IN (?, ?)`,
		status, announced, ts, bindingID, string(list), send.StatusAttemptStarted, send.StatusPossiblySubmitted)
	if err != nil {
		return fmt.Errorf("wake: finish %s: %w", opID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("wake: finish %s: %w", opID, err)
	}
	return nil
}

// ResolveStale settles attempts that stayed attempt_started longer than
// InFlight: the process died after its claim, so the prompt possibly went
// out. They are announced and never repeated. It returns how many rows it
// settled.
func ResolveStale(ctx context.Context, env Env) (int, error) { return resolveStale(ctx, env, "") }

// resolveStale is ResolveStale for one binding ("" means every binding).
// Nudge runs it for its recipient, so no maintenance runner is needed.
func resolveStale(ctx context.Context, env Env, bindingID string) (int, error) {
	cfg := env.config()
	now := env.now()
	db := env.DB.SQL()
	rows, err := db.QueryContext(ctx, `SELECT binding_id, message_id, COALESCE(last_attempt_at, first_seen_at) FROM wake
		WHERE announced_at IS NULL AND last_outcome = ? AND (? = '' OR binding_id = ?)`,
		send.StatusAttemptStarted, bindingID, bindingID)
	if err != nil {
		return 0, fmt.Errorf("wake: stale: %w", err)
	}
	type key struct{ binding, message string }
	var stale []key
	for rows.Next() {
		var k key
		var at string
		if err := rows.Scan(&k.binding, &k.message, &at); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("wake: stale: %w", err)
		}
		if t, err := time.Parse(store.TimeFormat, at); err != nil || now.Sub(t) >= cfg.InFlight {
			stale = append(stale, k)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("wake: stale: %w", err)
	}
	ts := store.FormatTime(now)
	for _, k := range stale {
		if _, err := db.ExecContext(ctx, `UPDATE wake SET last_outcome = ?, announced_at = ?, attempts = attempts + 1
			WHERE binding_id = ? AND message_id = ? AND last_outcome = ?`,
			send.StatusPossiblySubmitted, ts, k.binding, k.message, send.StatusAttemptStarted); err != nil {
			return 0, fmt.Errorf("wake: stale: %w", err)
		}
	}
	ops, err := db.QueryContext(ctx, `SELECT operation_id, created_at FROM operations WHERE verb = ? AND status = ?
		AND (? = '' OR json_extract(request_json, '$.binding_id') = ?)`,
		Verb, send.StatusAttemptStarted, bindingID, bindingID)
	if err != nil {
		return 0, fmt.Errorf("wake: stale: %w", err)
	}
	var staleOps []string
	for ops.Next() {
		var id, created string
		if err := ops.Scan(&id, &created); err != nil {
			_ = ops.Close()
			return 0, fmt.Errorf("wake: stale: %w", err)
		}
		if t, err := time.Parse(store.TimeFormat, created); err != nil || now.Sub(t) >= cfg.InFlight {
			staleOps = append(staleOps, id)
		}
	}
	if err := ops.Close(); err != nil {
		return 0, fmt.Errorf("wake: stale: %w", err)
	}
	// Compare and set: an attempt whose own outcome came in meanwhile
	// (finish) keeps it; a failed attempt must not start a cooldown.
	for _, id := range staleOps {
		if _, err := db.ExecContext(ctx, `UPDATE operations SET status = ?, updated_at = ?, completed_at = ?
			WHERE operation_id = ? AND verb = ? AND status = ?`,
			send.StatusPossiblySubmitted, ts, ts, id, Verb, send.StatusAttemptStarted); err != nil {
			return 0, fmt.Errorf("wake: stale: %w", err)
		}
	}
	return len(stale), nil
}

// Uncertain is a recent nudge whose submission is not known.
type Uncertain struct {
	OperationID string    `json:"operation_id"`
	BindingID   string    `json:"binding_id"`
	MailName    string    `json:"mail_name"`
	At          time.Time `json:"at"`
}

// PossiblySubmitted lists the nudges of the last window that herdr did
// not confirm. They are never repeated, so they stay visible here.
func PossiblySubmitted(ctx context.Context, env Env) ([]Uncertain, error) {
	cfg := env.config()
	now := env.now()
	rows, err := env.DB.SQL().QueryContext(ctx, `SELECT operation_id, created_at,
		COALESCE(json_extract(scope_json, '$.binding_id'), ''), COALESCE(json_extract(scope_json, '$.mail_name'), '')
		FROM operations WHERE verb = ? AND status = ? ORDER BY rowid DESC LIMIT 100`, Verb, send.StatusPossiblySubmitted)
	if err != nil {
		return nil, fmt.Errorf("wake: uncertain: %w", err)
	}
	defer rows.Close()
	out := []Uncertain{}
	for rows.Next() {
		var u Uncertain
		var created string
		if err := rows.Scan(&u.OperationID, &created, &u.BindingID, &u.MailName); err != nil {
			return nil, fmt.Errorf("wake: uncertain: %w", err)
		}
		u.At, _ = time.Parse(store.TimeFormat, created)
		if now.Sub(u.At) < cfg.RateWindow {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// Prune deletes wake operations older than Keep, and the rows that can no
// longer cause a nudge, once Keep has passed: the rows of read mail, and
// those of a binding that is not active. A row of mail that is still
// unread stays for as long as it is unread, whatever its age (a takeover
// announces old mail too): if it went, the next inbox read would find the
// mail again and nudge for it a second time.
func Prune(ctx context.Context, env Env) (int, error) {
	cfg := env.config()
	cut := env.now().Add(-cfg.Keep)
	db := env.DB.SQL()
	rows, err := db.QueryContext(ctx, `SELECT w.binding_id, w.message_id, w.announced_at FROM wake w
		LEFT JOIN bindings b ON b.binding_id = w.binding_id
		WHERE w.announced_at IS NOT NULL
		AND (COALESCE(w.last_outcome, '') = ? OR b.status IS NULL OR b.status <> 'active')`, OutcomeRead)
	if err != nil {
		return 0, fmt.Errorf("wake: prune: %w", err)
	}
	type key struct{ binding, message string }
	var old []key
	for rows.Next() {
		var k key
		var at string
		if err := rows.Scan(&k.binding, &k.message, &at); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("wake: prune: %w", err)
		}
		if t, err := time.Parse(store.TimeFormat, at); err == nil && t.Before(cut) {
			old = append(old, k)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("wake: prune: %w", err)
	}
	for _, k := range old {
		if _, err := db.ExecContext(ctx, `DELETE FROM wake WHERE binding_id = ? AND message_id = ?`, k.binding, k.message); err != nil {
			return 0, fmt.Errorf("wake: prune: %w", err)
		}
	}
	ops, err := db.QueryContext(ctx, `SELECT operation_id, created_at FROM operations WHERE verb = ? AND completed_at IS NOT NULL`, Verb)
	if err != nil {
		return 0, fmt.Errorf("wake: prune: %w", err)
	}
	var oldOps []string
	for ops.Next() {
		var id, created string
		if err := ops.Scan(&id, &created); err != nil {
			_ = ops.Close()
			return 0, fmt.Errorf("wake: prune: %w", err)
		}
		if t, err := time.Parse(store.TimeFormat, created); err == nil && t.Before(cut) {
			oldOps = append(oldOps, id)
		}
	}
	if err := ops.Close(); err != nil {
		return 0, fmt.Errorf("wake: prune: %w", err)
	}
	for _, id := range oldOps {
		if _, err := db.ExecContext(ctx, `DELETE FROM operations WHERE operation_id = ? AND verb = ?`, id, Verb); err != nil {
			return 0, fmt.Errorf("wake: prune: %w", err)
		}
	}
	return len(old), nil
}

// The meta keys of the maintenance runner, for doctor and the board.
const (
	metaLastPass = "maintain_last_pass"
	metaRunner   = "maintain_runner"
)

// Runners of the maintenance pass.
const (
	RunnerOnce  = "once"  // nhm maintain --once, from the systemd timer or by hand
	RunnerLoop  = "loop"  // nhm maintain --loop
	RunnerBoard = "board" // the board's refresh
)

// RecordPass notes a finished maintenance pass and its runner.
func RecordPass(ctx context.Context, db *store.DB, runner string, at time.Time) error {
	for _, kv := range [][2]string{{metaLastPass, store.FormatTime(at)}, {metaRunner, runner}} {
		if _, err := db.SQL().ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("wake: record pass: %w", err)
		}
	}
	return nil
}

// LastPass returns the last maintenance pass and its runner; ok is false
// when no pass ran.
func LastPass(ctx context.Context, db *store.DB) (runner string, at time.Time, ok bool, err error) {
	v, err := db.Meta(ctx, metaLastPass)
	if err != nil || v == "" {
		return "", time.Time{}, false, err
	}
	if at, err = time.Parse(store.TimeFormat, v); err != nil {
		return "", time.Time{}, false, fmt.Errorf("wake: %s: %w", metaLastPass, err)
	}
	runner, err = db.Meta(ctx, metaRunner)
	return runner, at, err == nil, err
}

// ErrNoBinding means a recipient has no active binding in nhm.
var ErrNoBinding = errors.New("wake: no active binding")

// Resolve finds the active binding of a name: an Agent Mail name (in
// projectKey when it is given), a herdr name, or a pane id.
func Resolve(ctx context.Context, bs *bindings.Store, projectKey, name string) (bindings.Binding, error) {
	list, err := bs.List(ctx, bindings.Filter{ProjectKey: projectKey, MailName: name, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return bindings.Binding{}, err
	}
	switch len(list) {
	case 1:
		return list[0], nil
	case 0:
	default:
		return bindings.Binding{}, fmt.Errorf("%w: %s is active in %d projects; give --project", bindings.ErrAmbiguous, name, len(list))
	}
	if b, err := bs.ActiveByAlias(ctx, strings.ToLower(name)); err == nil {
		return b, nil
	}
	if b, err := bs.ActiveByPane(ctx, name); err == nil {
		return b, nil
	}
	return bindings.Binding{}, fmt.Errorf("%w for %s", ErrNoBinding, name)
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
