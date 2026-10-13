package send

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

// Verb is the operation verb of a send.
const Verb = "send"

// Operation statuses of a send.
const (
	OpDispatching = "dispatching"
	OpCompleted   = "completed"
)

// Parallel is how many targets are sent to at once.
const Parallel = 4

// promptGateMS is herdr's own wait after a submission: it returns when it
// sees working or blocked, or stalls after five seconds.
const promptGateMS int64 = 5000

// lockWait bounds the wait for a target's lock.
const lockWait = 10 * time.Second

// Herdr is what send needs from herdr; *herdr.Client has it.
type Herdr interface {
	AgentList(ctx context.Context) ([]herdr.AgentInfo, error)
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentPrompt(ctx context.Context, p herdr.AgentPromptParams) (herdr.AgentInfo, error)
}

// Bead is the bead a template's bead_* placeholders come from.
type Bead struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	IssueType   string `json:"issue_type"`
}

// Env is what a send runs against.
type Env struct {
	DB          *store.DB
	Bindings    *bindings.Store
	Herdr       Herdr
	InstanceDir string
	// Inode returns herdr's socket inode for the baselines; nil: 0.
	Inode func() uint64
	Now   func() time.Time
	// Bead reads a bead in a project (br show --json).
	Bead func(ctx context.Context, projectRoot, id string) (Bead, error)
	// History appends a line to history.jsonl; nil skips it.
	History func(*history.HistoryEntry) error
	// CM and CASS serve the enrichment; nil is "not installed".
	CM      enrich.CM
	CASS    enrich.CASS
	Budgets enrich.Budgets
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Request is one send.
type Request struct {
	// Session is the session as the caller named it; WorkspaceID is the
	// resolved workspace.
	Session     string
	WorkspaceID string
	ProjectRoot string
	ProjectKey  string
	// Caller is the calling pane, left out unless selected by name.
	Caller   string
	Selector Selector
	// Message is the prompt text (an argument, --msg or --msg-file).
	Message string
	// Template is a key or a .md path; Vars are --var values.
	Template string
	Vars     map[string]string
	// FileContent is --file's content: {{file}} with a template, the
	// prompt without one.
	FileContent string
	BeadID      string
	WhenAny     bool
	Force       bool
	OperationID string
	// Expect freezes the targets a caller reviewed: binding id to the
	// generation it saw. When it is set, a selected target whose binding
	// is at another generation, or that is not in it, is refused under
	// the target's lock before any prompt, and an expected binding that
	// the selection no longer finds gets a refused receipt. Nil keeps
	// selection at submission (the CLI).
	Expect map[string]int64
	DryRun bool
	Source history.Source
	// Scope is recorded with the operation.
	Scope any
	// Enrich is the CM and CASS enrichment and the duplicate check.
	// Internal prompts (wake, naming) leave it empty.
	Enrich Enrichment
	// Prepared is an enrichment prepared before Message was composed, its
	// block already in Message (an assignment's work prompt, recorded
	// before its delivery; nhm-i85). The send retrieves nothing and
	// records it as its one target's exposure, under this operation and
	// the target's context generation now. It needs one target, the
	// binding it was prepared for at the same generation, and no
	// enrichment of the send's own.
	Prepared *enrich.Exposure
}

// Enrichment is a send's CM and CASS part (plan-v8 2.13).
type Enrichment struct {
	// CM and CASS are the explicit flags; empty is not given. Explicit
	// flags win over the template's enrichment, which wins over the
	// defaults (the user's config).
	CM, CASS               enrich.Mode
	DefaultCM, DefaultCASS enrich.Mode
	// Query is the query for both sources; empty: the prompt's start.
	Query string
	// Check runs the CASS duplicate check over the last CheckDays; it
	// only warns. --force skips it.
	Check     bool
	CheckDays int
}

// resolve applies the precedence: flag, template, default.
func (e Enrichment) resolve(tmpl *templates.Template) (cmMode, cassMode enrich.Mode, err error) {
	pick := func(flag, def enrich.Mode, source string) (enrich.Mode, error) {
		if flag != "" {
			return flag, nil
		}
		if tmpl != nil && tmpl.Enrichment[source] != "" {
			m, err := enrich.ParseMode(tmpl.Enrichment[source])
			if err != nil {
				return enrich.Off, fmt.Errorf("template %s: enrichment %s: %w", tmpl.Name, source, err)
			}
			return m, nil
		}
		return enrich.ParseMode(string(def))
	}
	if cmMode, err = pick(e.CM, e.DefaultCM, enrich.SourceCM); err != nil {
		return
	}
	cassMode, err = pick(e.CASS, e.DefaultCASS, enrich.SourceCASS)
	return
}

// Injection reports what one source retrieved for the send.
type Injection = enrich.Report

// Result is a send's outcome: from this run, or the stored one of an
// operation id claimed before.
type Result struct {
	OperationID string `json:"operation_id,omitempty"`
	// Replayed: the operation id was claimed before; nothing was sent.
	Replayed bool `json:"replayed,omitempty"`
	// InProgress: the stored operation has no final outcome yet.
	InProgress    bool        `json:"in_progress,omitempty"`
	DryRun        bool        `json:"dry_run,omitempty"`
	Session       string      `json:"session"`
	WorkspaceID   string      `json:"workspace_id"`
	Targets       []Receipt   `json:"targets"`
	Candidates    []Candidate `json:"candidates,omitempty"`
	Preview       string      `json:"message_preview"`
	PayloadSHA256 string      `json:"payload_sha256"`
	PayloadBytes  int64       `json:"payload_bytes"`
	// Rendered holds each target's prompt, for a dry run only.
	Rendered        []string  `json:"rendered,omitempty"`
	MemoryInjection Injection `json:"memory_injection"`
	CASSInjection   Injection `json:"cass_injection"`
	// Injections is each target's items: included, omitted (with the
	// reason) or suppressed as already present in its context.
	Injections []enrich.TargetInjection `json:"injections,omitempty"`
	// Duplicates is what the CASS duplicate check found.
	Duplicates  []enrich.Similar `json:"cass_check,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
	Warnings    []string         `json:"warnings"`
}

// notInjected is the report of a source that is off.
var notInjected = Injection{Enabled: false, Mode: enrich.Off, SkippedReason: "off"}

// Counts sums up the receipts.
func (r Result) Counts() map[string]int {
	c := map[string]int{}
	for _, t := range r.Targets {
		c[t.Status]++
	}
	return c
}

// opRecord is what the operation row's request holds: the caller's input,
// never the rendered payload (a date in a template would turn a retry
// into a conflict). The selector, not the resolved targets, binds the id.
type opRecord struct {
	WorkspaceID   string            `json:"workspace_id"`
	ProjectKey    string            `json:"project_key,omitempty"`
	Selector      Selector          `json:"selector"`
	MessageSHA256 string            `json:"message_sha256"`
	Template      string            `json:"template,omitempty"`
	Vars          map[string]string `json:"vars,omitempty"`
	FileSHA256    string            `json:"file_sha256,omitempty"`
	BeadID        string            `json:"bead_id,omitempty"`
	WhenAny       bool              `json:"when_any,omitempty"`
	Force         bool              `json:"force,omitempty"`
	CM            enrich.Mode       `json:"cm,omitempty"`
	CASS          enrich.Mode       `json:"cass,omitempty"`
	MemoryQuery   string            `json:"memory_query,omitempty"`
	Expect        map[string]int64  `json:"expect,omitempty"`
}

// opScope is the operation row's scope: the caller's scope, the session
// and the shared payload's digest, which the robot receipt reports.
type opScope struct {
	Scope         any    `json:"scope,omitempty"`
	Session       string `json:"session"`
	WorkspaceID   string `json:"workspace_id"`
	PayloadSHA256 string `json:"payload_sha256"`
	PayloadBytes  int64  `json:"payload_bytes"`
	Preview       string `json:"message_preview"`
	// The retrieval reports, without the items' text.
	MemoryInjection *Injection `json:"memory_injection,omitempty"`
	CASSInjection   *Injection `json:"cass_injection,omitempty"`
}

// Receipt error codes of an expected-generation refusal (Request.Expect).
const (
	CodeGenerationChanged = "generation_changed"
	CodeNotReviewed       = "not_reviewed"
	CodeNotSelected       = "not_selected"
)

// expectRefusal says why a target at generation gen is refused under
// expect; "" when expect is nil or matches.
func expectRefusal(expect map[string]int64, id string, gen int64) (code, reason string) {
	if expect == nil {
		return "", ""
	}
	want, ok := expect[id]
	switch {
	case !ok:
		return CodeNotReviewed, "the agent was not in the reviewed set"
	case want != gen:
		return CodeGenerationChanged, fmt.Sprintf("expected generation %d, the binding is at generation %d", want, gen)
	}
	return "", ""
}

// unselected lists, sorted, the expected bindings no target holds.
func unselected(expect map[string]int64, targets []Target) []string {
	var out []string
	for id := range expect {
		if !slices.ContainsFunc(targets, func(t Target) bool { return t.Binding.ID == id }) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Errors.
var (
	ErrNoPayload  = errors.New("nothing to send: give a prompt, --msg, --msg-file, --file or a template")
	ErrBothInputs = errors.New("give a prompt or a template, not both")
	ErrBeadNeedsT = errors.New("--bead fills a template's bead_* placeholders; give -t")
)

// Digest is the SHA-256 (hex) and the byte count of a payload.
func Digest(payload string) (string, int64) {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:]), int64(len(payload))
}

// Preview is ntm's message preview: the first 100 characters.
func Preview(s string) string {
	r := []rune(s)
	if len(r) > 100 {
		return string(r[:100]) + "..."
	}
	return s
}

// Send runs one send. A refusal before any prompt (bad input, no target,
// a template placeholder nothing fills) is an error and claims nothing. A
// dry run writes nothing. Otherwise the operation is claimed first; a
// second claim of the same id returns the stored outcome and sends
// nothing.
func Send(ctx context.Context, env Env, req Request) (Result, error) {
	res := Result{Session: req.Session, WorkspaceID: req.WorkspaceID, Warnings: []string{},
		MemoryInjection: notInjected, CASSInjection: notInjected}
	record := opRecord{WorkspaceID: req.WorkspaceID, ProjectKey: req.ProjectKey, Selector: req.Selector,
		Template: req.Template, Vars: req.Vars, BeadID: req.BeadID, WhenAny: req.WhenAny, Force: req.Force,
		CM: req.Enrich.CM, CASS: req.Enrich.CASS, MemoryQuery: req.Enrich.Query, Expect: req.Expect}
	record.MessageSHA256, _ = Digest(req.Message)
	if req.FileContent != "" {
		record.FileSHA256, _ = Digest(req.FileContent)
	}
	// An identical retry replays the stored outcome before anything else:
	// agents that left since, or a template that changed, never turn a
	// retry into an error or a second send.
	if req.OperationID != "" && !req.DryRun {
		if stored, ok, err := replay(ctx, env.DB, req.OperationID, record); ok || err != nil {
			return stored, err
		}
	}
	tmpl, err := buildTemplate(req)
	if err != nil {
		return res, err
	}
	var bead *Bead
	if req.BeadID != "" {
		if env.Bead == nil {
			return res, errors.New("--bead: no bead reader")
		}
		b, err := env.Bead(ctx, req.ProjectRoot, req.BeadID)
		if err != nil {
			return res, fmt.Errorf("--bead %s: %w", req.BeadID, err)
		}
		bead = &b
	}
	agents, err := env.Herdr.AgentList(ctx)
	if err != nil {
		return res, err
	}
	targets, candidates, err := Select(ctx, env.Bindings, agents, req.WorkspaceID, req.ProjectKey, req.Caller, req.Selector)
	if err != nil {
		return res, err
	}
	res.Candidates = candidates
	shared, payloads, err := render(tmpl, req, bead, targets)
	if err != nil {
		return res, err
	}
	ex, err := enrichTargets(ctx, env, req, tmpl, shared, targets, payloads, &res)
	if err != nil {
		return res, err
	}
	res.PayloadSHA256, res.PayloadBytes = Digest(shared)
	if len(payloads) > 0 && allEqual(payloads) {
		res.PayloadSHA256, res.PayloadBytes = Digest(payloads[0])
	}
	res.Preview = Preview(shared)

	if req.DryRun {
		res.DryRun = true
		for i, t := range targets {
			g, reason := DeliveryGate(t.Binding, t.Agent)
			status := "would_send"
			if t.Agent == nil || !Admit(g, req.WhenAny, req.Force) {
				status = StatusRefused
			}
			if code, why := expectRefusal(req.Expect, t.Binding.ID, t.Binding.Generation); code != "" {
				status, reason = StatusRefused, why
			}
			res.Targets = append(res.Targets, Receipt{TargetIndex: i, Key: t.Key, BindingID: t.Binding.ID,
				MailName: t.Binding.MailName, PaneID: t.Binding.PaneID, Kind: t.Binding.Kind, Generation: t.Binding.Generation,
				Status: status, Result: ReceiptResult{Gate: g, Reason: reason, TaskResult: "unknown"}})
		}
		res.Rendered = payloads
		return res, nil
	}

	now := env.now()
	claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: Verb,
		Scope: opScope{Scope: req.Scope, Session: req.Session, WorkspaceID: req.WorkspaceID,
			PayloadSHA256: res.PayloadSHA256, PayloadBytes: res.PayloadBytes, Preview: res.Preview,
			MemoryInjection: reportOf(res.MemoryInjection), CASSInjection: reportOf(res.CASSInjection)},
		Request: record}, now)
	if err != nil {
		return res, err
	}
	if claim.Existing {
		return Stored(ctx, env.DB, claim.Operation.ID)
	}
	res.OperationID = claim.Operation.ID
	res.CreatedAt = claim.Operation.CreatedAt

	rdb := receiptDB{env.DB}
	receipts := make([]Receipt, 0, len(targets)+len(candidates))
	for i, t := range targets {
		digest, n := Digest(payloads[i])
		rc := Receipt{OperationID: res.OperationID, TargetIndex: i, Key: t.Key, BindingID: t.Binding.ID,
			MailName: t.Binding.MailName, PaneID: t.Binding.PaneID, Kind: t.Binding.Kind, Generation: t.Binding.Generation,
			Status: StatusPending, PayloadSHA256: digest, PayloadBytes: n, UpdatedAt: now}
		rc.Result = ReceiptResult{Key: rc.Key, MailName: rc.MailName, PaneID: rc.PaneID, Kind: rc.Kind, TaskResult: "unknown"}
		receipts = append(receipts, rc)
	}
	for _, c := range candidates {
		rc := Receipt{OperationID: res.OperationID, TargetIndex: len(receipts), Key: c.Name, PaneID: c.PaneID,
			Status: StatusRefused, ErrorCode: "unbound", UpdatedAt: now}
		rc.Result = ReceiptResult{Key: c.Name, PaneID: c.PaneID, Reason: "no binding; " + c.Hint, TaskResult: "unknown"}
		receipts = append(receipts, rc)
	}
	for _, id := range unselected(req.Expect, targets) {
		rc := Receipt{OperationID: res.OperationID, TargetIndex: len(receipts), Key: id, BindingID: id,
			Generation: req.Expect[id], Status: StatusRefused, ErrorCode: CodeNotSelected, UpdatedAt: now}
		rc.Result = ReceiptResult{Key: id, Reason: "the selection no longer finds this agent (left, moved or not active)", TaskResult: "unknown"}
		receipts = append(receipts, rc)
	}
	for _, rc := range receipts {
		if err := rdb.insert(ctx, rc); err != nil {
			return res, err
		}
	}
	// An exposure for every target, before any prompt: the receipt's
	// status later says whether the target got it.
	for i := range ex {
		ex[i].OperationID = res.OperationID
		if err := enrich.Record(ctx, env.DB, ex[i], now); err != nil {
			return res, err
		}
	}
	if err := env.DB.SetOperationStatus(ctx, res.OperationID, OpDispatching, false, env.now()); err != nil {
		return res, err
	}

	sem := make(chan struct{}, Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, t := range targets {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := dispatch(ctx, env, rdb, t, &receipts[i], payloads[i], req.WhenAny || req.Force, req.Expect); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		// A receipt write failed; the operation stays "dispatching" and
		// its receipts say what is known.
		return res, firstErr
	}
	if err := env.DB.SetOperationStatus(ctx, res.OperationID, OpCompleted, true, env.now()); err != nil {
		return res, err
	}
	if env.History != nil {
		appendHistory(env, req, res.OperationID, shared, receipts)
	}
	out, err := Stored(ctx, env.DB, res.OperationID)
	out.Replayed, out.Candidates = false, candidates
	// This run's warnings and duplicate check; a replay has neither.
	out.Warnings, out.Duplicates = res.Warnings, res.Duplicates
	return out, err
}

// replay returns the stored outcome of an operation id claimed before.
// ok is false when the id is new. The same id with another verb or
// another input is store.ErrOperationIDConflict, as a claim would say.
func replay(ctx context.Context, db *store.DB, id string, record opRecord) (Result, bool, error) {
	op, err := db.Operation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return Result{}, false, err
	}
	if op.Verb != Verb || op.Fingerprint != store.Fingerprint(Verb, data) {
		return Result{}, false, fmt.Errorf("%w: %s was claimed by %s with a different request", store.ErrOperationIDConflict, id, op.Verb)
	}
	res, err := Stored(ctx, db, id)
	return res, true, err
}

// dispatch sends to one target under its lock: revalidate, gate,
// baseline, one agent.prompt, result. Only a receipt write failure is an
// error; everything else is the receipt's status.
func dispatch(ctx context.Context, env Env, rdb receiptDB, t Target, rc *Receipt, payload string, anyState bool, expect map[string]int64) error {
	finish := func(status, code, reason string) error {
		rc.Status, rc.ErrorCode, rc.UpdatedAt = status, code, env.now()
		if reason != "" {
			rc.Result.Reason = reason
		}
		return rdb.update(ctx, *rc)
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	unlock, err := lockTarget(lctx, env.InstanceDir, t.Binding.ID)
	cancel()
	if err != nil {
		return finish(StatusRefused, "target_busy", err.Error())
	}
	defer unlock()

	cur, err := env.Bindings.Get(ctx, t.Binding.ID)
	if err != nil {
		return finish(StatusFailed, "binding", err.Error())
	}
	if cur.Generation != t.Binding.Generation || cur.Status != bindings.Active || cur.PaneID != t.Binding.PaneID {
		return finish(StatusRefused, "generation_changed",
			fmt.Sprintf("the binding changed since selection (generation %d, %s, pane %s)", cur.Generation, cur.Status, cur.PaneID))
	}
	if code, why := expectRefusal(expect, cur.ID, cur.Generation); code != "" {
		return finish(StatusRefused, code, why)
	}
	a, err := env.Herdr.AgentGet(ctx, cur.PaneID)
	var agent *herdr.AgentInfo
	if err == nil {
		agent = &a
	} else if herdr.ErrorCode(err) == "" {
		return finish(StatusFailed, "herdr", err.Error())
	}
	// A moved pane keeps its old id as an alias, so agent.get by the
	// binding's pane can answer from another workspace. The binding's
	// workspace is the session's: never prompt outside it.
	if agent != nil && (agent.PaneID != cur.PaneID || (agent.WorkspaceID != "" && agent.WorkspaceID != cur.WorkspaceID)) {
		rc.Result.Gate = GateUnsafe
		return finish(StatusRefused, "gate_"+string(GateUnsafe), fmt.Sprintf(
			"the agent moved: herdr answers from pane %s in workspace %s; the binding says %s in %s",
			agent.PaneID, agent.WorkspaceID, cur.PaneID, cur.WorkspaceID))
	}
	g, reason := DeliveryGate(cur, agent)
	rc.Result.Gate = g
	if agent == nil {
		// No agent in the pane: --when any never prompts a shell.
		return finish(StatusRefused, "gate_"+string(g), reason)
	}
	if !Admit(g, anyState, false) {
		return finish(StatusRefused, "gate_"+string(g), reason)
	}
	var inode uint64
	if env.Inode != nil {
		inode = env.Inode()
	}
	if base, err := wait.Capture(cur, *agent, inode, env.now()); err == nil {
		rc.Baseline = &base
	}
	// The baseline and attempt_started are committed before the call: a
	// crash after this point leaves an outcome that is unknown, never
	// "not sent".
	if err := finish(StatusAttemptStarted, "", ""); err != nil {
		return err
	}
	ms := promptGateMS
	after, err := env.Herdr.AgentPrompt(ctx, herdr.AgentPromptParams{Target: cur.PaneID, Text: payload,
		Wait: &herdr.PromptWait{Until: []herdr.AgentStatus{herdr.StatusWorking, herdr.StatusBlocked}, TimeoutMS: &ms}})
	status, code := classify(err)
	if err == nil {
		rc.Result.StateAfter = string(after.AgentStatus)
		return finish(status, "", "")
	}
	reason = err.Error()
	if code == herdr.CodeAgentPromptStalled && rc.Baseline != nil {
		// exp.3: a fast turn can end inside herdr's five seconds. A
		// completion past the baseline makes delivery likely, but a person
		// or another writer could have caused it, so the status stays.
		if now, gerr := env.Herdr.AgentGet(ctx, cur.PaneID); gerr == nil && now.CompletionSeq != nil &&
			*now.CompletionSeq > rc.Baseline.StateChangeSeq {
			rc.Result.StateAfter = string(now.AgentStatus)
			reason += fmt.Sprintf("; a turn completed after the baseline (completion_seq %d > %d), so delivery is likely",
				*now.CompletionSeq, rc.Baseline.StateChangeSeq)
		}
	}
	return finish(status, code, reason)
}

// buildTemplate loads the template, or wraps the message as one that is
// never expanded: a plain prompt goes out as typed.
func buildTemplate(req Request) (*templates.Template, error) {
	if req.Template == "" {
		if req.BeadID != "" {
			return nil, ErrBeadNeedsT
		}
		msg := req.Message
		if msg == "" {
			msg = req.FileContent
		}
		if msg == "" {
			return nil, ErrNoPayload
		}
		return &templates.Template{Body: msg}, nil
	}
	if req.Message != "" {
		return nil, ErrBothInputs
	}
	tmpl, err := templates.NewLoader(req.ProjectRoot).Load(req.Template)
	if err != nil {
		return nil, err
	}
	return tmpl, nil
}

// render renders the shared text once, then each target's prompt. Any
// placeholder that nothing fills refuses the whole send before a prompt
// goes out.
func render(tmpl *templates.Template, req Request, bead *Bead, targets []Target) (string, []string, error) {
	if req.Template == "" {
		payloads := make([]string, len(targets))
		for i := range targets {
			payloads[i] = tmpl.Body
		}
		return tmpl.Body, payloads, nil
	}
	ctx := templates.ExecutionContext{Variables: maps.Clone(req.Vars), FileContent: req.FileContent,
		Session: req.Session, Project: req.ProjectRoot}
	if bead != nil {
		b := *bead
		ctx = ctx.WithBead(b.ID, b.Title, fmt.Sprintf("P%d", b.Priority), b.Description, b.Status, b.IssueType)
	}
	shared, err := tmpl.ExecuteShared(ctx)
	if err != nil {
		return "", nil, err
	}
	payloads := make([]string, len(targets))
	for i, t := range targets {
		pc := ctx.WithAgent(i+1, t.Binding.Kind, t.Binding.Model, t.Binding.PaneID).WithSendBatch(i, len(targets))
		pc.AgentName, pc.HerdrName, pc.Model = t.Binding.MailName, t.Binding.HerdrAlias, t.Binding.Model
		if payloads[i], err = tmpl.Execute(pc); err != nil {
			return "", nil, fmt.Errorf("%s (%s): %w", t.Binding.MailName, t.Key, err)
		}
	}
	return shared, payloads, nil
}

func allEqual(list []string) bool {
	for _, s := range list[1:] {
		if s != list[0] {
			return false
		}
	}
	return true
}

func appendHistory(env Env, req Request, opID, shared string, receipts []Receipt) {
	var keys, kinds []string
	ok := true
	for _, rc := range receipts {
		keys = append(keys, rc.Key)
		kinds = append(kinds, rc.Kind)
		ok = ok && rc.Status == StatusSubmitted
	}
	source := req.Source
	if source == "" {
		source = history.SourceCLI
	}
	e := history.NewEntry(req.Session, keys, shared, source)
	e.SetAgentTypes(kinds)
	e.Template, e.OperationID = req.Template, opID
	if ok {
		e.SetSuccess()
	} else {
		e.SetError(errors.New("not every target was submitted"))
	}
	// history.jsonl is a convenience for pfr and --cass-check; a failed
	// append never changes the send's outcome.
	_ = env.History(e)
}

// Stored reads an operation's outcome from the database.
func Stored(ctx context.Context, db *store.DB, opID string) (Result, error) {
	op, err := db.Operation(ctx, opID)
	if err != nil {
		return Result{}, err
	}
	if op.Verb != Verb {
		return Result{}, fmt.Errorf("operation %s is a %s, not a send", opID, op.Verb)
	}
	var sc opScope
	_ = json.Unmarshal([]byte(op.ScopeJSON), &sc)
	res := Result{OperationID: op.ID, Replayed: true, Session: sc.Session, WorkspaceID: sc.WorkspaceID,
		PayloadSHA256: sc.PayloadSHA256, PayloadBytes: sc.PayloadBytes, Preview: sc.Preview,
		MemoryInjection: notInjected, CASSInjection: notInjected, CreatedAt: op.CreatedAt, CompletedAt: op.CompletedAt,
		Warnings: []string{}}
	res.InProgress = op.CompletedAt == nil
	if sc.MemoryInjection != nil {
		res.MemoryInjection = *sc.MemoryInjection
	}
	if sc.CASSInjection != nil {
		res.CASSInjection = *sc.CASSInjection
	}
	if res.Injections, err = enrich.Exposures(ctx, db, opID); err != nil {
		return res, err
	}
	if res.Targets, err = Receipts(ctx, db, opID); err != nil {
		return res, err
	}
	sort.SliceStable(res.Targets, func(i, j int) bool { return res.Targets[i].TargetIndex < res.Targets[j].TargetIndex })
	if res.Targets == nil {
		res.Targets = []Receipt{}
	}
	return res, nil
}

// Unknown reports a target whose submission is not known: possibly
// submitted, or a dispatch that stopped after it started.
func Unknown(status string) bool {
	return slices.Contains([]string{StatusPossiblySubmitted, StatusAttemptStarted, StatusPending}, status)
}
