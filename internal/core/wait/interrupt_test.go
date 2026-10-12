package wait

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

func openDB(t *testing.T) (*store.DB, *bindings.Store) {
	t.Helper()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, bindings.NewStore(db)
}

func create(t *testing.T, s *bindings.Store, name, tab, pane, kind string) bindings.Binding {
	t.Helper()
	b, err := s.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: tab, PaneID: pane,
			HerdrAlias: lower(name), Kind: kind}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// kindHerdr answers every pane with an agent of the pane's kind.
type kindHerdr struct {
	fakeHerdr
	kinds map[string]string
}

func (k *kindHerdr) AgentGet(_ context.Context, pane string) (herdr.AgentInfo, error) {
	return herdr.AgentInfo{PaneID: pane, Agent: k.kinds[pane], AgentStatus: herdr.StatusWorking, StateChangeSeq: u64(4)}, nil
}

func TestInterruptSendsTheKindsKeyOncePerOperation(t *testing.T) {
	db, s := openDB(t)
	ctx := context.Background()
	icy := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	blue := create(t, s, "BlueLake", "w1:t2", "w1:p2", "agy")
	red := create(t, s, "RedStone", "w1:t3", "w1:p3", "codex")
	h := &kindHerdr{kinds: map[string]string{"w1:p1": "claude", "w1:p2": "agy", "w1:p3": "claude"}}
	req := InterruptRequest{OperationID: "op-int-1", Targets: []bindings.Binding{icy, blue, red}}
	res, err := Interrupt(ctx, db, h, s, req, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{InterruptSent, InterruptRefused, InterruptRefused}
	for i, tg := range res.Targets {
		if tg.Status != want[i] {
			t.Errorf("%s = %s (%s), want %s", tg.MailName, tg.Status, tg.Reason, want[i])
		}
	}
	if res.Status != "partial" || len(h.keys) != 1 || h.keys[0][0] != "esc" || res.Targets[0].StateBefore != herdr.StatusWorking {
		t.Fatalf("status %s, keys %v", res.Status, h.keys)
	}
	// The same operation id sends nothing again.
	res, err = Interrupt(ctx, db, h, s, req, time.Now)
	if err != nil || !res.Existing || res.Status != "partial" || len(h.keys) != 1 {
		t.Fatalf("repeat = %+v %v, keys %v", res, err, h.keys)
	}
	// The same id with other targets is a conflict.
	req.Targets = req.Targets[:1]
	if _, err := Interrupt(ctx, db, h, s, req, time.Now); !errors.Is(err, store.ErrOperationIDConflict) {
		t.Fatalf("conflict = %v", err)
	}
}

func TestInterruptRefusesAChangedBinding(t *testing.T) {
	db, s := openDB(t)
	ctx := context.Background()
	icy := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	if _, err := s.Displace(ctx, icy.ID, icy.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	h := &kindHerdr{kinds: map[string]string{"w1:p1": "claude"}}
	res, err := Interrupt(ctx, db, h, s, InterruptRequest{Targets: []bindings.Binding{icy}}, time.Now)
	if err != nil || res.Status != "refused" || len(h.keys) != 0 {
		t.Fatalf("= %+v %v", res, err)
	}
	op, err := db.Operation(ctx, res.OperationID)
	if err != nil || op.Status != "refused" || op.CompletedAt == nil {
		t.Fatalf("operation = %+v %v", op, err)
	}
}

func TestSelect(t *testing.T) {
	_, s := openDB(t)
	ctx := context.Background()
	create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	create(t, s, "BlueLake", "w1:t2", "w1:p2", "codex")
	create(t, s, "RedStone", "w1:t1", "w1:p3", "claude")
	names := func(list []bindings.Binding) []string {
		var out []string
		for _, b := range list {
			out = append(out, b.MailName+"@"+b.Slot)
		}
		return out
	}
	all, err := Select(ctx, s, "w1", "", Selector{})
	if err != nil || len(all) != 3 || names(all)[0] != "IcyKnoll@0.1" || names(all)[1] != "RedStone@0.2" || names(all)[2] != "BlueLake@1.1" {
		t.Fatalf("all = %v %v", names(all), err)
	}
	got, err := Select(ctx, s, "w1", "", Selector{Kinds: []string{"claude"}, Exclude: "w1:p1"})
	if err != nil || len(got) != 1 || got[0].MailName != "RedStone" {
		t.Fatalf("claude without the caller = %v %v", names(got), err)
	}
	// A named caller stays in.
	got, err = Select(ctx, s, "w1", "", Selector{Names: []string{"icyknoll"}, Exclude: "w1:p1"})
	if err != nil || len(got) != 1 {
		t.Fatalf("named caller = %v %v", names(got), err)
	}
	if got, err = Select(ctx, s, "w1", "", Selector{Slots: []string{"1.1"}}); err != nil || got[0].MailName != "BlueLake" {
		t.Fatalf("slot = %v %v", names(got), err)
	}
	if _, err := Select(ctx, s, "w1", "", Selector{Names: []string{"Nobody"}}); !errors.Is(err, ErrNoTargets) {
		t.Fatalf("unknown name = %v", err)
	}
	if _, err := Select(ctx, s, "w2", "", Selector{}); !errors.Is(err, ErrNoTargets) {
		t.Fatalf("empty workspace = %v", err)
	}
}

func TestReceipts(t *testing.T) {
	db, s := openDB(t)
	ctx := context.Background()
	icy := create(t, s, "IcyKnoll", "w1:t1", "w1:p1", "claude")
	if _, err := Receipts(ctx, db.SQL(), "op-none"); !errors.Is(err, ErrNoReceipts) {
		t.Fatalf("no receipts = %v", err)
	}
	if _, err := db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-send-1", Verb: "send", Request: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ts := store.FormatTime(time.Now())
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation, status, baseline_json, updated_at)
		VALUES ('op-send-1', 0, ?, 1, 'submitted', '{"binding_id":"`+icy.ID+`","generation":1,"pane_id":"w1:p1","state":"idle","state_change_seq":9,"already_working":false,"at":"2026-10-12T05:00:00Z"}', ?),
		('op-send-1', 1, NULL, NULL, 'refused', NULL, ?)`, icy.ID, ts, ts); err != nil {
		t.Fatal(err)
	}
	rs, err := Receipts(ctx, db.SQL(), "op-send-1")
	if err != nil || len(rs) != 2 {
		t.Fatalf("= %+v %v", rs, err)
	}
	if rs[0].Baseline == nil || rs[0].Baseline.StateChangeSeq != 9 || rs[0].Status != Submitted || rs[1].Baseline != nil {
		t.Fatalf("= %+v / %+v", rs[0], rs[1])
	}
}
