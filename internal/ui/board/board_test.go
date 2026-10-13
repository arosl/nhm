package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/store"
)

// fakeHerdr is a herdr server whose snapshot the test changes.
type fakeHerdr struct {
	mu    sync.Mutex
	snap  herdr.SessionSnapshot
	err   error
	calls atomic.Int64
	texts map[string]string
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.err
}

func (f *fakeHerdr) PaneRead(_ context.Context, p herdr.PaneReadParams) (herdr.PaneRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return herdr.PaneRead{PaneID: p.PaneID, Text: f.texts[p.PaneID]}, nil
}

func (f *fakeHerdr) set(fn func(s *herdr.SessionSnapshot)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.snap)
}

type fakeMail struct {
	mu      sync.Mutex
	inbox   map[string][]agentmail.Message
	err     error
	failFor map[string]error
	calls   atomic.Int64
}

func (f *fakeMail) FetchInbox(_ context.Context, _, agent string, _ int) ([]agentmail.Message, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if err := f.failFor[agent]; err != nil {
		return nil, err
	}
	return f.inbox[agent], nil
}

func u64(v uint64) *uint64 { return &v }

// The real client reads the preview.
var _ PaneReader = (*herdr.Client)(nil)

type fixture struct {
	db    *store.DB
	h     *fakeHerdr
	mail  *fakeMail
	bs    *bindings.Store
	icy   bindings.Binding
	blue  bindings.Binding
	gone  bindings.Binding
	beads atomic.Int64
}

// w1 holds IcyKnoll (bound, idle), BlueLake (bound, working) and an
// unbound codex; GoneFox's pane holds no agent. w2 holds RedStone.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr.sock", "default", t0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fx := &fixture{db: db, bs: bindings.NewStore(db), mail: &fakeMail{inbox: map[string][]agentmail.Message{}}}
	mk := func(name, ws, tab, pane, kind string) bindings.Binding {
		b, err := fx.bs.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: ws, TabID: tab, PaneID: pane,
				HerdrAlias: strings.ToLower(name), Kind: kind}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	fx.icy = mk("IcyKnoll", "w1", "w1:t1", "w1:p1", "claude")
	fx.blue = mk("BlueLake", "w1", "w1:t1", "w1:p2", "claude")
	fx.gone = mk("GoneFox", "w1", "w1:t2", "w1:p9", "claude")
	mk("RedStone", "w2", "w2:t1", "w2:p1", "agy")
	agent := func(pane, ws, kind, name string, st herdr.AgentStatus) herdr.AgentInfo {
		return herdr.AgentInfo{PaneID: pane, TabID: ws + ":t1", WorkspaceID: ws, Agent: kind, Name: name, AgentStatus: st,
			StateChangeSeq: u64(4)}
	}
	fx.h = &fakeHerdr{texts: map[string]string{}, snap: herdr.SessionSnapshot{Protocol: 22, Version: "0.9.3",
		Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w1", Number: 1, Label: "swarm"}, {WorkspaceID: "w2", Number: 2, Label: "other"}},
		Agents: []herdr.AgentInfo{
			agent("w1:p1", "w1", "claude", "icyknoll", herdr.StatusIdle),
			agent("w1:p2", "w1", "claude", "bluelake", herdr.StatusWorking),
			agent("w1:p3", "w1", "codex", "", herdr.StatusIdle),
			agent("w2:p1", "w2", "agy", "redstone", herdr.StatusDone),
		}}}
	return fx
}

