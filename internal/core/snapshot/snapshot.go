// Package snapshot builds nhm's one read model (plan-v8 2.5): the herdr
// session, nhm's bindings and open operations, and bounded enrichment
// (Agent Mail inboxes, quota). status, list, snapshot, attention,
// activity, health, the board and the robot read flags all render it.
//
// It is built in three stages, each with its own deadline, and it is
// always truthful partial data: a source that fails or is slow gives
// "unknown" values and its health says why; it never gives zeros, and a
// failure of one source never drops what another source confirmed.
//
//  1. herdr: one session.snapshot (topology, agents, states, sequences);
//  2. local: bindings, open operations, receipts, quota rows, pending wakes;
//  3. enrichment: Agent Mail fetch_inbox with mark_read false, at most
//     eight calls in flight.
package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/store"
)

// Source health states, ntm's names.
const (
	Fresh       = "fresh"
	Stale       = "stale"
	Unavailable = "unavailable"
	Unknown     = "unknown"
	// Skipped: the caller did not ask for the source.
	Skipped = "skipped"
)

// Source names.
const (
	SourceHerdr    = "herdr"
	SourceBindings = "bindings"
	SourceMail     = "agent_mail"
	SourceQuota    = "quota"
	SourceBeads    = "beads"
	SourceBoards   = "boards"
)

// SourceHealth is how one source did in this build.
type SourceHealth struct {
	Source      string    `json:"source"`
	Status      string    `json:"status"`
	CollectedAt time.Time `json:"collected_at"`
	DurationMs  int64     `json:"duration_ms"`
	Error       string    `json:"error,omitempty"`
}

// Herdr is what the read model needs from herdr.
type Herdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
}

// Mail peeks at inboxes; *agentmail.Client has it.
type Mail interface {
	FetchInbox(ctx context.Context, projectKey, agent string, limit int) ([]agentmail.Message, error)
}

// Deps are the sources. Mail may be nil: the mail source is then skipped.
type Deps struct {
	Herdr Herdr
	DB    *store.DB
	Mail  Mail
	Now   func() time.Time
	// Guards reports a kind's guards in a project (guards.Check); nil
	// skips the guards.
	Guards func(kind, projectRoot string) []guards.Result
	// LimitsFile is the limits-seen.json that `nhm limits` keeps; "" or
	// a missing file means no limit was seen.
	LimitsFile string
	// Beads lists the beads in progress in a project's bead databases,
	// by assignee (an Agent Mail name); nil skips the beads.
	Beads func(ctx context.Context, projectRoot string) (map[string][]string, error)
	// Boards lists nhm's live boards by pane id (uilaunch.BoardsAt) in a
	// workspace, or in all for "", against the build's own snapshot: a
	// board's pane is no agent, even when herdr names one in it (plan-v4
	// section 7). nil skips the check.
	Boards func(ctx context.Context, snap herdr.SessionSnapshot, workspace string) (map[string]bool, error)
}

// Options select and bound a build.
type Options struct {
	// Workspace keeps one workspace (an id); empty keeps all.
	Workspace string
	// ProjectKey keeps the agents bound in one project, and the unbound
	// ones; empty keeps all.
	ProjectKey string
	// Deadlines per stage; zero means the default.
	HerdrTimeout time.Duration
	LocalTimeout time.Duration
	MailTimeout  time.Duration
	// QuotaStaleAfter marks a quota row older than this stale (default 15 min).
	QuotaStaleAfter time.Duration
}

const (
	defaultHerdrTimeout = 5 * time.Second
	defaultLocalTimeout = 3 * time.Second
	defaultMailTimeout  = 4 * time.Second
	defaultQuotaStale   = 15 * time.Minute
	mailInFlight        = 8
	mailPeekLimit       = 50
)

// MailState is one agent's inbox, when Agent Mail answered for it.
type MailState struct {
	Status      string `json:"status"` // fresh, unavailable, skipped
	Unread      int    `json:"unread"`
	Urgent      int    `json:"urgent"`
	PendingAcks int    `json:"pending_acks"`
	// OverdueAcks are acks owed for more than 30 minutes, Agent Mail's SLA.
	OverdueAcks int    `json:"overdue_acks"`
	Error       string `json:"error,omitempty"`
}

