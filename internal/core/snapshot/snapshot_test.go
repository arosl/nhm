package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

var t0 = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

type fakeHerdr struct {
	snap herdr.SessionSnapshot
	err  error
}

func (f fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) { return f.snap, f.err }

type fakeMail struct {
	inbox map[string][]agentmail.Message
	fail  map[string]error
}

func (f fakeMail) FetchInbox(_ context.Context, _, agent string, _ int) ([]agentmail.Message, error) {
	if err := f.fail[agent]; err != nil {
		return nil, err
	}
	return f.inbox[agent], nil
}

func u64(v uint64) *uint64 { return &v }

// session: w1 "swarm" with IcyKnoll (bound, idle), BlueLake (bound,
// blocked, herdr name cleared), an unbound codex; w2 "other" with a
// bound agent; a binding whose pane is gone, and a displaced one.
func setup(t *testing.T) (*store.DB, fakeHerdr) {
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
	bs := bindings.NewStore(db)
	ctx := context.Background()
	mk := func(name, ws, tab, pane, kind string) bindings.Binding {
		b, err := bs.Create(ctx, bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj", Provenance: bindings.Created,
			Occupant: bindings.Occupant{WorkspaceID: ws, TabID: tab, PaneID: pane, HerdrAlias: strings.ToLower(name), Kind: kind, Model: "m-" + name}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	mk("IcyKnoll", "w1", "w1:t1", "w1:p1", "claude")
	mk("BlueLake", "w1", "w1:t2", "w1:p2", "claude")
	mk("RedStone", "w2", "w2:t1", "w2:p1", "agy")
	mk("GoneFox", "w1", "w1:t3", "w1:p9", "claude")
	d := mk("PaleOwl", "w1", "w1:t4", "w1:p8", "codex")
	if _, err := bs.Displace(ctx, d.ID, 1, "SessionStart startup with an unknown session"); err != nil {
		t.Fatal(err)
	}
	agent := func(pane, ws, kind, name string, st herdr.AgentStatus) herdr.AgentInfo {
		return herdr.AgentInfo{PaneID: pane, TabID: ws + ":t", WorkspaceID: ws, Agent: kind, Name: name, AgentStatus: st,
			StateChangeSeq: u64(4), CompletionSeq: u64(4)}
	}
	h := fakeHerdr{snap: herdr.SessionSnapshot{Protocol: 22, Version: "0.9.3", FocusedPaneID: "w1:p1",
		Workspaces: []herdr.WorkspaceInfo{
			{WorkspaceID: "w2", Number: 2, Label: "other", TabCount: 1, PaneCount: 1},
			{WorkspaceID: "w1", Number: 1, Label: "swarm", Focused: true, TabCount: 3, PaneCount: 3},
		},
		Agents: []herdr.AgentInfo{
			agent("w1:p2", "w1", "claude", "", herdr.StatusBlocked),
			agent("w1:p1", "w1", "claude", "icyknoll", herdr.StatusIdle),
			agent("w1:p3", "w1", "codex", "", herdr.StatusWorking),
			agent("w2:p1", "w2", "agy", "redstone", herdr.StatusDone),
		}}}
	return db, h
}

func TestBuildJoinsHerdrAndBindings(t *testing.T) {
	db, h := setup(t)
	mail := fakeMail{inbox: map[string][]agentmail.Message{
		"IcyKnoll": {{ID: 1, Importance: "urgent", AckRequired: true, CreatedTS: t0.Add(-time.Hour).Format(time.RFC3339)}},
	}}
	m := Build(context.Background(), Deps{Herdr: h, DB: db, Mail: mail, Now: func() time.Time { return t0 }}, Options{})
	if !m.HerdrOK() || len(m.Degraded()) != 0 {
		t.Fatalf("sources = %+v", m.Sources)
	}
	if len(m.Agents) != 4 || m.Agents[0].Binding == nil || m.Agents[0].Binding.MailName != "IcyKnoll" || m.Agents[0].Slot != "0.1" {
		t.Fatalf("agents = %+v", m.Agents)
	}
	// Ordered by workspace, then slot; the unbound agent last in w1.
	order := []string{}
	for _, a := range m.Agents {
		order = append(order, a.PaneID)
	}
	if strings.Join(order, ",") != "w1:p1,w1:p2,w1:p3,w2:p1" {
		t.Fatalf("order = %v", order)
	}
	blue := m.Agents[1]
	if blue.Binding.NameMatches || blue.State != herdr.StatusBlocked {
		t.Fatalf("BlueLake = %+v", blue.Binding)
	}
	if !m.Agents[0].Binding.NameMatches || m.Agents[2].Binding != nil {
		t.Fatal("name match or unbound agent wrong")
	}
	icy := m.Agents[0].Mail
	if icy == nil || icy.Unread != 1 || icy.Urgent != 1 || icy.PendingAcks != 1 || icy.OverdueAcks != 1 {
		t.Fatalf("IcyKnoll mail = %+v", icy)
	}
	if m.Agents[2].Mail != nil {
		t.Fatal("an unbound agent got a mail state")
	}
	if len(m.Absent) != 1 || m.Absent[0].MailName != "GoneFox" || len(m.Displaced) != 1 || m.Displaced[0].MailName != "PaleOwl" {
		t.Fatalf("absent %+v displaced %+v", m.Absent, m.Displaced)
	}
	if len(m.Workspaces) != 2 || m.Workspaces[0].ID != "w1" || m.Workspaces[0].AgentCount != 3 {
		t.Fatalf("workspaces = %+v", m.Workspaces)
	}
}

func TestHerdrDownKeepsLocalTruth(t *testing.T) {
	db, h := setup(t)
	h.err = errors.New("dial unix: no such file")
	m := Build(context.Background(), Deps{Herdr: h, DB: db}, Options{})
	if m.HerdrOK() || m.Sources[SourceHerdr].Status != Unavailable || m.Sources[SourceMail].Status != Skipped {
		t.Fatalf("sources = %+v", m.Sources)
	}
	// No agents are invented, and no binding is called absent on missing data.
	if len(m.Agents) != 0 || len(m.Absent) != 0 || len(m.Displaced) != 1 {
		t.Fatalf("agents %d absent %d displaced %d", len(m.Agents), len(m.Absent), len(m.Displaced))
	}
	if got := m.Degraded(); len(got) != 1 || got[0] != SourceHerdr {
		t.Fatalf("degraded = %v", got)
	}
}

func TestMailFailuresAreUnknownNotZero(t *testing.T) {
	db, h := setup(t)
	mail := fakeMail{fail: map[string]error{"BlueLake": errors.New("connection refused")}}
	m := Build(context.Background(), Deps{Herdr: h, DB: db, Mail: mail}, Options{})
	if m.Sources[SourceMail].Status != Stale {
		t.Fatalf("mail source = %+v", m.Sources[SourceMail])
	}
	if s := m.Agents[1].Mail; s == nil || s.Status != Unavailable || s.Unread != 0 || s.Error == "" {
		t.Fatalf("BlueLake mail = %+v", s)
	}
	all := fakeMail{fail: map[string]error{"IcyKnoll": errors.New("x"), "BlueLake": errors.New("x"), "RedStone": errors.New("x")}}
	m = Build(context.Background(), Deps{Herdr: h, DB: db, Mail: all}, Options{})
	if m.Sources[SourceMail].Status != Unavailable {
		t.Fatalf("all failed = %+v", m.Sources[SourceMail])
	}
}

func TestNotEnrolled(t *testing.T) {
	_, h := setup(t)
	m := Build(context.Background(), Deps{Herdr: h}, Options{})
	if m.Sources[SourceBindings].Status != Unavailable || len(m.Agents) != 4 || m.Agents[0].Binding != nil {
		t.Fatalf("sources %+v agents %d", m.Sources, len(m.Agents))
	}
}

func TestFilters(t *testing.T) {
	db, h := setup(t)
	m := Build(context.Background(), Deps{Herdr: h, DB: db}, Options{})
	w, err := m.FindWorkspace("swarm")
	if err != nil || w.ID != "w1" {
		t.Fatalf("find = %+v %v", w, err)
	}
	if _, err := m.FindWorkspace("nope"); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("missing: %v", err)
	}
	one := m.Only("w2")
	if len(one.Agents) != 1 || len(one.Workspaces) != 1 || len(one.Absent) != 0 || len(m.Agents) != 4 {
		t.Fatalf("only w2 = %d agents; the original keeps %d", len(one.Agents), len(m.Agents))
	}
	h.snap.Workspaces = append(h.snap.Workspaces, herdr.WorkspaceInfo{WorkspaceID: "w3", Number: 3, Label: "swarm"})
	m = Build(context.Background(), Deps{Herdr: h, DB: db}, Options{})
	if _, err := m.FindWorkspace("swarm"); !errors.Is(err, ErrAmbiguousWorkspace) {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := m.FindWorkspace("w1"); err != nil {
		t.Fatalf("an id still selects: %v", err)
	}
	p := Build(context.Background(), Deps{Herdr: h, DB: db}, Options{ProjectKey: "/other"})
	for _, a := range p.Agents {
		if a.Binding != nil {
			t.Fatalf("a binding of another project: %+v", a)
		}
	}
}

func TestAttention(t *testing.T) {
	db, h := setup(t)
	if _, err := db.SQL().Exec(`INSERT INTO quota (pool_id, window, used_pct, limited, resets_at, source, observed_at)
		VALUES ('claude-max', '5h', 91, 1, '2026-10-12T09:00:00Z', 'statusline', ?), ('codex-pro', 'week', 10, 0, NULL, 'codex', ?)`,
		store.FormatTime(t0), store.FormatTime(t0.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	mail := fakeMail{inbox: map[string][]agentmail.Message{"IcyKnoll": {{ID: 1, Importance: "normal"}}}}
	m := Build(context.Background(), Deps{Herdr: h, DB: db, Mail: mail, Now: func() time.Time { return t0 }}, Options{})
	items := Attention(m)
	kinds := map[string]int{}
	for _, it := range items {
		kinds[it.Kind]++
	}
	for _, k := range []string{AttnBlocked, AttnDone, AttnUnread, AttnAbsent, AttnDisplaced, AttnNameMismatch, AttnUnbound, AttnQuotaLimited, AttnQuotaStale} {
		if kinds[k] != 1 {
			t.Errorf("%s: %d items", k, kinds[k])
		}
	}
	if items[0].Severity != SevCritical || items[0].Kind != AttnBlocked {
		t.Fatalf("first item = %+v", items[0])
	}
	if c := CountBySeverity(items); c[SevCritical] != 1 {
		t.Fatalf("counts = %v", c)
	}
}

func TestRobotProjections(t *testing.T) {
	db, h := setup(t)
	m := Build(context.Background(), Deps{Herdr: h, DB: db}, Options{})
	items := Attention(m)
	st := Status(m, items, BuildInfo{Version: "0.1.0"}, "json", t0)
	if !st.Success || st.SchemaID != robot.StatusSchemaID || len(st.Sessions) != 2 || st.Sessions[0].Name != "swarm" ||
		st.Sessions[0].AgentCount != 3 || st.Sessions[0].Health.Status != "degraded" || !st.Sessions[0].Attached {
		t.Fatalf("status = %+v", st)
	}
	if st.Summary.TotalAgents != 4 || st.Summary.ClaudeCount != 2 || st.Summary.AntigravityCount != 1 || st.Summary.CodexCount != 1 ||
		st.Summary.AgentsByState["blocked"] != 1 || st.OverallStatus != "critical" || st.System.TmuxOK {
		t.Fatalf("summary = %+v %s", st.Summary, st.OverallStatus)
	}
	sn := Snapshot(m, items, "json", t0)
	if len(sn.Sessions) != 2 || sn.Sessions[0].Agents[0].Pane != "0.1" || sn.Sessions[0].Agents[2].Pane != "" ||
		sn.ActiveIncidents == nil || len(sn.Alerts) == 0 || sn.ReplayWindow.Supported {
		t.Fatalf("snapshot = %+v", sn)
	}
	w, _ := m.FindWorkspace("swarm")
	act := ActivityFor(m, w, "json", t0)
	states := map[string]string{}
	for _, a := range act.Agents {
		states[a.Pane+"/"+a.AgentType] = a.State
	}
	if states["0.1/claude"] != robot.ActivityWaiting || states["1.1/claude"] != robot.ActivityStalled || states["/codex"] != robot.ActivityGenerating {
		t.Fatalf("activity = %v", states)
	}
	if act.Summary.TotalAgents != 3 || act.SourceHealth["herdr"].Status != Fresh {
		t.Fatalf("activity summary = %+v", act)
	}
	tail := TailFor(m, w, map[string]herdr.PaneRead{"w1:p1": {Text: "one\ntwo\n"}}, map[string]error{"w1:p2": errors.New("gone")}, "json", t0)
	if got := tail.Panes["0.1"].Lines; len(got) != 2 || got[1] != "two" || tail.Panes["1.1"].CaptureProvenance != Unavailable || tail.Panes["w1:p3"].State != "active" {
		t.Fatalf("tail = %+v", tail.Panes)
	}
}

func TestActivityMapping(t *testing.T) {
	for st, want := range map[herdr.AgentStatus]string{herdr.StatusWorking: robot.ActivityGenerating, herdr.StatusIdle: robot.ActivityWaiting,
		herdr.StatusDone: robot.ActivityWaiting, herdr.StatusBlocked: robot.ActivityStalled, herdr.StatusUnknown: robot.ActivityUnknown, "new": robot.ActivityUnknown} {
		if got, _, _ := Activity(st); got != want {
			t.Errorf("%s = %s, want %s", st, got, want)
		}
	}
	if NTMType("agy") != "antigravity" || NTMType("") != "unknown" || NTMType("pi") != "pi" {
		t.Error("NTMType")
	}
}
