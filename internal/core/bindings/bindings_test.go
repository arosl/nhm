package bindings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

var t0 = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

func openStore(t *testing.T) (*store.DB, *Store) {
	t.Helper()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", t0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewStore(db)
	tick := t0
	s.now = func() time.Time { tick = tick.Add(time.Second); return tick }
	return db, s
}

func newBinding(name, project, ws, tab, pane string) New {
	return New{MailName: name, MailProjectKey: project, ProjectID: "proj-" + project, Provenance: Created,
		Occupant: Occupant{WorkspaceID: ws, TabID: tab, PaneID: pane, HerdrAlias: strings.ToLower(name), Kind: "claude"}}
}

func TestCreateGivesGenerationOneAndSlots(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	want := map[string]string{"IcyKnoll": "0.1", "BlueLake": "0.2", "RedStone": "1.1"}
	for _, n := range []New{
		newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"),
		newBinding("BlueLake", "/p", "w1", "w1:t1", "w1:p2"),
		newBinding("RedStone", "/p", "w1", "w1:t2", "w1:p3"),
	} {
		b, err := s.Create(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		if b.Generation != 1 || b.Status != Active || b.Slot != want[n.MailName] || !strings.HasPrefix(b.ID, "bnd-") {
			t.Errorf("%s = gen %d %s slot %s id %s", n.MailName, b.Generation, b.Status, b.Slot, b.ID)
		}
	}
	// Another workspace counts from 0.1 again.
	b, err := s.Create(ctx, newBinding("GreenCastle", "/p", "w2", "w2:t1", "w2:p1"))
	if err != nil || b.Slot != "0.1" {
		t.Fatalf("w2 slot = %s %v", b.Slot, err)
	}
	// No tab, no slot.
	b, err = s.Create(ctx, newBinding("PinkFox", "/p", "w1", "", "w1:p9"))
	if err != nil || b.Slot != "" {
		t.Fatalf("slot without a tab = %q %v", b.Slot, err)
	}
}

func TestOneActiveBindingPerIdentityAndPane(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, newBinding("icyknoll", "/p", "w1", "w1:t2", "w1:p2")); !errors.Is(err, ErrIdentityBusy) {
		t.Fatalf("same name, other case: %v", err)
	}
	if _, err := s.Create(ctx, newBinding("BlueLake", "/p", "w1", "w1:t1", "w1:p1")); !errors.Is(err, ErrPaneBusy) {
		t.Fatalf("same pane: %v", err)
	}
	// The same name in another project is another identity.
	if _, err := s.Create(ctx, newBinding("IcyKnoll", "/q", "w1", "w1:t3", "w1:p3")); err != nil {
		t.Fatalf("other project: %v", err)
	}
	if _, err := s.ActiveByName(ctx, "", "icyknoll"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("a name in two projects: %v", err)
	}
	if b, err := s.ActiveByName(ctx, "/q", "ICYKNOLL"); err != nil || b.PaneID != "w1:p3" {
		t.Fatalf("by name in /q = %+v %v", b, err)
	}
}

func TestOccupyBumpsTheGenerationAndKeepsTheSlot(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	b, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	occ := Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "codex", Model: "gpt",
		NativeSession: &NativeSession{Source: "nhm:codex", Value: "thread-1"}, ForegroundPID: 42, ForegroundStart: 7,
		Evidence: []Evidence{{Kind: "test", Detail: "x"}}}
	b2, err := s.Occupy(ctx, b.ID, 1, occ)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Generation != 2 || b2.Slot != "0.1" || b2.Kind != "codex" || b2.NativeSession.Value != "thread-1" || b2.ForegroundPID != 42 || len(b2.Evidence) != 1 {
		t.Fatalf("occupied = %+v", b2)
	}
	if _, err := s.Occupy(ctx, b.ID, 1, occ); !errors.Is(err, ErrStale) {
		t.Fatalf("stale occupy: %v", err)
	}
	// A move to another workspace takes a slot there.
	occ.WorkspaceID, occ.TabID, occ.PaneID = "w2", "w2:t5", "w2:p7"
	b3, err := s.Occupy(ctx, b.ID, 2, occ)
	if err != nil || b3.Generation != 3 || b3.Slot != "0.1" || b3.WorkspaceID != "w2" {
		t.Fatalf("moved = %+v %v", b3, err)
	}
}

