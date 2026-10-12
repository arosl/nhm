// Package bindings keeps nhm's bindings: which herdr pane holds which
// Agent Mail identity, with a generation of its own (plan-v8 sections 1,
// 2.7, 2.10, 2.12).
//
// A binding is one identity in one project. Its generation counts the
// occupants that have held the identity: a new occupant (adopt with a
// handoff, recycle, a resume) bumps it; a compaction or a rename does
// not. Every change names the generation it expects, so a change made on
// stale facts fails with ErrStale instead of moving the identity twice.
//
// A slot is ntm's pane number "N.M" (2.12): N is the order of the
// binding's tab in the workspace, from 0; M counts that tab's bindings,
// from 1. It is persisted at the first binding in a workspace and never
// computed from the layout. A slot is never given to another binding in
// that workspace; a recycle keeps it; a move to another workspace takes a
// new one there and keeps the old row.
package bindings

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/store"
)

// Status is a binding's state.
type Status string

const (
	// Active: the identity is in the binding's pane.
	Active Status = "active"
	// Displaced: the pane's occupant changed and nhm could not prove
	// continuity; the binding waits for adopt.
	Displaced Status = "displaced"
	// Retired: the identity is gone; the row stays for history.
	Retired Status = "retired"
)

// Provenance says how the identity came to nhm.
type Provenance string

const (
	Created  Provenance = "created"  // nhm created the identity (spawn)
	Adopted  Provenance = "adopted"  // bound by hand (adopt)
	Imported Provenance = "imported" // verified import (recover reconcile)
)

// Errors.
var (
	ErrNotFound     = errors.New("bindings: no such binding")
	ErrStale        = errors.New("bindings: the binding changed since it was read")
	ErrIdentityBusy = errors.New("bindings: the identity already has an active binding")
	ErrPaneBusy     = errors.New("bindings: the pane already has an active binding")
	ErrAmbiguous    = errors.New("bindings: more than one binding matches")
)

// NativeSession is the agent's own session, as herdr or a hook reported
// it.
type NativeSession struct {
	Source     string   `json:"source,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Value      string   `json:"value"`
	ResumeArgv []string `json:"resume_argv,omitempty"`
}

// Evidence is one fact a binding was verified on.
type Evidence struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Binding is one row of the bindings table, with its current slot.
type Binding struct {
	ID             string         `json:"binding_id"`
	Generation     int64          `json:"generation"`
	Status         Status         `json:"status"`
	StatusReason   string         `json:"status_reason,omitempty"`
	MailName       string         `json:"mail_name"`
	MailProjectKey string         `json:"mail_project_key"`
	ProjectID      string         `json:"project_id"`
	WorkspaceID    string         `json:"workspace_id,omitempty"`
	TabID          string         `json:"tab_id,omitempty"`
	PaneID         string         `json:"pane_id,omitempty"`
	HerdrAlias     string         `json:"herdr_alias,omitempty"`
	Kind           string         `json:"kind"`
	Model          string         `json:"model,omitempty"`
	LaunchID       string         `json:"launch_id,omitempty"`
	NativeSession  *NativeSession `json:"native_session,omitempty"`
	// ForegroundPID and ForegroundStart identify the agent process: its
	// pid and its start time in clock ticks since boot (/proc). Zero when
	// unknown.
	ForegroundPID   int64      `json:"foreground_pid,omitempty"`
	ForegroundStart int64      `json:"foreground_start,omitempty"`
	Provenance      Provenance `json:"identity_provenance"`
	CreatedByOp     string     `json:"created_by_op,omitempty"`
	Evidence        []Evidence `json:"evidence"`
	Slot            string     `json:"slot,omitempty"`
	CompactedAt     *time.Time `json:"compacted_at,omitempty"`
	ResumedAt       *time.Time `json:"resumed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Occupant is the agent that holds an identity: where it is and what it
// is.
type Occupant struct {
	WorkspaceID     string
	TabID           string
	PaneID          string
	HerdrAlias      string
	Kind            string
	Model           string
	LaunchID        string
	NativeSession   *NativeSession
	ForegroundPID   int64
	ForegroundStart int64
	Evidence        []Evidence
}

// New is a binding to create.
type New struct {
	MailName       string
	MailProjectKey string
	ProjectID      string
	Provenance     Provenance
	CreatedByOp    string
	Occupant
}

// Store reads and writes bindings and slots in an instance's database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over db.
func NewStore(db *store.DB) *Store {
	return &Store{db: db.SQL(), now: time.Now}
}

// NewID returns a fresh binding id; it is also a valid secrets file name.
func NewID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("bindings: id: %w", err)
	}
	return "bnd-" + hex.EncodeToString(b), nil
}