// Agent is one agent: herdr's view and nhm's binding, joined by pane.
type Agent struct {
	PaneID         string              `json:"pane_id"`
	TabID          string              `json:"tab_id"`
	WorkspaceID    string              `json:"workspace_id"`
	Slot           string              `json:"slot,omitempty"`
	HerdrName      string              `json:"herdr_name,omitempty"`
	Kind           string              `json:"kind"`
	DisplayAgent   string              `json:"display_agent,omitempty"`
	State          herdr.AgentStatus   `json:"state"`
	StateChangeSeq *uint64             `json:"state_change_seq,omitempty"`
	CompletionSeq  *uint64             `json:"completion_seq,omitempty"`
	Session        *herdr.AgentSession `json:"agent_session,omitempty"`
	Focused        bool                `json:"focused"`
	Cwd            string              `json:"cwd,omitempty"`
	Title          string              `json:"title,omitempty"`
	// ObservedAt is when this build saw the state.
	ObservedAt time.Time `json:"observed_at"`

	Binding *BindingRef `json:"binding,omitempty"`
	Mail    *MailState  `json:"mail,omitempty"`
	// Guards maps dcg, rch and agent_mail_stop to their states, when the
	// guards were checked.
	Guards map[string]guards.State `json:"guards,omitempty"`
	// Limit is the limit line `nhm limits` last saw on the agent's screen.
	Limit string `json:"limit,omitempty"`
	// Bead is the bound agent's bead in progress (br, in its project's
	// bead databases), the first by id when it has several; null when it
	// has none or the beads were not read (the beads source says why).
	// Beads lists them all (METHOD-MATRIX E14, acfs-9hkh.82).
	Bead  *string  `json:"bead"`
	Beads []string `json:"beads,omitempty"`
}

// BindingRef is the binding of an agent's pane.
type BindingRef struct {
	ID         string          `json:"binding_id"`
	Generation int64           `json:"generation"`
	Status     bindings.Status `json:"status"`
	MailName   string          `json:"mail_name"`
	ProjectKey string          `json:"mail_project_key"`
	Model      string          `json:"model,omitempty"`
	Alias      string          `json:"herdr_alias,omitempty"`
	// NameMatches is false when herdr shows another name than the alias.
	NameMatches bool `json:"name_matches"`
}

// Workspace is one herdr workspace (ntm's session).
type Workspace struct {
	ID         string `json:"workspace_id"`
	Label      string `json:"label"`
	Number     uint   `json:"number"`
	Focused    bool   `json:"focused"`
	TabCount   uint   `json:"tab_count"`
	PaneCount  uint   `json:"pane_count"`
	AgentCount int    `json:"agent_count"`
}

// Operation is an operation that has not completed.
type Operation struct {
	ID        string    `json:"operation_id"`
	Verb      string    `json:"verb"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Receipt is a send target whose submission is uncertain.
type Receipt struct {
	OperationID string `json:"operation_id"`
	BindingID   string `json:"binding_id,omitempty"`
	Status      string `json:"status"`
}

// Quota is one quota observation.
type Quota struct {
	PoolID     string    `json:"pool_id"`
	Window     string    `json:"window"`
	UsedPct    *float64  `json:"used_pct,omitempty"`
	Limited    bool      `json:"limited"`
	ResetsAt   string    `json:"resets_at,omitempty"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
	Stale      bool      `json:"stale"`
}

// Wake is a pending wake: mail nhm has not yet nudged its recipient for.
type Wake struct {
	BindingID string `json:"binding_id"`
	MessageID string `json:"message_id"`
	WakeAfter string `json:"wake_after,omitempty"`
	Attempts  int    `json:"attempts"`
}