func TestClosedSlotsAreNotReused(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	a, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retire(ctx, a.ID, a.Generation, "done"); err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, newBinding("BlueLake", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil || b.Slot != "0.2" {
		t.Fatalf("after a retire the slot is %s, %v", b.Slot, err)
	}
	if r, _ := s.Get(ctx, a.ID); r.Slot != "0.1" || r.StatusReason != "done" {
		t.Fatalf("retired keeps its slot and reason: %+v", r)
	}
}

func TestDisplaceRetireAndRetiredIsFinal(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	b, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Displace(ctx, b.ID, 1, "SessionStart startup with an unknown session")
	if err != nil || d.Status != Displaced || d.Generation != 1 {
		t.Fatalf("displaced = %+v %v", d, err)
	}
	if _, err := s.ActiveByPane(ctx, "w1:p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a displaced binding is active by pane: %v", err)
	}
	if _, err := s.Displace(ctx, b.ID, 5, "x"); !errors.Is(err, ErrStale) {
		t.Fatalf("stale displace: %v", err)
	}
	if _, err := s.Retire(ctx, b.ID, 1, "retired"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Occupy(ctx, b.ID, 1, Occupant{PaneID: "w1:p1"}); err == nil {
		t.Fatal("a retired binding took an occupant")
	}
	if _, err := s.Displace(ctx, b.ID, 1, "x"); !errors.Is(err, ErrStale) {
		t.Fatalf("a retired binding was displaced: %v", err)
	}
	list, err := s.List(ctx, Filter{ProjectKey: "/p", Statuses: []Status{Retired}})
	if err != nil || len(list) != 1 {
		t.Fatalf("list retired = %d %v", len(list), err)
	}
}

func TestCreateValidates(t *testing.T) {
	_, s := openStore(t)
	n := newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1")
	n.Provenance = "guessed"
	if _, err := s.Create(context.Background(), n); err == nil {
		t.Fatal("unknown provenance accepted")
	}
	n = newBinding("", "/p", "w1", "w1:t1", "w1:p1")
	if _, err := s.Create(context.Background(), n); err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestParseSlot(t *testing.T) {
	if s, err := ParseSlot("2.13"); err != nil || s.Tab != 2 || s.Pane != 13 || s.String() != "2.13" {
		t.Fatalf("ParseSlot = %+v %v", s, err)
	}
	for _, bad := range []string{"", "1", "a.b", "1.-1", "1.2.3"} {
		if _, err := ParseSlot(bad); err == nil {
			t.Errorf("ParseSlot(%q) accepted", bad)
		}
	}
}

func TestAlias(t *testing.T) {
	if a, err := Alias("IcyKnoll"); err != nil || a != "icyknoll" {
		t.Fatalf("Alias = %q %v", a, err)
	}
	for _, bad := range []string{"", "9Lives", "Has Space", strings.Repeat("a", 33)} {
		if _, err := Alias(bad); err == nil {
			t.Errorf("Alias(%q) accepted", bad)
		}
	}
}

// fakeHerdr is a session snapshot and process table.
type fakeHerdr struct {
	snap       herdr.SessionSnapshot
	procs      map[string][]herdr.Process
	renamed    map[string]string
	paneLabels map[string]string
	fail       error
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) { return f.snap, f.fail }

func (f *fakeHerdr) PaneProcessInfo(_ context.Context, pane string) (herdr.ProcessInfo, error) {
	return herdr.ProcessInfo{PaneID: pane, ForegroundProcesses: f.procs[pane]}, nil
}

func (f *fakeHerdr) AgentRename(_ context.Context, target, name string) (herdr.AgentInfo, error) {
	if f.renamed == nil {
		f.renamed = map[string]string{}
	}
	f.renamed[target] = name
	for i := range f.snap.Agents {
		if f.snap.Agents[i].PaneID == target {
			f.snap.Agents[i].Name = name
		}
	}
	return herdr.AgentInfo{PaneID: target, Name: name}, nil
}

func (f *fakeHerdr) PaneRename(_ context.Context, pane, label string) (herdr.PaneInfo, error) {
	if f.paneLabels == nil {
		f.paneLabels = map[string]string{}
	}
	f.paneLabels[pane] = label
	return herdr.PaneInfo{PaneID: pane}, nil
}

func (f *fakeHerdr) TabGet(_ context.Context, tab string) (herdr.TabInfo, error) {
	for _, t := range f.snap.Tabs {
		if t.TabID == tab {
			return t, nil
		}
	}
	return herdr.TabInfo{}, errors.New("tab_not_found: " + tab)
}

func (f *fakeHerdr) TabRename(_ context.Context, tab, label string) (herdr.TabInfo, error) {
	for i := range f.snap.Tabs {
		if f.snap.Tabs[i].TabID == tab {
			f.snap.Tabs[i].Label = label
			return f.snap.Tabs[i], nil
		}
	}
	return herdr.TabInfo{}, errors.New("tab_not_found: " + tab)
}

// tabLabel is the label of a tab in the fake session.
func (f *fakeHerdr) tabLabel(tab string) string {
	t, _ := f.TabGet(context.Background(), tab)
	return t.Label
}

// session builds a snapshot: one tab per pane, labelled, with an agent.
func session(panes ...paneSpec) *fakeHerdr {
	f := &fakeHerdr{procs: map[string][]herdr.Process{}}
	for i, p := range panes {
		tab := "w1:t" + string(rune('1'+i))
		f.snap.Tabs = append(f.snap.Tabs, herdr.TabInfo{TabID: tab, WorkspaceID: "w1", Label: p.tabLabel, PaneCount: 1})
		f.snap.Panes = append(f.snap.Panes, herdr.PaneInfo{PaneID: p.pane, TabID: tab, WorkspaceID: "w1", Cwd: p.cwd, Agent: p.kind})
		if p.kind != "" {
			a := herdr.AgentInfo{PaneID: p.pane, TabID: tab, WorkspaceID: "w1", Agent: p.kind, Name: p.alias,
				AgentStatus: herdr.StatusIdle, Cwd: p.cwd}
			if p.session != "" {
				a.AgentSession = &herdr.AgentSession{Source: "herdr:claude", Agent: p.kind, Kind: "id", Value: p.session}
			}
			f.snap.Agents = append(f.snap.Agents, a)
		}
		if p.pid != 0 {
			f.procs[p.pane] = []herdr.Process{{PID: uint32(p.pid), Name: p.kind}}
		}
	}
	return f
}

type paneSpec struct {
	pane, kind, alias, tabLabel, cwd, session string
	pid                                       int64
}

func fakeProcStart(t *testing.T, starts map[int64]int64) {
	t.Helper()
	old := ProcStart
	ProcStart = func(pid int64) (int64, error) {
		if s, ok := starts[pid]; ok {
			return s, nil
		}
		return 0, os.ErrNotExist
	}
	t.Cleanup(func() { ProcStart = old })
}

func TestVerify(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	fakeProcStart(t, map[int64]int64{100: 5000})
	n := newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1")
	n.NativeSession = &NativeSession{Value: "sess-1"}
	n.ForegroundPID, n.ForegroundStart = 100, 5000
	b, err := s.Create(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		h    *fakeHerdr
		want Verdict
	}{
		{"all match", session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", session: "sess-1", pid: 100}), Verified},
		{"alias cleared, session matches", session(paneSpec{pane: "w1:p1", kind: "claude", session: "sess-1", pid: 100}), Verified},
		{"another session", session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", session: "sess-2", pid: 100}), Mismatch},
		{"another kind", session(paneSpec{pane: "w1:p1", kind: "codex", alias: "icyknoll"}), Mismatch},
		{"pid gone", session(paneSpec{pane: "w1:p1", kind: "claude", session: "sess-1", pid: 101}), Mismatch},
		{"no agent", session(paneSpec{pane: "w1:p1"}), Absent},
		{"no pane", session(paneSpec{pane: "w1:p8", kind: "claude"}), Absent},
	}
	for _, c := range cases {
		v, err := Verify(ctx, c.h, b)
		if err != nil || v.Verdict != c.want {
			t.Errorf("%s: verdict %s, want %s (%+v) %v", c.name, v.Verdict, c.want, v.Checks, err)
		}
	}
	// Only the kind matches: nothing contradicts, nothing confirms.
	plain, err := s.Create(ctx, newBinding("BlueLake", "/p", "w1", "w1:t2", "w1:p2"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := Verify(ctx, session(paneSpec{pane: "w1:p2", kind: "claude"}), plain)
	if err != nil || v.Verdict != Unverified {
		t.Fatalf("kind only: %s %v", v.Verdict, err)
	}
	// A reused pid with another start time is a mismatch.
	fakeProcStart(t, map[int64]int64{100: 9999})
	v, _ = Verify(ctx, session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", pid: 100}), b)
	if v.Verdict != Mismatch {
		t.Fatalf("reused pid: %s", v.Verdict)
	}
}

func TestResolveFindsByNameAliasPaneAndCurrent(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	b, err := s.Create(ctx, newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	h := session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll"})
	env := func(k string) string {
		if k == "HERDR_PANE_ID" {
			return "w1:p1"
		}
		return ""
	}
	for _, q := range []Query{{Target: "IcyKnoll"}, {Target: "icyknoll"}, {Target: "w1:p1"}, {Current: true}, {Target: "IcyKnoll", ProjectKey: "/p"}} {
		r, err := Resolve(ctx, s, h, q, env)
		if err != nil || r.Binding.ID != b.ID || r.Verification.Verdict != Verified {
			t.Errorf("%+v: %+v %v", q, r, err)
		}
	}
	if _, err := Resolve(ctx, s, h, Query{Target: "Nobody"}, env); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown name: %v", err)
	}
	if _, err := Resolve(ctx, s, h, Query{Current: true}, func(string) string { return "" }); err == nil {
		t.Error("--current outside herdr resolved")
	}
	// A retired identity resolves to its last binding, unverified.
	if _, err := s.Retire(ctx, b.ID, 1, "done"); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(ctx, s, h, Query{Target: "IcyKnoll"}, env)
	if err != nil || r.Binding.Status != Retired || r.Verification.Verdict != NotActive {
		t.Fatalf("retired: %+v %v", r, err)
	}
}

func testEnv(t *testing.T, h *fakeHerdr) (Env, string) {
	t.Helper()
	db, s := openStore(t)
	home := t.TempDir()
	tick := t0
	return Env{DB: db, Bindings: s, Herdr: h, ConfigHome: home, Now: func() time.Time { tick = tick.Add(time.Second); return tick }}, home
}

func TestAdoptCreatesRenamesAndWritesTheIdentityFile(t *testing.T) {
	fakeProcStart(t, map[int64]int64{300: 77})
	h := session(paneSpec{pane: "w1:p1", kind: "claude", session: "sess-9", pid: 300, tabLabel: "5"})
	env, home := testEnv(t, h)
	project := t.TempDir()
	ctx := context.Background()
	res, err := Adopt(ctx, env, AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p1", ProjectKey: project, ProjectID: "proj-x", Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	b := res.Binding
	if !res.Created || b.Provenance != Adopted || b.Generation != 1 || b.Slot != "0.1" || b.ForegroundPID != 300 ||
		b.ForegroundStart != 77 || b.NativeSession == nil || b.Model != "opus" || b.CreatedByOp != res.OperationID {
		t.Fatalf("adopted = %+v", res)
	}
	if h.renamed["w1:p1"] != "icyknoll" {
		t.Fatalf("herdr rename = %v", h.renamed)
	}
	// The labels, as spawn sets them (nhm-qyz): the pane "<Name> <model>",
	// the tab, which holds only this pane, "<Name>".
	if h.paneLabels["w1:p1"] != "IcyKnoll opus" || h.tabLabel("w1:t1") != "IcyKnoll" || len(res.Warnings) != 0 {
		t.Fatalf("labels: pane %v, tab %q, warnings %v", h.paneLabels, h.tabLabel("w1:t1"), res.Warnings)
	}
	rec, ok, err := agentmail.ReadIdentity(home, project, "w1:p1")
	if err != nil || !ok || rec.Name != "IcyKnoll" || res.IdentityPath == "" {
		t.Fatalf("identity file = %+v %v %v", rec, ok, err)
	}
	op, err := env.DB.Operation(ctx, res.OperationID)
	if err != nil || op.Verb != "adopt" || op.Status != "done" {
		t.Fatalf("operation = %+v %v", op, err)
	}
	// Adopting the same identity again is refused, with the way forward.
	_, err = Adopt(ctx, env, AdoptRequest{MailName: "icyknoll", PaneID: "w1:p1", ProjectKey: project, ProjectID: "proj-x"})
	if !errors.Is(err, ErrIdentityBusy) || !strings.Contains(err.Error(), "--handoff-from "+b.ID) {
		t.Fatalf("second adopt: %v", err)
	}
}

func TestAdoptHandoffBumpsTheGeneration(t *testing.T) {
	h := session(paneSpec{pane: "w1:p1", kind: "codex", alias: "icyknoll"}, paneSpec{pane: "w1:p2", kind: "claude"})
	env, home := testEnv(t, h)
	project := t.TempDir()
	ctx := context.Background()
	old, err := env.Bindings.Create(ctx, newBinding("IcyKnoll", project, "w1", "w1:t1", "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentmail.WriteIdentity(home, project, "w1:p1", "IcyKnoll", t0); err != nil {
		t.Fatal(err)
	}
	// While the old occupant runs and the binding is active, no handoff.
	if _, err := Adopt(ctx, env, AdoptRequest{PaneID: "w1:p2", HandoffFrom: old.ID}); err == nil || !strings.Contains(err.Error(), "still has a codex agent") {
		t.Fatalf("handoff from a live occupant: %v", err)
	}
	if _, err := env.Bindings.Displace(ctx, old.ID, 1, "converted by casr"); err != nil {
		t.Fatal(err)
	}
	res, err := Adopt(ctx, env, AdoptRequest{PaneID: "w1:p2", HandoffFrom: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	b := res.Binding
	if res.Created || b.ID != old.ID || b.Generation != 2 || b.Status != Active || b.PaneID != "w1:p2" || b.Kind != "claude" || b.Slot != "0.1" {
		t.Fatalf("handoff = %+v", b)
	}
	if _, ok, _ := agentmail.ReadIdentity(home, project, "w1:p1"); ok {
		t.Fatal("the old pane still names the identity")
	}
	if rec, ok, _ := agentmail.ReadIdentity(home, project, "w1:p2"); !ok || rec.Name != "IcyKnoll" {
		t.Fatal("the new pane does not name the identity")
	}
	// A handoff to another name is refused.
	if _, err := Adopt(ctx, env, AdoptRequest{MailName: "BlueLake", PaneID: "w1:p2", HandoffFrom: old.ID}); err == nil {
		t.Fatal("handoff under another name accepted")
	}
}

// A tab that holds other panes keeps its label: it names more than the
// adopted agent.
func TestAdoptLeavesASharedTabsLabel(t *testing.T) {
	h := session(paneSpec{pane: "w1:p1", kind: "claude", tabLabel: "work"})
	h.snap.Tabs[0].PaneCount = 2
	env, _ := testEnv(t, h)
	res, err := Adopt(context.Background(), env, AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p1", ProjectKey: t.TempDir(), ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if h.paneLabels["w1:p1"] != "IcyKnoll" || h.tabLabel("w1:t1") != "work" || len(res.Warnings) != 0 {
		t.Fatalf("labels: pane %v, tab %q, warnings %v", h.paneLabels, h.tabLabel("w1:t1"), res.Warnings)
	}
}

func TestAdoptRefusals(t *testing.T) {
	h := session(paneSpec{pane: "w1:p1"}, paneSpec{pane: "w1:p2", kind: "claude"})
	env, _ := testEnv(t, h)
	ctx := context.Background()
	project := t.TempDir()
	for name, req := range map[string]AdoptRequest{
		"no agent":   {MailName: "IcyKnoll", PaneID: "w1:p1", ProjectKey: project, ProjectID: "p"},
		"no pane":    {MailName: "IcyKnoll", PaneID: "w1:p9", ProjectKey: project, ProjectID: "p"},
		"wrong kind": {MailName: "IcyKnoll", PaneID: "w1:p2", Kind: "codex", ProjectKey: project, ProjectID: "p"},
		"bad name":   {MailName: "Not A Name", PaneID: "w1:p2", ProjectKey: project, ProjectID: "p"},
		"pane unset": {MailName: "IcyKnoll", ProjectKey: project, ProjectID: "p"},
	} {
		if _, err := Adopt(ctx, env, req); err == nil {
			t.Errorf("%s: adopt accepted %+v", name, req)
		}
	}
	// A retried operation id never runs twice.
	req := AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p2", ProjectKey: project, ProjectID: "p", OperationID: "op-retry-1"}
	if _, err := Adopt(ctx, env, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(ctx, env, req); err == nil || !strings.Contains(err.Error(), "nothing was done again") {
		t.Fatalf("retry: %v", err)
	}
}

func TestImportOnlyNeedsVerifiedEvidence(t *testing.T) {
	project := t.TempDir()
	real, _ := filepath.EvalSymlinks(project)
	acfs := t.TempDir()
	h := session(
		// Bash-spawned: tab label, alias and token agree.
		paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", tabLabel: "IcyKnoll", cwd: real},
		// Same shape, but no Bash token: a name match only.
		paneSpec{pane: "w1:p2", kind: "codex", alias: "bluelake", tabLabel: "BlueLake", cwd: real},
		// Hand-started, named by Agent Mail's identity file.
		paneSpec{pane: "w1:p3", kind: "claude", tabLabel: "3", cwd: real},
		// Outside any project.
		paneSpec{pane: "w1:p4", kind: "claude", alias: "redstone", tabLabel: "RedStone", cwd: "/"},
		// A shell, no agent.
		paneSpec{pane: "w1:p5", tabLabel: "shell", cwd: real},
	)
	env, home := testEnv(t, h)
	ctx := context.Background()
	token := BashTokenFile(acfs, real, "IcyKnoll")
	if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentmail.WriteIdentity(home, real, "w1:p3", "GreenCastle", t0); err != nil {
		t.Fatal(err)
	}
	projectOf := func(_ context.Context, cwd string) (string, string, error) {
		if cwd == real {
			return real, "proj-x", nil
		}
		return "", "", errors.New("not a git work tree")
	}

	dry, err := ImportOnly(ctx, env, projectOf, ImportRequest{DryRun: true, AcfsHome: acfs})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range dry.Items {
		got[it.PaneID] = it.Action + " " + it.MailName
	}
	want := map[string]string{
		"w1:p1": "would_import IcyKnoll",
		"w1:p2": "skipped ",
		"w1:p3": "would_import GreenCastle",
		"w1:p4": "skipped ",
	}
	for pane, w := range want {
		if got[pane] != w {
			t.Errorf("dry run %s = %q, want %q", pane, got[pane], w)
		}
	}
	if len(dry.Items) != 4 || dry.OperationID != "" {
		t.Errorf("dry run items = %d, op %q", len(dry.Items), dry.OperationID)
	}
	if list, _ := env.Bindings.List(ctx, Filter{}); len(list) != 0 {
		t.Fatalf("the dry run wrote %d bindings", len(list))
	}

	rep, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Imported != 2 || rep.OperationID == "" {
		t.Fatalf("import = %+v", rep)
	}
	b, err := env.Bindings.ActiveByPane(ctx, "w1:p1")
	if err != nil || b.Provenance != Imported || b.MailName != "IcyKnoll" || b.HerdrAlias != "icyknoll" || b.Evidence[0].Kind != "bash_spawn" {
		t.Fatalf("imported p1 = %+v %v", b, err)
	}
	if rec, ok, _ := agentmail.ReadIdentity(home, real, "w1:p1"); !ok || rec.Name != "IcyKnoll" {
		t.Fatal("the import did not write p1's identity file")
	}
	if len(h.renamed) != 0 {
		t.Fatalf("the import renamed agents in herdr: %v", h.renamed)
	}
	// A second run finds them bound and imports nothing.
	again, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs})
	if err != nil || again.Imported != 0 {
		t.Fatalf("second run = %+v %v", again, err)
	}
	for _, it := range again.Items {
		if (it.PaneID == "w1:p1" || it.PaneID == "w1:p3") && it.Action != ActionBound {
			t.Errorf("second run %s = %s", it.PaneID, it.Action)
		}
	}
	// --project keeps to one project.
	only, err := ImportOnly(ctx, env, projectOf, ImportRequest{ProjectKey: "/elsewhere", DryRun: true, AcfsHome: acfs})
	if err != nil || len(only.Items) != 0 {
		t.Fatalf("other project = %+v %v", only, err)
	}
}

func TestImportRefusesDisagreeingEvidence(t *testing.T) {
	project := t.TempDir()
	real, _ := filepath.EvalSymlinks(project)
	acfs := t.TempDir()
	h := session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", tabLabel: "IcyKnoll", cwd: real})
	env, home := testEnv(t, h)
	token := BashTokenFile(acfs, real, "IcyKnoll")
	_ = os.MkdirAll(filepath.Dir(token), 0o700)
	_ = os.WriteFile(token, []byte("secret\n"), 0o600)
	if _, err := agentmail.WriteIdentity(home, real, "w1:p1", "BlueLake", t0); err != nil {
		t.Fatal(err)
	}
	projectOf := func(context.Context, string) (string, string, error) { return real, "proj-x", nil }
	rep, err := ImportOnly(context.Background(), env, projectOf, ImportRequest{DryRun: true, AcfsHome: acfs})
	if err != nil || len(rep.Items) != 1 || rep.Items[0].Action != ActionSkipped || !strings.Contains(rep.Items[0].Reason, "BlueLake") {
		t.Fatalf("disagreeing evidence = %+v %v", rep, err)
	}
}

func TestProcStartParsesProcStat(t *testing.T) {
	start, err := ProcStart(int64(os.Getpid()))
	if err != nil || start <= 0 {
		t.Fatalf("ProcStart(self) = %d %v", start, err)
	}
}