// Create inserts an active binding with generation 1 and gives it a slot
// in its workspace when the occupant names a workspace and a tab.
func (s *Store) Create(ctx context.Context, n New) (Binding, error) {
	if n.MailName == "" || n.MailProjectKey == "" || n.ProjectID == "" || n.Kind == "" {
		return Binding{}, errors.New("bindings: create needs a mail name, project and kind")
	}
	switch n.Provenance {
	case Created, Adopted, Imported:
	default:
		return Binding{}, fmt.Errorf("bindings: unknown provenance %q", n.Provenance)
	}
	id, err := NewID()
	if err != nil {
		return Binding{}, err
	}
	now := store.FormatTime(s.now())
	native, evidence, err := encodeJSON(n.NativeSession, n.Evidence)
	if err != nil {
		return Binding{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkFree(ctx, tx, "", n.MailProjectKey, n.MailName, n.PaneID); err != nil {
		return Binding{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bindings (binding_id, generation, status, mail_name, mail_project_key,
		project_id, workspace_id, tab_id, pane_id, herdr_alias, kind, model, launch_id, native_session_json,
		foreground_pid, foreground_start, identity_provenance, created_by_op, evidence_json, created_at, updated_at)
		VALUES (?, 1, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, n.MailName, n.MailProjectKey, n.ProjectID, null(n.WorkspaceID), null(n.TabID), null(n.PaneID),
		null(n.HerdrAlias), n.Kind, null(n.Model), null(n.LaunchID), native,
		nullInt(n.ForegroundPID), nullInt(n.ForegroundStart), string(n.Provenance), null(n.CreatedByOp), evidence, now, now)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: create %s: %w", n.MailName, err)
	}
	if err := allocateSlot(ctx, tx, id, n.WorkspaceID, n.TabID, now); err != nil {
		return Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, fmt.Errorf("bindings: %w", err)
	}
	return s.Get(ctx, id)
}

// Occupy gives an identity a new occupant: the generation goes up by one,
// the binding becomes active, and the occupant's facts replace the old
// ones. The slot stays when the workspace stays; a new workspace (or a
// first workspace) gives a new slot. It fails with ErrStale when the
// generation is not expected.
func (s *Store) Occupy(ctx context.Context, id string, expected int64, o Occupant) (Binding, error) {
	b, err := s.Get(ctx, id)
	if err != nil {
		return Binding{}, err
	}
	if b.Status == Retired {
		return Binding{}, fmt.Errorf("bindings: %s (%s) is retired", id, b.MailName)
	}
	if o.Kind == "" {
		o.Kind = b.Kind
	}
	native, evidence, err := encodeJSON(o.NativeSession, o.Evidence)
	if err != nil {
		return Binding{}, err
	}
	now := store.FormatTime(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkFree(ctx, tx, id, b.MailProjectKey, b.MailName, o.PaneID); err != nil {
		return Binding{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE bindings SET generation = generation + 1, status = 'active',
		status_reason = NULL, workspace_id = ?, tab_id = ?, pane_id = ?, herdr_alias = ?, kind = ?, model = ?,
		launch_id = ?, native_session_json = ?, foreground_pid = ?, foreground_start = ?, evidence_json = ?,
		compacted_at = NULL, resumed_at = NULL, updated_at = ?
		WHERE binding_id = ? AND generation = ?`,
		null(o.WorkspaceID), null(o.TabID), null(o.PaneID), null(o.HerdrAlias), o.Kind, null(o.Model),
		null(o.LaunchID), native, nullInt(o.ForegroundPID), nullInt(o.ForegroundStart), evidence, now, id, expected)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: occupy %s: %w", id, err)
	}
	if err := oneRow(res, id, expected); err != nil {
		return Binding{}, err
	}
	if o.WorkspaceID != "" && (o.WorkspaceID != b.WorkspaceID || b.Slot == "") {
		if err := allocateSlot(ctx, tx, id, o.WorkspaceID, o.TabID, now); err != nil {
			return Binding{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, fmt.Errorf("bindings: %w", err)
	}
	return s.Get(ctx, id)
}

// Displace marks an active binding displaced: its pane holds someone else
// now, or nhm cannot prove that it does not. The generation stays.
func (s *Store) Displace(ctx context.Context, id string, expected int64, reason string) (Binding, error) {
	return s.setStatus(ctx, id, expected, Displaced, reason)
}

// Retire marks a binding retired. The generation stays.
func (s *Store) Retire(ctx context.Context, id string, expected int64, reason string) (Binding, error) {
	return s.setStatus(ctx, id, expected, Retired, reason)
}

func (s *Store) setStatus(ctx context.Context, id string, expected int64, st Status, reason string) (Binding, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE bindings SET status = ?, status_reason = ?, updated_at = ?
		WHERE binding_id = ? AND generation = ? AND status != 'retired'`,
		string(st), null(reason), store.FormatTime(s.now()), id, expected)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: %s %s: %w", st, id, err)
	}
	if err := oneRow(res, id, expected); err != nil {
		return Binding{}, err
	}
	return s.Get(ctx, id)
}

// checkFree refuses when another active binding holds the identity or
// the pane; self is the binding being changed ("" for a new one).
func (s *Store) checkFree(ctx context.Context, tx *sql.Tx, self, projectKey, name, pane string) error {
	var other string
	err := tx.QueryRowContext(ctx, `SELECT binding_id FROM bindings WHERE status = 'active'
		AND mail_project_key = ? AND lower(mail_name) = lower(?) AND binding_id != ?`, projectKey, name, self).Scan(&other)
	if err == nil {
		return fmt.Errorf("%w: %s is bound by %s", ErrIdentityBusy, name, other)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("bindings: %w", err)
	}
	if pane == "" {
		return nil
	}
	err = tx.QueryRowContext(ctx, `SELECT binding_id FROM bindings WHERE status = 'active'
		AND pane_id = ? AND binding_id != ?`, pane, self).Scan(&other)
	if err == nil {
		return fmt.Errorf("%w: %s is bound by %s", ErrPaneBusy, pane, other)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("bindings: %w", err)
	}
	return nil
}

func oneRow(res sql.Result, id string, expected int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("bindings: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("%w: %s at generation %d", ErrStale, id, expected)
	}
	return nil
}

// Get reads one binding.
func (s *Store) Get(ctx context.Context, id string) (Binding, error) {
	list, err := s.query(ctx, `WHERE b.binding_id = ?`, id)
	if err != nil {
		return Binding{}, err
	}
	if len(list) == 0 {
		return Binding{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return list[0], nil
}

// ActiveByName finds the active binding of a mail name. Mail names are
// unique without regard to case. An empty projectKey searches every
// project and fails with ErrAmbiguous when two projects match.
func (s *Store) ActiveByName(ctx context.Context, projectKey, name string) (Binding, error) {
	where, args := `WHERE b.status = 'active' AND lower(b.mail_name) = lower(?)`, []any{name}
	if projectKey != "" {
		where += ` AND b.mail_project_key = ?`
		args = append(args, projectKey)
	}
	return s.one(ctx, name, where, args...)
}

// ActiveByPane finds the active binding of a herdr pane.
func (s *Store) ActiveByPane(ctx context.Context, paneID string) (Binding, error) {
	return s.one(ctx, paneID, `WHERE b.status = 'active' AND b.pane_id = ?`, paneID)
}

// ActiveByAlias finds the active binding with a herdr alias.
func (s *Store) ActiveByAlias(ctx context.Context, alias string) (Binding, error) {
	return s.one(ctx, alias, `WHERE b.status = 'active' AND b.herdr_alias = ?`, alias)
}

// Filter selects bindings for List; empty fields match everything.
type Filter struct {
	ProjectKey  string
	WorkspaceID string
	MailName    string
	Statuses    []Status
}

// List returns the bindings that match f, newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Binding, error) {
	var conds []string
	var args []any
	if f.ProjectKey != "" {
		conds = append(conds, "b.mail_project_key = ?")
		args = append(args, f.ProjectKey)
	}
	if f.WorkspaceID != "" {
		conds = append(conds, "b.workspace_id = ?")
		args = append(args, f.WorkspaceID)
	}
	if f.MailName != "" {
		conds = append(conds, "lower(b.mail_name) = lower(?)")
		args = append(args, f.MailName)
	}
	if len(f.Statuses) > 0 {
		marks := make([]string, len(f.Statuses))
		for i, st := range f.Statuses {
			marks[i] = "?"
			args = append(args, string(st))
		}
		conds = append(conds, "b.status IN ("+strings.Join(marks, ", ")+")")
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return s.query(ctx, where+" ORDER BY b.updated_at DESC, b.binding_id", args...)
}

func (s *Store) one(ctx context.Context, what, where string, args ...any) (Binding, error) {
	list, err := s.query(ctx, where, args...)
	if err != nil {
		return Binding{}, err
	}
	switch len(list) {
	case 0:
		return Binding{}, fmt.Errorf("%w: %s", ErrNotFound, what)
	case 1:
		return list[0], nil
	default:
		ids := make([]string, len(list))
		for i, b := range list {
			ids[i] = b.ID + " (" + b.MailProjectKey + ")"
		}
		return Binding{}, fmt.Errorf("%w: %s: %s", ErrAmbiguous, what, strings.Join(ids, ", "))
	}
}

// query selects bindings with their current slot: the newest slot row in
// the binding's workspace.
func (s *Store) query(ctx context.Context, where string, args ...any) ([]Binding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT b.binding_id, b.generation, b.status, b.status_reason, b.mail_name,
		b.mail_project_key, b.project_id, b.workspace_id, b.tab_id, b.pane_id, b.herdr_alias, b.kind, b.model,
		b.launch_id, b.native_session_json, b.foreground_pid, b.foreground_start, b.identity_provenance,
		b.created_by_op, b.evidence_json, b.compacted_at, b.resumed_at, b.created_at, b.updated_at,
		(SELECT s.slot FROM slots s WHERE s.binding_id = b.binding_id AND s.workspace_id = b.workspace_id
			ORDER BY s.assigned_at DESC LIMIT 1)
		FROM bindings b `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("bindings: %w", err)
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		var reason, ws, tab, pane, alias, model, launch, native, op, compacted, resumed, slot sql.NullString
		var pid, start sql.NullInt64
		var status, prov, evidence, created, updated string
		if err := rows.Scan(&b.ID, &b.Generation, &status, &reason, &b.MailName, &b.MailProjectKey, &b.ProjectID,
			&ws, &tab, &pane, &alias, &b.Kind, &model, &launch, &native, &pid, &start, &prov, &op, &evidence,
			&compacted, &resumed, &created, &updated, &slot); err != nil {
			return nil, fmt.Errorf("bindings: %w", err)
		}
		b.Status, b.Provenance = Status(status), Provenance(prov)
		b.StatusReason, b.WorkspaceID, b.TabID, b.PaneID = reason.String, ws.String, tab.String, pane.String
		b.HerdrAlias, b.Model, b.LaunchID, b.CreatedByOp, b.Slot = alias.String, model.String, launch.String, op.String, slot.String
		b.ForegroundPID, b.ForegroundStart = pid.Int64, start.Int64
		if native.Valid && native.String != "" {
			b.NativeSession = &NativeSession{}
			if err := json.Unmarshal([]byte(native.String), b.NativeSession); err != nil {
				return nil, fmt.Errorf("bindings: %s: native session: %w", b.ID, err)
			}
		}
		if err := json.Unmarshal([]byte(evidence), &b.Evidence); err != nil {
			return nil, fmt.Errorf("bindings: %s: evidence: %w", b.ID, err)
		}
		var perr error
		if b.CreatedAt, perr = time.Parse(store.TimeFormat, created); perr != nil {
			return nil, fmt.Errorf("bindings: %s: created_at: %w", b.ID, perr)
		}
		if b.UpdatedAt, perr = time.Parse(store.TimeFormat, updated); perr != nil {
			return nil, fmt.Errorf("bindings: %s: updated_at: %w", b.ID, perr)
		}
		if b.CompactedAt, perr = parseOptTime(compacted); perr != nil {
			return nil, fmt.Errorf("bindings: %s: compacted_at: %w", b.ID, perr)
		}
		if b.ResumedAt, perr = parseOptTime(resumed); perr != nil {
			return nil, fmt.Errorf("bindings: %s: resumed_at: %w", b.ID, perr)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("bindings: %w", err)
	}
	return out, nil
}

// Slot is a parsed "N.M".
type Slot struct {
	Tab  int
	Pane int
}

func (s Slot) String() string { return strconv.Itoa(s.Tab) + "." + strconv.Itoa(s.Pane) }

// ParseSlot parses "N.M".
func ParseSlot(text string) (Slot, error) {
	n, m, ok := strings.Cut(text, ".")
	if !ok {
		return Slot{}, fmt.Errorf("bindings: slot %q is not N.M", text)
	}
	tab, err1 := strconv.Atoi(n)
	pane, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || tab < 0 || pane < 0 {
		return Slot{}, fmt.Errorf("bindings: slot %q is not N.M", text)
	}
	return Slot{Tab: tab, Pane: pane}, nil
}

// allocateSlot gives the binding the next slot of its tab in the
// workspace. The tab keeps the N of its first slot; a tab new to the
// workspace gets the next N. M is one more than the tab's highest, so a
// closed slot is never handed out again. Without a workspace or a tab
// there is no slot.
func allocateSlot(ctx context.Context, tx *sql.Tx, bindingID, workspaceID, tabID, now string) error {
	if workspaceID == "" || tabID == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT slot, tab_id FROM slots WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return fmt.Errorf("bindings: slots: %w", err)
	}
	tabN, maxN, maxM := -1, -1, map[int]int{}
	for rows.Next() {
		var text string
		var tab sql.NullString
		if err := rows.Scan(&text, &tab); err != nil {
			_ = rows.Close()
			return fmt.Errorf("bindings: slots: %w", err)
		}
		sl, err := ParseSlot(text)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if sl.Tab > maxN {
			maxN = sl.Tab
		}
		if sl.Pane > maxM[sl.Tab] {
			maxM[sl.Tab] = sl.Pane
		}
		if tab.String == tabID {
			tabN = sl.Tab
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("bindings: slots: %w", err)
	}
	if tabN < 0 {
		tabN = maxN + 1
	}
	slot := Slot{Tab: tabN, Pane: maxM[tabN] + 1}
	_, err = tx.ExecContext(ctx, `INSERT INTO slots (workspace_id, slot, binding_id, assigned_at, tab_id)
		VALUES (?, ?, ?, ?, ?)`, workspaceID, slot.String(), bindingID, now, tabID)
	if err != nil {
		return fmt.Errorf("bindings: slot %s in %s: %w", slot, workspaceID, err)
	}
	return nil
}

func encodeJSON(native *NativeSession, evidence []Evidence) (any, string, error) {
	var n any
	if native != nil {
		data, err := json.Marshal(native)
		if err != nil {
			return nil, "", fmt.Errorf("bindings: native session: %w", err)
		}
		n = string(data)
	}
	if evidence == nil {
		evidence = []Evidence{}
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return nil, "", fmt.Errorf("bindings: evidence: %w", err)
	}
	return n, string(data), nil
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func parseOptTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := time.Parse(store.TimeFormat, v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
