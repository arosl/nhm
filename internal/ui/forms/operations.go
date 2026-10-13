package forms

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

// OperationsReader reads the core's operation records
// (lifecycle.RecentOperations) and the UI's jobs (runner.List). It only
// reads.
type OperationsReader interface {
	RecentOperations(ctx context.Context, limit int) ([]lifecycle.RecentOperation, error)
	RecentJobs(limit int) ([]runner.Entry, error)
}

// OperationsRefresh is how often the view reads while it is open.
const OperationsRefresh = 2 * time.Second

// QuietAfter is how long an operation with no final outcome may go
// without a change before the view says its process may have ended.
const QuietAfter = 10 * time.Minute

// OpState is how far an operation got.
type OpState string

// Operation states.
const (
	// OpFinal: the operation has its final outcome.
	OpFinal OpState = "final"
	// OpOpen: no final outcome yet; it runs.
	OpOpen OpState = "open"
	// OpQuiet: no final outcome and no change for QuietAfter: its process
	// may have ended partway.
	OpQuiet OpState = "quiet"
	// OpUnknown: its job ended, but the record has no final outcome. It is
	// unknown until reconciled (diagnostics: unfinished operations);
	// nothing replays it.
	OpUnknown OpState = "unknown"
)

// OperationRow is one operation as the view shows it, in the core's
// words: a record of the core, or a UI job with no record of its own
// (new, reap, wake, or a job that ended before it claimed one).
type OperationRow struct {
	ID        string
	Verb      string
	Status    string
	State     OpState
	Workspace string
	Created   time.Time
	Updated   time.Time
	Completed *time.Time
	// Job is how the UI job of the same id ended, when one did.
	Job *runner.Result
	// Detail is the job's error, when it ended with one.
	Detail  string
	Targets []TargetRow
}

// TargetRow is one receipt.
type TargetRow struct {
	Index      int
	Name       string
	BindingID  string
	Generation int64
	Status     string
	ErrorCode  string
	// Detail is the receipt's reason: refusals, an error, a gate's reason.
	Detail string
}

// OperationsView is the menu's recent operations: what nhm started, and
// how it ended, also after the popup that started it closed (plan-v4
// sections 3 and 4). It reads every OperationsRefresh, one read at a
// time; the selection follows the operation, not the row.
type OperationsView struct {
	read  OperationsReader
	limit int
	now   func() time.Time

	request  uint64
	loading  bool
	rows     []OperationRow
	selected string
	err      error
	loadedAt time.Time
}

type opsLoaded struct {
	request uint64
	ops     []lifecycle.RecentOperation
	jobs    []runner.Entry
	err     error
}

type opsTick struct{ request uint64 }

// NewOperationsView reads up to limit operations (<= 0 is 20).
func NewOperationsView(read OperationsReader, limit int, now func() time.Time) *OperationsView {
	if now == nil {
		now = time.Now
	}
	if limit <= 0 {
		limit = 20
	}
	return &OperationsView{read: read, limit: limit, now: now}
}

// Rows are the operations, newest first.
func (v *OperationsView) Rows() []OperationRow { return v.rows }

// Err is the last read's error; the rows stay as last read.
func (v *OperationsView) Err() error { return v.err }

// LoadedAt is when the rows were read.
func (v *OperationsView) LoadedAt() time.Time { return v.loadedAt }

// Selected is the selected operation's id.
func (v *OperationsView) Selected() string { return v.selected }

// Select selects an operation by id.
func (v *OperationsView) Select(id string) { v.selected = id }

// Move moves the selection by delta rows.
func (v *OperationsView) Move(delta int) {
	if len(v.rows) == 0 {
		return
	}
	i := v.index() + delta
	i = max(0, min(i, len(v.rows)-1))
	v.selected = v.rows[i].ID
}

func (v *OperationsView) index() int {
	for i, r := range v.rows {
		if r.ID == v.selected {
			return i
		}
	}
	return 0
}

// Init reads.
func (v *OperationsView) Init() tea.Cmd { return v.load() }

// Refresh reads now, unless a read runs.
func (v *OperationsView) Refresh() tea.Cmd { return v.load() }

func (v *OperationsView) load() tea.Cmd {
	if v.loading {
		return nil
	}
	v.loading, v.request = true, nextRequest()
	read, id, limit := v.read, v.request, v.limit
	return func() tea.Msg {
		ops, err := read.RecentOperations(context.Background(), limit)
		jobs, jerr := read.RecentJobs(limit)
		return opsLoaded{request: id, ops: ops, jobs: jobs, err: errors.Join(err, jerr)}
	}
}