// Model is the read model.
type Model struct {
	GeneratedAt  time.Time   `json:"generated_at"`
	HerdrVersion string      `json:"herdr_version,omitempty"`
	Protocol     int         `json:"herdr_protocol,omitempty"`
	FocusedPane  string      `json:"focused_pane_id,omitempty"`
	Workspaces   []Workspace `json:"workspaces"`
	Agents       []Agent     `json:"agents"`
	// Absent are active bindings whose pane holds no agent, or is gone;
	// Displaced are bindings waiting for adopt.
	Absent     []bindings.Binding      `json:"absent_bindings"`
	Displaced  []bindings.Binding      `json:"displaced_bindings"`
	Operations []Operation             `json:"open_operations"`
	Uncertain  []Receipt               `json:"uncertain_receipts"`
	Quota      []Quota                 `json:"quota"`
	Wakes      []Wake                  `json:"pending_wakes"`
	Sources    map[string]SourceHealth `json:"sources"`
	// herdrOK is false when the herdr stage failed: agent data is then
	// the bindings' last word, not herdr's.
	herdrOK bool
	// boards are nhm's live boards by pane id: no agent rows.
	boards map[string]bool
	// peers are the active bindings of every project, by workspace, for
	// the mixed-workspace check: a build for one project still sees the
	// other projects beside its agents (nhm-8ze).
	peers map[string][]peer
}

// peer is one active binding in a workspace: its id and project.
type peer struct{ id, project string }

// HerdrOK reports whether herdr answered.
func (m *Model) HerdrOK() bool { return m.herdrOK }

// Degraded lists the sources that are not fresh (skipped ones excluded).
func (m *Model) Degraded() []string {
	var out []string
	for name, h := range m.Sources {
		if h.Status != Fresh && h.Status != Skipped {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Build builds the model.
func Build(ctx context.Context, deps Deps, opts Options) *Model {
	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}
	m := &Model{GeneratedAt: now().UTC(), Workspaces: []Workspace{}, Agents: []Agent{}, Absent: []bindings.Binding{},
		Displaced: []bindings.Binding{}, Operations: []Operation{}, Uncertain: []Receipt{}, Quota: []Quota{}, Wakes: []Wake{},
		Sources: map[string]SourceHealth{}}

	// Stage 1: herdr.
	var snap herdr.SessionSnapshot
	m.stage(SourceHerdr, now, orDefault(opts.HerdrTimeout, defaultHerdrTimeout), ctx, func(c context.Context) error {
		var err error
		snap, err = deps.Herdr.Snapshot(c)
		return err
	})
	m.herdrOK = m.Sources[SourceHerdr].Status == Fresh
	if m.herdrOK {
		m.HerdrVersion, m.Protocol, m.FocusedPane = snap.Version, snap.Protocol, snap.FocusedPaneID
	} else {
		// A failed call contributes nothing, whatever it returned.
		snap = herdr.SessionSnapshot{}
	}
	if deps.Boards != nil && m.herdrOK {
		// A check that failed for one workspace still leaves out the
		// boards it found in the others.
		m.stage(SourceBoards, now, orDefault(opts.LocalTimeout, defaultLocalTimeout), ctx, func(c context.Context) error {
			var err error
			m.boards, err = deps.Boards(c, snap, opts.Workspace)
			return err
		})
	}

	// Stage 2: local state.
	var all []bindings.Binding
	if deps.DB == nil {
		for _, name := range []string{SourceBindings, SourceQuota} {
			m.Sources[name] = SourceHealth{Source: name, Status: Unavailable, CollectedAt: now().UTC(),
				Error: "this herdr server is not enrolled (nhm admin instance enroll)"}
		}
		m.join(snap, nil, opts)
		m.skipOrEnrich(ctx, deps, now, opts)
		return m
	}
	m.stage(SourceBindings, now, orDefault(opts.LocalTimeout, defaultLocalTimeout), ctx, func(c context.Context) error {
		var err error
		if all, err = bindings.NewStore(deps.DB).List(c, bindings.Filter{ProjectKey: opts.ProjectKey,
			Statuses: []bindings.Status{bindings.Active, bindings.Displaced}}); err != nil {
			return err
		}
		m.readPeers(c, deps.DB, all, opts.ProjectKey)
		return m.readLocal(c, deps.DB.SQL())
	})
	m.stage(SourceQuota, now, orDefault(opts.LocalTimeout, defaultLocalTimeout), ctx, func(c context.Context) error {
		return m.readQuota(c, deps.DB.SQL(), m.GeneratedAt, orDefault(opts.QuotaStaleAfter, defaultQuotaStale))
	})

	m.join(snap, all, opts)
	m.skipOrEnrich(ctx, deps, now, opts)
	return m
}

// readPeers keeps every workspace's active bindings, of all projects, for
// the mixed-workspace check: from the build's own list, or with a second
// read when the build is for one project (nhm-8ze). That read only serves
// the check: when it fails, peers stay unset and the check sees the
// build's rows, and the build goes on.
func (m *Model) readPeers(ctx context.Context, db *store.DB, all []bindings.Binding, projectKey string) {
	if projectKey != "" {
		var err error
		if all, err = bindings.NewStore(db).List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}}); err != nil {
			return
		}
	}
	m.peers = map[string][]peer{}
	for _, b := range all {
		if b.Status == bindings.Active && b.WorkspaceID != "" {
			m.peers[b.WorkspaceID] = append(m.peers[b.WorkspaceID], peer{b.ID, b.MailProjectKey})
		}
	}
}

