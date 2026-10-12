package enrich

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// Received is the rules one context of an agent received in one send:
// the included ones of a submitted receipt, and the ones suppressed as
// already present (an exposure all the same).
type Received struct {
	OperationID       string    `json:"operation_id"`
	TargetIndex       int       `json:"target_index"`
	BindingID         string    `json:"binding_id"`
	MailName          string    `json:"mail_name"`
	Generation        int64     `json:"generation"`
	ContextGeneration string    `json:"context_generation"`
	BeadID            string    `json:"bead_id,omitempty"`
	ProjectRoot       string    `json:"project_root,omitempty"`
	Submission        string    `json:"submission"`
	Rules             []Item    `json:"rules"`
	At                time.Time `json:"at"`
}

// RuleIDs is the rules' ids, sorted and unique.
func (r Received) RuleIDs() []string {
	var ids []string
	for _, it := range r.Rules {
		ids = append(ids, it.ID)
	}
	sort.Strings(ids)
	return slices.Compact(ids)
}

// Filter narrows List.
type Filter struct {
	OperationID string
	MailName    string
	BeadID      string
	// SubmittedOnly leaves out targets that were not submitted.
	SubmittedOnly bool
}

// List reads the CM rules that each target received, newest first.
func List(ctx context.Context, db *store.DB, f Filter) ([]Received, error) {
	q := `SELECT i.operation_id, COALESCE(i.target_index, 0), COALESCE(i.binding_id, ''), COALESCE(b.mail_name, ''),
		COALESCE(i.generation, 0), COALESCE(i.context_generation, ''), COALESCE(i.bead_id, ''),
		COALESCE(json_extract(o.scope_json, '$.scope.project_root'), ''), COALESCE(r.status, ''), i.items_json, i.created_at
		FROM injections i
		JOIN operations o ON o.operation_id = i.operation_id
		LEFT JOIN receipts r ON r.operation_id = i.operation_id AND r.target_index = i.target_index
		LEFT JOIN bindings b ON b.binding_id = i.binding_id
		WHERE i.source = ?`
	args := []any{SourceCM}
	if f.OperationID != "" {
		q += ` AND i.operation_id = ?`
		args = append(args, f.OperationID)
	}
	if f.MailName != "" {
		q += ` AND b.mail_name = ?`
		args = append(args, f.MailName)
	}
	if f.BeadID != "" {
		q += ` AND i.bead_id = ?`
		args = append(args, f.BeadID)
	}
	if f.SubmittedOnly {
		q += ` AND r.status = ?`
		args = append(args, receiptSubmitted)
	}
	q += ` ORDER BY i.created_at DESC, i.operation_id, i.target_index`
	rows, err := db.SQL().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("enrich: list: %w", err)
	}
	defer rows.Close()
	var out []Received
	for rows.Next() {
		var r Received
		var data, at string
		if err := rows.Scan(&r.OperationID, &r.TargetIndex, &r.BindingID, &r.MailName, &r.Generation, &r.ContextGeneration,
			&r.BeadID, &r.ProjectRoot, &r.Submission, &data, &at); err != nil {
			return nil, err
		}
		var items []Item
		if err := json.Unmarshal([]byte(data), &items); err != nil {
			return nil, fmt.Errorf("enrich: list: %w", err)
		}
		for _, it := range items {
			if it.Status == Included || it.Status == Suppressed {
				r.Rules = append(r.Rules, it)
			}
		}
		if r.Rules == nil {
			r.Rules = []Item{}
		}
		r.At, _ = time.Parse(store.TimeFormat, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Transcripts finds the transcript path of a binding's session, or "".
type Transcripts func(ctx context.Context, bindingID string) (string, error)

// FeedbackEnv is what feedback needs.
type FeedbackEnv struct {
	DB          *store.DB
	CM          CM
	Transcripts Transcripts
	Now         func() time.Time
}

func (e FeedbackEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// MarkResult is a mark's outcome.
type MarkResult struct {
	OperationID string `json:"operation_id"`
	RuleID      string `json:"rule_id"`
	MailName    string `json:"mail_name"`
	Outcome     string `json:"outcome"`
	Session     string `json:"session,omitempty"`
	// Repeat: the same mark was applied before; CM was not called again.
	Repeat bool `json:"repeat,omitempty"`
}

// Errors of a mark.
var (
	ErrNotReceived = errors.New("no submitted target of the operation received the rule")
	ErrAmbiguous   = errors.New("more than one target received the rule; name one")
	ErrMarkedOther = errors.New("the rule was marked the other way for this target")
	// ErrInProgress: another run holds the key and is telling CM now.
	ErrInProgress = errors.New("another run is telling CM this feedback now")
)

// Mark tells CM that a rule an operation gave was helpful or harmful, for
// the target's session. The key (operation, binding, rule) makes a repeat
// a no-op; the opposite mark for the same key is refused.
func Mark(ctx context.Context, env FeedbackEnv, opID, ruleID, mailName string, helpful bool) (MarkResult, error) {
	outcome := "harmful"
	if helpful {
		outcome = "helpful"
	}
	res := MarkResult{OperationID: opID, RuleID: ruleID, Outcome: outcome}
	list, err := List(ctx, env.DB, Filter{OperationID: opID, MailName: mailName, SubmittedOnly: true})
	if err != nil {
		return res, err
	}
	var hits []Received
	for _, r := range list {
		if slices.Contains(r.RuleIDs(), ruleID) {
			hits = append(hits, r)
		}
	}
	switch {
	case len(hits) == 0:
		return res, fmt.Errorf("%w: %s in %s", ErrNotReceived, ruleID, opID)
	case len(hits) > 1:
		var names []string
		for _, h := range hits {
			names = append(names, h.MailName)
		}
		return res, fmt.Errorf("%w (--name %s)", ErrAmbiguous, strings.Join(names, " or --name "))
	}
	r := hits[0]
	res.MailName = r.MailName
	if env.CM == nil {
		return res, errors.New("cm is not installed")
	}
	if env.Transcripts != nil {
		res.Session, _ = env.Transcripts(ctx, r.BindingID)
	}
	key := fmt.Sprintf("mark:%s:%s:%s", opID, r.BindingID, ruleID)
	if err := insertFeedback(ctx, env.DB, key, opID, r.BindingID, outcome, []string{ruleID},
		map[string]any{"session": res.Session}, env.now()); err != nil {
		return res, err
	}
	// The row is there now, maybe from another run: its outcome rules.
	st, err := feedbackRow(ctx, env.DB, key)
	if err != nil {
		return res, err
	}
	if st.outcome != outcome {
		return res, fmt.Errorf("%w (%s)", ErrMarkedOther, st.outcome)
	}
	if st.applied {
		res.Repeat = true
		return res, nil
	}
	// Only the run that claims the key calls CM.
	l, err := claim(ctx, env.DB, key, env.now())
	if err != nil {
		return res, err
	}
	if l == nil {
		return res, ErrInProgress
	}
	cctx, cancel := context.WithTimeout(ctx, cmCallTimeout)
	err = env.CM.Mark(cctx, ruleID, helpful, res.Session)
	cancel()
	if err != nil {
		l.release(ctx)
		return res, err
	}
	return res, l.applied(ctx, env.now())
}

// Bead is the part of a bead that the implicit outcome reads.
type Bead struct {
	Status   string
	Assignee string
}

// BeadReader reads a bead in a project.
type BeadReader func(ctx context.Context, projectRoot, id string) (Bead, error)

// OutcomeResult is one (bead, binding, generation) of ImplicitOutcomes.
type OutcomeResult struct {
	BeadID     string   `json:"bead_id"`
	MailName   string   `json:"mail_name"`
	Generation int64    `json:"generation"`
	Rules      []string `json:"rules"`
	Session    string   `json:"session,omitempty"`
	// State: applied, repeat (applied before), waiting (the bead is open,
	// or closed by or for someone else: no evidence), no_transcript,
	// in_progress (another run holds it), or failed.
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// ImplicitOutcomes runs the learning profile closed-assigned: for each
// bead that rules were submitted for, when the bead is closed and
// assigned to the agent that got them, it tells CM the outcome once per
// (bead, binding, generation): `cm outcome success` with the session's
// transcript, then `cm outcome-apply` for that session. An open,
// abandoned or handed-off bead gives nothing; a Mail ack and a transport
// receipt are not evidence.
func ImplicitOutcomes(ctx context.Context, env FeedbackEnv, beads BeadReader) ([]OutcomeResult, error) {
	list, err := List(ctx, env.DB, Filter{SubmittedOnly: true})
	if err != nil {
		return nil, err
	}
	type group struct {
		r     Received
		rules map[string]bool
	}
	groups := map[string]*group{}
	var order []string
	for _, r := range list {
		if r.BeadID == "" || r.BindingID == "" {
			continue
		}
		k := fmt.Sprintf("outcome:%s:%s:%d", r.BeadID, r.BindingID, r.Generation)
		g, ok := groups[k]
		if !ok {
			g = &group{r: r, rules: map[string]bool{}}
			groups[k] = g
			order = append(order, k)
		}
		for _, id := range r.RuleIDs() {
			g.rules[id] = true
		}
	}
	sort.Strings(order)
	var out []OutcomeResult
	for _, k := range order {
		g := groups[k]
		res := OutcomeResult{BeadID: g.r.BeadID, MailName: g.r.MailName, Generation: g.r.Generation}
		for id := range g.rules {
			res.Rules = append(res.Rules, id)
		}
		sort.Strings(res.Rules)
		out = append(out, outcome(ctx, env, beads, k, g.r, res))
	}
	return out, nil
}

func outcome(ctx context.Context, env FeedbackEnv, beads BeadReader, key string, r Received, res OutcomeResult) OutcomeResult {
	fail := func(err error) OutcomeResult {
		res.State, res.Detail = "failed", err.Error()
		return res
	}
	st, err := feedbackRow(ctx, env.DB, key)
	if err != nil {
		return fail(err)
	}
	if st.applied {
		res.State = "repeat"
		return res
	}
	if len(res.Rules) == 0 {
		res.State, res.Detail = "waiting", "no rules"
		return res
	}
	b, err := beads(ctx, r.ProjectRoot, r.BeadID)
	if err != nil {
		return fail(err)
	}
	if b.Status != "closed" || r.MailName == "" || b.Assignee != r.MailName {
		res.State = "waiting"
		res.Detail = fmt.Sprintf("bead %s, assignee %q", b.Status, b.Assignee)
		return res
	}
	if env.Transcripts != nil {
		if res.Session, err = env.Transcripts(ctx, r.BindingID); err != nil {
			return fail(err)
		}
	}
	if res.Session == "" {
		res.State, res.Detail = "no_transcript", "the session's transcript is not found; CM keys an outcome by it"
		return res
	}
	if env.CM == nil {
		return fail(errors.New("cm is not installed"))
	}
	text := fmt.Sprintf("implicit: bead %s closed while assigned to %s", r.BeadID, r.MailName)
	if err := insertFeedback(ctx, env.DB, key, r.OperationID, r.BindingID, "success", res.Rules,
		map[string]any{"bead_id": r.BeadID, "generation": r.Generation, "session": res.Session}, env.now()); err != nil {
		return fail(err)
	}
	// Only the run that claims the key calls CM.
	l, err := claim(ctx, env.DB, key, env.now())
	if err != nil {
		return fail(err)
	}
	if l == nil {
		if st, err = feedbackRow(ctx, env.DB, key); err == nil && st.applied {
			res.State = "repeat"
			return res
		}
		res.State, res.Detail = "in_progress", ErrInProgress.Error()
		return res
	}
	if st, err = feedbackRow(ctx, env.DB, key); err != nil {
		l.release(ctx)
		return fail(err)
	}
	// A retry after outcome-apply failed does not repeat cm outcome.
	if st.stage != stageOutcome {
		cctx, cancel := context.WithTimeout(ctx, cmCallTimeout)
		err = env.CM.Outcome(cctx, "success", res.Rules, res.Session, text)
		cancel()
		if err == nil {
			err = l.stage(ctx, stageOutcome)
		}
		if err != nil {
			l.release(ctx)
			return fail(err)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, cmCallTimeout)
	err = env.CM.OutcomeApply(cctx, res.Session)
	cancel()
	if err != nil {
		l.release(ctx)
		return fail(err)
	}
	if err := l.applied(ctx, env.now()); err != nil {
		return fail(err)
	}
	res.State = "applied"
	return res
}

// stageOutcome: `cm outcome` ran for the key; only outcome-apply is left.
const stageOutcome = "outcome"

// cmCallTimeout bounds each cm call. A run makes at most two under one
// claim, so it ends well inside claimLease: no second run claims a key
// while the first can still call CM. A variable for the tests.
var cmCallTimeout = 60 * time.Second

// claimLease frees the claim of a run that died holding it.
const claimLease = 5 * time.Minute

// releaseTimeout bounds a release, which runs on a fresh context: a
// cancelled run still frees its key.
const releaseTimeout = 5 * time.Second

// ErrLostClaim: the lease passed and another run claimed the key; this
// run's write is refused.
var ErrLostClaim = errors.New("the feedback claim was lost to another run")

type feedbackState struct {
	outcome string
	applied bool
	stage   string
}

// feedbackRow reads a key's row; a missing row is the zero state.
func feedbackRow(ctx context.Context, db *store.DB, key string) (feedbackState, error) {
	var st feedbackState
	var applied, stage sql.NullString
	err := db.SQL().QueryRowContext(ctx, `SELECT outcome, applied_at, stage FROM feedback WHERE feedback_key = ?`, key).
		Scan(&st.outcome, &applied, &stage)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("enrich: feedback: %w", err)
	}
	st.applied, st.stage = applied.Valid, stage.String
	return st, nil
}

// lease is a run's claim of a key. Its token fences every write: a run
// whose lease passed and was taken over writes nothing.
type lease struct {
	db    *store.DB
	key   string
	token string
}

// claim takes a key for this run: an unapplied key whose claim is free,
// or older than the lease. nil: another run holds it, or it is applied.
func claim(ctx context.Context, db *store.DB, key string, now time.Time) (*lease, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	l := &lease{db: db, key: key, token: hex.EncodeToString(b[:])}
	res, err := db.SQL().ExecContext(ctx, `UPDATE feedback SET claimed_at = ?, claim_token = ? WHERE feedback_key = ?
		AND applied_at IS NULL AND (claimed_at IS NULL OR CAST(claimed_at AS INTEGER) < ?)`,
		now.UnixMilli(), l.token, key, now.Add(-claimLease).UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("enrich: feedback claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return nil, err
	}
	return l, nil
}

// write runs one fenced update; no row means the claim was lost.
func (l *lease) write(ctx context.Context, set string, args ...any) error {
	res, err := l.db.SQL().ExecContext(ctx, `UPDATE feedback SET `+set+` WHERE feedback_key = ? AND claim_token = ?`,
		append(args, l.key, l.token)...)
	if err != nil {
		return fmt.Errorf("enrich: feedback: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrLostClaim
	}
	return nil
}

func (l *lease) stage(ctx context.Context, stage string) error {
	return l.write(ctx, `stage = ?`, stage)
}

// applied records that CM has the feedback and frees the key.
func (l *lease) applied(ctx context.Context, now time.Time) error {
	return l.write(ctx, `applied_at = ?, claimed_at = NULL, claim_token = NULL`, store.FormatTime(now))
}

// release frees the key after a failure, for the next run, on a fresh
// context: a cancelled or timed-out run still frees it.
func (l *lease) release(ctx context.Context) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	_ = l.write(rctx, `claimed_at = NULL, claim_token = NULL`)
}

// insertFeedback creates a key's row once; a second run finds it there.
// A row left without applied_at (CM failed) is retried by the next run.
func insertFeedback(ctx context.Context, db *store.DB, key, opID, bindingID, outcome string, rules []string,
	detail map[string]any, now time.Time) error {
	data, _ := json.Marshal(detail)
	_, err := db.SQL().ExecContext(ctx, `INSERT INTO feedback (operation_id, binding_id, outcome, detail_json, created_at,
		feedback_key, rule_ids) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (feedback_key) WHERE feedback_key IS NOT NULL DO NOTHING`,
		nullStr(opID), nullStr(bindingID), outcome, string(data), store.FormatTime(now), key, strings.Join(rules, ","))
	if err != nil {
		return fmt.Errorf("enrich: feedback: %w", err)
	}
	return nil
}