// Update takes the reads and the refresh ticks.
func (v *OperationsView) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case opsLoaded:
		if m.request != v.request {
			return nil
		}
		v.loading, v.err = false, m.err
		if m.err == nil {
			v.rows, v.loadedAt = rowsOf(m.ops, m.jobs, v.now(), v.limit), v.now()
			if len(v.rows) > 0 && v.rows[v.index()].ID != v.selected {
				v.selected = v.rows[0].ID
			}
		}
		id := v.request
		return tea.Tick(OperationsRefresh, func(time.Time) tea.Msg { return opsTick{request: id} })
	case opsTick:
		if m.request != v.request {
			return nil
		}
		return v.load()
	}
	return nil
}

func rowsOf(ops []lifecycle.RecentOperation, jobs []runner.Entry, now time.Time, limit int) []OperationRow {
	byID := map[string]runner.Entry{}
	for _, e := range jobs {
		byID[e.Job.ID] = e
	}
	rows := make([]OperationRow, 0, len(ops)+len(jobs))
	recorded := map[string]bool{}
	for _, op := range ops {
		r := OperationRow{ID: op.OperationID, Verb: clean(op.Verb), Status: clean(op.Status), Created: op.CreatedAt,
			Updated: op.UpdatedAt, Completed: op.Completed, State: OpOpen, Workspace: workspaceOf(op.Scope)}
		switch {
		case op.Completed != nil:
			r.State = OpFinal
		case now.Sub(op.UpdatedAt) >= QuietAfter:
			r.State = OpQuiet
		}
		if e, ok := byID[op.OperationID]; ok {
			recorded[op.OperationID] = true
			if r.Job = e.Result; r.Job != nil {
				r.Detail = clean(r.Job.Error)
				if op.Completed == nil {
					r.State = OpUnknown
				}
			}
		}
		for _, t := range op.Targets {
			r.Targets = append(r.Targets, targetRow(t))
		}
		rows = append(rows, r)
	}
	for _, e := range jobs {
		if !recorded[e.Job.ID] {
			rows = append(rows, jobRow(e, now))
		}
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].Created.After(rows[b].Created) })
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// jobRow is a UI job with no record of its own.
func jobRow(e runner.Entry, now time.Time) OperationRow {
	r := OperationRow{ID: e.Job.ID, Verb: clean(e.Job.Verb), Created: e.Job.CreatedAt, Updated: e.Job.CreatedAt,
		Workspace: workspaceOf(e.Job.Request), Job: e.Result}
	switch {
	case e.Result != nil:
		r.State, r.Completed, r.Updated, r.Detail = OpFinal, &e.Result.EndedAt, e.Result.EndedAt, clean(e.Result.Error)
		switch e.Result.Code {
		case runner.CodeOK:
			r.Status = "done"
		case runner.CodeRefused:
			r.Status = "refused"
		default:
			r.Status = "failed"
		}
	case e.Claimed:
		r.State, r.Status = OpOpen, "running"
	default:
		r.State, r.Status = OpOpen, "not started"
	}
	if r.State == OpOpen && now.Sub(e.Job.CreatedAt) >= QuietAfter {
		r.State = OpQuiet
	}
	return r
}

func workspaceOf(raw json.RawMessage) string {
	var v struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return clean(v.WorkspaceID)
}

// targetRow reads the keys the verbs share in a receipt's result:
// mail_name, and the reason as refusals, error or reason.
func targetRow(t lifecycle.OperationTarget) TargetRow {
	row := TargetRow{Index: t.Index, BindingID: t.BindingID, Generation: t.Generation, Status: clean(t.Status), ErrorCode: clean(t.ErrorCode)}
	var res struct {
		MailName string   `json:"mail_name"`
		Refusals []string `json:"refusals"`
		Error    string   `json:"error"`
		Reason   string   `json:"reason"`
	}
	_ = json.Unmarshal(t.Result, &res)
	row.Name = clean(res.MailName)
	if row.Name == "" {
		row.Name = t.BindingID
	}
	var detail []string
	for _, s := range append(res.Refusals, res.Error, res.Reason) {
		if s = clean(s); s != "" {
			detail = append(detail, s)
		}
	}
	row.Detail = strings.Join(detail, "; ")
	return row
}

// Clean removes terminal control sequences and control bytes other than
// newline and tab from outside text before a surface shows it (plan-v4
// section 4).
func Clean(s string) string { return clean(s) }

// ansiPattern matches CSI and OSC sequences and lone escapes (nhm read's
// pattern).
var ansiPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// clean removes terminal control sequences and control bytes other than
// newline and tab from stored text before it is shown (plan-v4 section
// 4).
func clean(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f) {
			return r
		}
		return -1
	}, s)
}