// skipOrEnrich runs stage 3: the local files (guards, limits), the beads
// and then Agent Mail.
func (m *Model) skipOrEnrich(ctx context.Context, deps Deps, now func() time.Time, opts Options) {
	m.addGuards(deps.Guards)
	m.addLimits(deps.LimitsFile)
	m.addBeads(ctx, deps.Beads, now, orDefault(opts.LocalTimeout, defaultLocalTimeout))
	if deps.Mail == nil {
		m.Sources[SourceMail] = SourceHealth{Source: SourceMail, Status: Skipped, CollectedAt: now().UTC()}
		return
	}
	m.enrichMail(ctx, deps.Mail, now, orDefault(opts.MailTimeout, defaultMailTimeout))
}

// addGuards checks each agent's guards once per kind and project: a
// bound agent in its project, an unbound one with the user settings only.
func (m *Model) addGuards(check func(kind, projectRoot string) []guards.Result) {
	if check == nil {
		return
	}
	cache := map[string]map[string]guards.State{}
	for i := range m.Agents {
		a := &m.Agents[i]
		project := ""
		if a.Binding != nil {
			project = a.Binding.ProjectKey
		}
		key := a.Kind + "\x00" + project
		if _, ok := cache[key]; !ok {
			states := map[string]guards.State{}
			for _, r := range check(a.Kind, project) {
				states[r.Guard] = r.State
			}
			cache[key] = states
		}
		a.Guards = cache[key]
	}
}

// addBeads gives each bound agent its beads in progress: one read per
// project, under one deadline. A project whose beads cannot be read
// leaves its agents' bead null and makes the source unavailable, naming
// the project; the other projects still count.
func (m *Model) addBeads(ctx context.Context, list func(context.Context, string) (map[string][]string, error),
	now func() time.Time, d time.Duration) {
	if list == nil {
		m.Sources[SourceBeads] = SourceHealth{Source: SourceBeads, Status: Skipped, CollectedAt: now().UTC()}
		return
	}
	m.stage(SourceBeads, now, d, ctx, func(c context.Context) error {
		byProject := map[string]map[string][]string{}
		var failed []string
		for i := range m.Agents {
			a := &m.Agents[i]
			if a.Binding == nil || a.Binding.ProjectKey == "" {
				continue
			}
			root := a.Binding.ProjectKey
			beads, done := byProject[root]
			if !done {
				var err error
				if beads, err = list(c, root); err != nil {
					failed = append(failed, root+": "+err.Error())
					beads = nil
				}
				byProject[root] = beads
			}
			if ids := beads[a.Binding.MailName]; len(ids) > 0 {
				sorted := append([]string(nil), ids...)
				sort.Slice(sorted, func(i, j int) bool { return beadLess(sorted[i], sorted[j]) })
				a.Bead, a.Beads = &sorted[0], sorted
			}
		}
		if len(failed) > 0 {
			return errors.New(strings.Join(failed, "; "))
		}
		return nil
	})
}