func (fx *fixture) board(t *testing.T) *Board {
	t.Helper()
	b, err := New(Config{WorkspaceID: "w1", Reader: fx.h, Deps: snapshot.Deps{Herdr: fx.h, DB: fx.db, Mail: fx.mail,
		Guards: func(string, string) []guards.Result { return nil },
		Beads: func(context.Context, string) (map[string][]string, error) {
			fx.beads.Add(1)
			return map[string][]string{"IcyKnoll": {"nhm-2", "nhm-10"}}, nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

// settle refreshes every source and waits until no read is in flight.
func settle(t *testing.T, b *Board) {
	t.Helper()
	before := map[string]uint64{}
	for _, h := range b.Health() {
		before[h.Source] = h.Reads
	}
	b.Refresh()
	waitFor(t, "the sources", func() bool {
		for _, h := range b.Health() {
			if h.Reading || h.Reads == before[h.Source] {
				return false
			}
		}
		return true
	})
	b.Sync()
}

func names(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func TestBoardNeedsAWorkspace(t *testing.T) {
	fx := newFixture(t)
	if _, err := New(Config{Deps: snapshot.Deps{Herdr: fx.h}}); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("New without a workspace = %v", err)
	}
}

// The board shows its workspace only: agents in slot order, then the
// absent binding; mail and beads joined by key.
func TestBoardRows(t *testing.T) {
	fx := newFixture(t)
	fx.mail.inbox["IcyKnoll"] = []agentmail.Message{{ID: 1, Subject: "hi"}}
	b := fx.board(t)
	settle(t, b)
	rows := b.Sel.Rows()
	if got := strings.Join(names(rows), ","); got != "IcyKnoll,BlueLake,,GoneFox" {
		t.Fatalf("rows = %s", got)
	}
	icy := rows[0]
	if !icy.Mail.Known || icy.Mail.Value.Unread != 1 || !icy.Beads.Known || strings.Join(icy.Beads.Value, ",") != "nhm-2,nhm-10" {
		t.Fatalf("IcyKnoll = mail %+v beads %+v", icy.Mail, icy.Beads)
	}
	if blue := rows[1]; !blue.Mail.Known || blue.Mail.Value.Unread != 0 || !blue.Beads.Known || len(blue.Beads.Value) != 0 {
		t.Fatalf("BlueLake = mail %+v beads %+v", blue.Mail, blue.Beads)
	}
	if codex := rows[2]; codex.Key.Bound() || codex.Key.PaneID != "w1:p3" || codex.Mail.Known {
		t.Fatalf("the unbound codex = %+v", codex)
	}
	if gone := rows[3]; gone.Agent != nil || gone.Absent == nil || gone.Key.BindingID != fx.gone.ID {
		t.Fatalf("GoneFox = %+v", gone)
	}
	if p := b.Problems(time.Now()); len(p) != 0 {
		t.Fatalf("problems = %v", p)
	}
}

// Mail that cannot be read keeps the last inbox counts and says so; it
// never shows zero unread.
func TestBoardMailFailureKeepsLastValue(t *testing.T) {
	fx := newFixture(t)
	fx.mail.inbox["IcyKnoll"] = []agentmail.Message{{ID: 1}, {ID: 2}}
	b := fx.board(t)
	settle(t, b)
	fx.mail.mu.Lock()
	fx.mail.err = errors.New("agent mail is down")
	fx.mail.mu.Unlock()
	settle(t, b)
	icy := b.Sel.Rows()[0]
	if !icy.Mail.Known || icy.Mail.Value.Unread != 2 {
		t.Fatalf("IcyKnoll's mail after a failure = %+v", icy.Mail)
	}
	p := strings.Join(b.Problems(time.Now()), "\n")
	if !strings.Contains(p, "mail:") || !strings.Contains(p, "old") {
		t.Fatalf("problems = %q", p)
	}
}

// One inbox fails, the others answer: the failed one keeps its last
// counts; on a board that never read it, it stays unknown.
func TestBoardMailPartialFailure(t *testing.T) {
	fx := newFixture(t)
	fx.mail.inbox["IcyKnoll"] = []agentmail.Message{{ID: 1}, {ID: 2}, {ID: 3}}
	b := fx.board(t)
	settle(t, b)
	fx.mail.mu.Lock()
	fx.mail.failFor = map[string]error{"IcyKnoll": errors.New("inbox timeout")}
	fx.mail.mu.Unlock()
	settle(t, b)
	rows := b.Sel.Rows()
	if icy := rows[0]; !icy.Mail.Known || icy.Mail.Value.Unread != 3 || icy.Mail.Value.Status != snapshot.Fresh {
		t.Fatalf("IcyKnoll after its inbox failed = %+v", icy.Mail)
	}
	if blue := rows[1]; !blue.Mail.Known {
		t.Fatalf("BlueLake's answered inbox is unknown: %+v", blue.Mail)
	}
	fresh := fx.board(t)
	settle(t, fresh)
	if icy := fresh.Sel.Rows()[0]; icy.Mail.Known {
		t.Fatalf("a never-read failed inbox shows %+v", icy.Mail)
	}
}

// Merged joins the mail and beads into the agents, so attention sees
// unread mail, and a failing mail source shows as stale.
func TestMergedModelForAttention(t *testing.T) {
	fx := newFixture(t)
	fx.mail.inbox["IcyKnoll"] = []agentmail.Message{{ID: 1}, {ID: 2}}
	b := fx.board(t)
	settle(t, b)
	m := b.Merged()
	found := false
	for _, it := range snapshot.Attention(m) {
		if it.Kind == snapshot.AttnUnread && it.MailName == "IcyKnoll" {
			found = true
		}
	}
	if !found || m.Agents[0].Bead == nil || *m.Agents[0].Bead != "nhm-2" {
		t.Fatalf("attention %+v, bead %v", snapshot.Attention(m), m.Agents[0].Bead)
	}
	if b.Model().Agents[0].Mail != nil {
		t.Fatal("Merged changed the state feed's model")
	}
	fx.mail.mu.Lock()
	fx.mail.err = errors.New("down")
	fx.mail.mu.Unlock()
	settle(t, b)
	if s := b.Merged().Sources[snapshot.SourceMail]; s.Status != snapshot.Stale {
		t.Fatalf("mail source = %+v", s)
	}
}

// herdr down: the board keeps its last rows and says why; it does not
// empty the list (and so never invalidates every mark).
func TestBoardHerdrDownKeepsRows(t *testing.T) {
	fx := newFixture(t)
	b := fx.board(t)
	settle(t, b)
	b.Sel.Toggle()
	fx.h.mu.Lock()
	fx.h.err = errors.New("connection refused")
	fx.h.mu.Unlock()
	settle(t, b)
	if len(b.Sel.Rows()) != 4 || b.Sel.Marks()[0].State != MarkValid {
		t.Fatalf("rows %v, marks %+v", names(b.Sel.Rows()), b.Sel.Marks())
	}
	if p := strings.Join(b.Problems(time.Now()), "\n"); !strings.Contains(p, "state: herdr") {
		t.Fatalf("problems = %q", p)
	}
}

// Marks keep their identity: a recycled agent (same binding, new
// generation) is "replaced", a closed one "missing", a pane that now
// holds a bound agent replaces the unbound mark; none of them moves to
// the row now in its place. The cursor stays on its key through a new
// order.
func TestMarksByIdentity(t *testing.T) {
	fx := newFixture(t)
	b := fx.board(t)
	settle(t, b)
	// Mark IcyKnoll, BlueLake and the unbound codex.
	b.Sel.Toggle()
	b.Sel.Move(1)
	b.Sel.Toggle()
	b.Sel.Move(1)
	b.Sel.Toggle()
	if len(b.Sel.Marks()) != 3 {
		t.Fatalf("marks = %+v", b.Sel.Marks())
	}
	b.Sel.Move(-1) // the cursor on BlueLake
	// IcyKnoll is recycled in its pane; BlueLake's pane closes; the codex
	// is adopted as PaleOwl.
	ctx := context.Background()
	if _, err := fx.bs.Occupy(ctx, fx.icy.ID, fx.icy.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
		PaneID: "w1:p1", HerdrAlias: "icyknoll"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.bs.Create(ctx, bindings.New{MailName: "PaleOwl", MailProjectKey: "/p", ProjectID: "proj", Provenance: bindings.Adopted,
		Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p3", HerdrAlias: "paleowl", Kind: "codex"}}); err != nil {
		t.Fatal(err)
	}
	fx.h.set(func(s *herdr.SessionSnapshot) {
		var keep []herdr.AgentInfo
		for _, a := range s.Agents {
			if a.PaneID != "w1:p2" {
				keep = append(keep, a)
			}
		}
		s.Agents = keep
	})
	if _, err := fx.bs.Retire(ctx, fx.blue.ID, fx.blue.Generation, "closed"); err != nil {
		t.Fatal(err)
	}
	settle(t, b)
	got := map[string]string{}
	for _, m := range b.Sel.Marks() {
		got[m.Name] = m.State
	}
	want := map[string]string{"IcyKnoll": MarkReplaced, "BlueLake": MarkMissing, "": MarkReplaced}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("marks = %v, want %v", got, want)
		}
	}
	tg := b.Sel.Targets()
	if tg.From != FromMarks || len(tg.Rows) != 0 || len(tg.Invalid) != 3 {
		t.Fatalf("targets = %+v", tg)
	}
	// BlueLake is gone: the cursor stays at its place (the next row).
	if r, ok := b.Sel.Cursor(); !ok || r.Name != "PaleOwl" {
		t.Fatalf("cursor = %+v", r)
	}
}

// A target handed in from elsewhere (the context, the picker) is the
// same target when its binding and generation are, whatever else it
// names.
func TestKeysCompareByIdentity(t *testing.T) {
	fx := newFixture(t)
	b := fx.board(t)
	settle(t, b)
	if !b.Sel.SetCursor(Key{BindingID: fx.blue.ID, Generation: fx.blue.Generation}) {
		t.Fatal("a bare binding target did not find its row")
	}
	if r, _ := b.Sel.Cursor(); r.Name != "BlueLake" || r.Key.PaneID != "w1:p2" {
		t.Fatalf("cursor = %+v", r.Key)
	}
	b.Sel.Toggle()
	if !b.Sel.Marked(Key{BindingID: fx.blue.ID, Generation: fx.blue.Generation, PaneID: "elsewhere"}) {
		t.Fatal("the mark is not found by identity")
	}
	if b.Sel.SetCursor(Key{BindingID: fx.blue.ID, Generation: fx.blue.Generation + 1}) {
		t.Fatal("another generation found the row")
	}
}

// No marks: the cursor's row. No rows: nothing, and the caller opens
// the picker; it is never "all".
func TestTargetsNoImplicitAll(t *testing.T) {
	var s Selection
	if tg := s.Targets(); tg.From != FromNone || len(tg.Rows) != 0 {
		t.Fatalf("empty board targets = %+v", tg)
	}
	fx := newFixture(t)
	b := fx.board(t)
	settle(t, b)
	if tg := b.Sel.Targets(); tg.From != FromCursor || len(tg.Rows) != 1 || tg.Rows[0].Name != "IcyKnoll" {
		t.Fatalf("cursor targets = %+v", tg)
	}
	b.Sel.SetFilter("nobody")
	if tg := b.Sel.Targets(); tg.From != FromNone {
		t.Fatalf("filtered-out targets = %+v", tg)
	}
}

// A filter hides rows, not marks: a hidden mark still counts, and the
// review says how many it hides.
func TestFilterKeepsMarks(t *testing.T) {
	fx := newFixture(t)
	b := fx.board(t)
	settle(t, b)
	b.Sel.Toggle() // IcyKnoll
	b.Sel.SetFilter("BLUE")
	if got := names(b.Sel.Rows()); len(got) != 1 || got[0] != "BlueLake" {
		t.Fatalf("filtered rows = %v", got)
	}
	b.Sel.Toggle()
	tg := b.Sel.Targets()
	if len(tg.Rows) != 2 || tg.Hidden != 1 {
		t.Fatalf("targets = %+v", tg)
	}
	b.Sel.SetFilter("")
	if r, _ := b.Sel.Cursor(); r.Name != "BlueLake" {
		t.Fatalf("cursor after the filter = %s", r.Name)
	}
	b.Sel.Clear()
	if len(b.Sel.Marks()) != 0 || b.Sel.Filter() != "" {
		t.Fatal("Clear kept a mark or the filter")
	}
}

// The mail of an earlier generation is not the new occupant's.
func TestMailCellsMatchGeneration(t *testing.T) {
	m := &snapshot.Model{Agents: []snapshot.Agent{{PaneID: "w1:p1", Kind: "claude",
		Binding: &snapshot.BindingRef{ID: "bnd-1", Generation: 2, MailName: "IcyKnoll"}}}}
	mail := State[MailByKey]{Has: true, Value: MailByKey{Key{BindingID: "bnd-1", Generation: 1}.Key(): {Unread: 5}}}
	rows := BuildRows(m, mail, State[BeadsByKey]{})
	if rows[0].Mail.Known || rows[0].Beads.Known {
		t.Fatalf("row = %+v", rows[0])
	}
}

// The preview reads the cursor's pane only, without its control
// sequences, and is not shown for another row.
func TestPreview(t *testing.T) {
	fx := newFixture(t)
	fx.h.texts["w1:p1"] = "line one\n\x1b[31mred\x1b[0m\x1b]0;title\x07\n"
	fx.h.texts["w1:p2"] = "blue's pane"
	b := fx.board(t)
	settle(t, b)
	waitFor(t, "the preview", func() bool { _, ok := b.Preview(); return ok })
	p, _ := b.Preview()
	if p.PaneID != "w1:p1" || p.Text != "line one\nred" {
		t.Fatalf("preview = %+v", p)
	}
	b.Sel.Move(1)
	if _, ok := b.Preview(); ok {
		t.Fatal("IcyKnoll's preview shown on BlueLake's row")
	}
	b.FollowCursor()
	waitFor(t, "BlueLake's preview", func() bool { p, ok := b.Preview(); return ok && p.PaneID == "w1:p2" })
}

// N board refreshes record nothing: every table, the limits file and
// the mail read state stay as they were (plan-v4 section 4).
func TestBoardRefreshesAreReadOnly(t *testing.T) {
	fx := newFixture(t)
	limits := filepath.Join(t.TempDir(), "limits-seen.json")
	seen := []byte(`{"lines":{"w1:p1":"You've hit your limit"}}`)
	if err := os.WriteFile(limits, seen, 0o600); err != nil {
		t.Fatal(err)
	}
	fx.mail.inbox["IcyKnoll"] = []agentmail.Message{{ID: 7, Subject: "hi"}}
	b, err := New(Config{WorkspaceID: "w1", Reader: fx.h, Deps: snapshot.Deps{Herdr: fx.h, DB: fx.db, Mail: fx.mail,
		LimitsFile: limits, Guards: func(string, string) []guards.Result { return nil },
		Beads: func(context.Context, string) (map[string][]string, error) {
			return map[string][]string{"IcyKnoll": {"x-1"}}, nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	before := dumpStore(t, fx.db.SQL())
	for range 20 {
		settle(t, b)
	}
	if after := dumpStore(t, fx.db.SQL()); after != before {
		t.Fatalf("the store changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got, err := os.ReadFile(limits); err != nil || string(got) != string(seen) {
		t.Fatalf("limits file = %q %v", got, err)
	}
	if fx.mail.calls.Load() == 0 || b.Sel.Rows()[0].Agent.Limit == "" {
		t.Fatal("the sources were not read; the test proves nothing")
	}
}

// dumpStore renders every row of every table.
func dumpStore(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, tbl := range tables {
		r, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %q ORDER BY 1`, tbl))
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		fmt.Fprintf(&out, "[%s] %v\n", tbl, cols)
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for r.Next() {
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&out, "%v\t", v)
			}
			out.WriteString("\n")
		}
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}