// beadLess orders bead ids with their numbers as numbers: acfs-9 before
// acfs-10, acfs-9hkh.9 before acfs-9hkh.84. Ids that read as the same
// numbers (a-01, a-1) fall back to plain string order, so it is a strict
// order and every sort gives the same result.
func beadLess(a, b string) bool {
	oa, ob := a, b
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na := strings.TrimLeft(a[:da], "0")
			nb := strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return oa < ob
}

// digitRun is the length of s's leading run of ASCII digits.
func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// addLimits marks the agents whose pane showed a limit line.
func (m *Model) addLimits(path string) {
	if path == "" {
		return
	}
	seen, err := quota.LoadSeen(path)
	if err != nil {
		return
	}
	for i := range m.Agents {
		m.Agents[i].Limit = seen.Lines[m.Agents[i].PaneID]
	}
}

// ErrAmbiguousWorkspace means a label names more than one workspace.
var ErrAmbiguousWorkspace = errors.New("snapshot: the workspace label is ambiguous; give the workspace id")

// ErrNoWorkspace means no workspace has that label or id.
var ErrNoWorkspace = errors.New("snapshot: no such workspace")

// FindWorkspace finds a workspace by id or label (ntm's session name). A
// label that two workspaces share selects neither.
func (m *Model) FindWorkspace(nameOrID string) (Workspace, error) {
	var hits []Workspace
	for _, w := range m.Workspaces {
		if w.ID == nameOrID {
			return w, nil
		}
		if w.Label == nameOrID {
			hits = append(hits, w)
		}
	}
	switch len(hits) {
	case 0:
		return Workspace{}, fmt.Errorf("%w: %s", ErrNoWorkspace, nameOrID)
	case 1:
		return hits[0], nil
	default:
		ids := make([]string, len(hits))
		for i, w := range hits {
			ids[i] = w.ID
		}
		return Workspace{}, fmt.Errorf("%w: %q is %v", ErrAmbiguousWorkspace, nameOrID, ids)
	}
}

// Only returns a copy of the model that keeps one workspace.
func (m *Model) Only(wsID string) *Model {
	c := *m
	c.Workspaces, c.Agents = []Workspace{}, []Agent{}
	c.Absent, c.Displaced = []bindings.Binding{}, []bindings.Binding{}
	for _, w := range m.Workspaces {
		if w.ID == wsID {
			c.Workspaces = append(c.Workspaces, w)
		}
	}
	for _, a := range m.Agents {
		if a.WorkspaceID == wsID {
			c.Agents = append(c.Agents, a)
		}
	}
	for _, b := range m.Absent {
		if b.WorkspaceID == wsID {
			c.Absent = append(c.Absent, b)
		}
	}
	for _, b := range m.Displaced {
		if b.WorkspaceID == wsID {
			c.Displaced = append(c.Displaced, b)
		}
	}
	return &c
}

// stage runs one source with its deadline and records its health.
func (m *Model) stage(name string, now func() time.Time, d time.Duration, ctx context.Context, fn func(context.Context) error) {
	start := now()
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	err := fn(c)
	h := SourceHealth{Source: name, Status: Fresh, CollectedAt: start.UTC(), DurationMs: now().Sub(start).Milliseconds()}
	if err != nil {
		h.Status, h.Error = Unavailable, err.Error()
	}
	m.Sources[name] = h
}

// join puts herdr's agents and nhm's bindings together.
func (m *Model) join(snap herdr.SessionSnapshot, all []bindings.Binding, opts Options) {
	byPane := map[string]bindings.Binding{}
	for _, b := range all {
		switch b.Status {
		case bindings.Active:
			if b.PaneID != "" {
				byPane[b.PaneID] = b
			}
		case bindings.Displaced:
			if opts.Workspace == "" || b.WorkspaceID == opts.Workspace {
				m.Displaced = append(m.Displaced, b)
			}
		}
	}
	agentCount := map[string]int{}
	seen := map[string]bool{}
	for _, a := range snap.Agents {
		if a.Agent == "" || m.boards[a.PaneID] || (opts.Workspace != "" && a.WorkspaceID != opts.Workspace) {
			continue
		}
		b, bound := byPane[a.PaneID]
		if opts.ProjectKey != "" && bound && b.MailProjectKey != opts.ProjectKey {
			continue
		}
		ag := Agent{PaneID: a.PaneID, TabID: a.TabID, WorkspaceID: a.WorkspaceID, HerdrName: a.Name, Kind: a.Agent,
			DisplayAgent: a.DisplayAgent, State: a.AgentStatus, StateChangeSeq: a.StateChangeSeq,
			CompletionSeq: a.CompletionSeq, Session: a.AgentSession, Focused: a.Focused,
			Cwd: firstNonEmpty(a.ForegroundCwd, a.Cwd), Title: firstNonEmpty(a.Title, a.TerminalTitleStripped),
			ObservedAt: m.GeneratedAt}
		if bound {
			seen[a.PaneID] = true
			ag.Slot = b.Slot
			ag.Binding = &BindingRef{ID: b.ID, Generation: b.Generation, Status: b.Status, MailName: b.MailName,
				ProjectKey: b.MailProjectKey, Model: b.Model, Alias: b.HerdrAlias,
				NameMatches: b.HerdrAlias == "" || a.Name == b.HerdrAlias}
		}
		agentCount[a.WorkspaceID]++
		m.Agents = append(m.Agents, ag)
	}
	if m.herdrOK {
		for _, b := range byPane {
			if !seen[b.PaneID] && (opts.Workspace == "" || b.WorkspaceID == opts.Workspace) {
				m.Absent = append(m.Absent, b)
			}
		}
	}
	sort.Slice(m.Absent, func(i, j int) bool { return m.Absent[i].ID < m.Absent[j].ID })
	for _, w := range snap.Workspaces {
		if opts.Workspace != "" && w.WorkspaceID != opts.Workspace {
			continue
		}
		m.Workspaces = append(m.Workspaces, Workspace{ID: w.WorkspaceID, Label: w.Label, Number: w.Number,
			Focused: w.Focused, TabCount: w.TabCount, PaneCount: w.PaneCount, AgentCount: agentCount[w.WorkspaceID]})
	}
	sort.Slice(m.Workspaces, func(i, j int) bool { return m.Workspaces[i].Number < m.Workspaces[j].Number })
	sort.SliceStable(m.Agents, func(i, j int) bool {
		a, b := m.Agents[i], m.Agents[j]
		if a.WorkspaceID != b.WorkspaceID {
			return a.WorkspaceID < b.WorkspaceID
		}
		if a.Slot != b.Slot {
			return slotLess(a.Slot, b.Slot)
		}
		return a.PaneID < b.PaneID
	})
}

// slotLess orders "N.M" slots numerically; agents without a slot last.
func slotLess(a, b string) bool {
	if a == "" || b == "" {
		return b == "" && a != ""
	}
	sa, ea := bindings.ParseSlot(a)
	sb, eb := bindings.ParseSlot(b)
	if ea != nil || eb != nil {
		return a < b
	}
	if sa.Tab != sb.Tab {
		return sa.Tab < sb.Tab
	}
	return sa.Pane < sb.Pane
}

// readLocal reads the open operations, the uncertain receipts and the
// pending wakes.
func (m *Model) readLocal(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT operation_id, verb, status, created_at FROM operations
		WHERE completed_at IS NULL ORDER BY created_at`)
	if err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	for rows.Next() {
		var op Operation
		var created string
		if err := rows.Scan(&op.ID, &op.Verb, &op.Status, &created); err != nil {
			_ = rows.Close()
			return fmt.Errorf("operations: %w", err)
		}
		op.CreatedAt, _ = time.Parse(store.TimeFormat, created)
		m.Operations = append(m.Operations, op)
	}
	if err := closeRows(rows); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	rows, err = db.QueryContext(ctx, `SELECT operation_id, COALESCE(binding_id, ''), status FROM receipts
		WHERE status = 'possibly_submitted' ORDER BY operation_id, target_index`)
	if err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	for rows.Next() {
		var r Receipt
		if err := rows.Scan(&r.OperationID, &r.BindingID, &r.Status); err != nil {
			_ = rows.Close()
			return fmt.Errorf("receipts: %w", err)
		}
		m.Uncertain = append(m.Uncertain, r)
	}
	if err := closeRows(rows); err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	rows, err = db.QueryContext(ctx, `SELECT binding_id, message_id, COALESCE(wake_after, ''), attempts FROM wake
		WHERE announced_at IS NULL ORDER BY first_seen_at`)
	if err != nil {
		return fmt.Errorf("wake: %w", err)
	}
	for rows.Next() {
		var w Wake
		if err := rows.Scan(&w.BindingID, &w.MessageID, &w.WakeAfter, &w.Attempts); err != nil {
			_ = rows.Close()
			return fmt.Errorf("wake: %w", err)
		}
		m.Wakes = append(m.Wakes, w)
	}
	return closeRows(rows)
}

func (m *Model) readQuota(ctx context.Context, db *sql.DB, now time.Time, staleAfter time.Duration) error {
	rows, err := db.QueryContext(ctx, `SELECT pool_id, window, used_pct, limited, COALESCE(resets_at, ''), source, observed_at
		FROM quota ORDER BY pool_id, window`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var q Quota
		var used sql.NullFloat64
		var limited int
		var observed string
		if err := rows.Scan(&q.PoolID, &q.Window, &used, &limited, &q.ResetsAt, &q.Source, &observed); err != nil {
			_ = rows.Close()
			return err
		}
		if used.Valid {
			q.UsedPct = &used.Float64
		}
		q.Limited = limited != 0
		t, err := time.Parse(store.TimeFormat, observed)
		q.ObservedAt = t
		q.Stale = err != nil || now.Sub(t) > staleAfter
		m.Quota = append(m.Quota, q)
	}
	return closeRows(rows)
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

// enrichMail peeks at the inbox of every bound agent, at most eight at a
// time, within the stage deadline.
func (m *Model) enrichMail(ctx context.Context, mail Mail, now func() time.Time, d time.Duration) {
	start := now()
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	sem := make(chan struct{}, mailInFlight)
	var wg sync.WaitGroup
	var mu sync.Mutex
	asked, failed := 0, 0
	var lastErr error
	for i := range m.Agents {
		ag := &m.Agents[i]
		if ag.Binding == nil {
			continue
		}
		asked++
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-c.Done():
				mu.Lock()
				failed++
				lastErr = c.Err()
				ag.Mail = &MailState{Status: Unavailable, Error: c.Err().Error()}
				mu.Unlock()
				return
			}
			defer func() { <-sem }()
			msgs, err := mail.FetchInbox(c, ag.Binding.ProjectKey, ag.Binding.MailName, mailPeekLimit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				lastErr = err
				ag.Mail = &MailState{Status: Unavailable, Error: err.Error()}
				return
			}
			ag.Mail = mailState(msgs, now())
		}()
	}
	wg.Wait()
	h := SourceHealth{Source: SourceMail, Status: Fresh, CollectedAt: start.UTC(), DurationMs: now().Sub(start).Milliseconds()}
	switch {
	case asked == 0:
		h.Status = Fresh
	case failed == asked:
		h.Status = Unavailable
	case failed > 0:
		h.Status = Stale
	}
	if lastErr != nil {
		h.Error = fmt.Sprintf("%d of %d inboxes: %v", failed, asked, lastErr)
	}
	m.Sources[SourceMail] = h
}

// ackSLA is Agent Mail's acknowledgement SLA.
const ackSLA = 30 * time.Minute

func mailState(msgs []agentmail.Message, now time.Time) *MailState {
	u := agentmail.Summarize(msgs)
	s := &MailState{Status: Fresh, Unread: u.Unread, Urgent: u.Urgent, PendingAcks: u.PendingAcks}
	for _, msg := range msgs {
		if !msg.AckRequired || msg.AckTS != "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, msg.CreatedTS); err == nil && now.Sub(t) > ackSLA {
			s.OverdueAcks++
		}
	}
	return s
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
