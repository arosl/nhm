package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// slowHerdr counts herdr's session snapshots and can hold back the
// snapshot and the process query, to place the hook's deadline.
type slowHerdr struct {
	Herdr
	mu            sync.Mutex
	snapshots     int
	snapshotDelay time.Duration
	processDelay  time.Duration
}

func (s *slowHerdr) Snapshot(ctx context.Context) (herdr.SessionSnapshot, error) {
	if err := sleepCtx(ctx, s.countSnapshot()); err != nil {
		return herdr.SessionSnapshot{}, err
	}
	return s.Herdr.Snapshot(ctx)
}

func (s *slowHerdr) PaneProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error) {
	if err := sleepCtx(ctx, s.processWait()); err != nil {
		return herdr.ProcessInfo{}, err
	}
	return s.Herdr.PaneProcessInfo(ctx, paneID)
}

// countSnapshot counts a snapshot and returns its delay.
func (s *slowHerdr) countSnapshot() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots++
	return s.snapshotDelay
}

func (s *slowHerdr) processWait() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processDelay
}

func (s *slowHerdr) setProcessDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processDelay = d
}

func (s *slowHerdr) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshots
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bashHarness is a harness under the Bash owner, with a project and an
// ACFS home, and herdr wrapped in a slowHerdr.
func bashHarness(t *testing.T) (*harness, *slowHerdr, string) {
	t.Helper()
	h := newHarness(t, core.OwnerBash)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo = canonical(repo)
	h.env.AcfsHome = t.TempDir()
	slow := &slowHerdr{Herdr: h.env.Herdr}
	h.env.Herdr = slow
	return h, slow, repo
}

func operationStatuses(t *testing.T, db *store.DB) map[string]int {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT status FROM operations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got[s]++
	}
	return got
}

// A hook for a pane without evidence costs no session snapshot; a pane
// whose herdr name has a Bash token gets the full check.
func TestHookWithoutEvidenceTakesNoSnapshot(t *testing.T) {
	h, slow, repo := bashHarness(t)
	ctx := context.Background()
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500, cwd: repo})
	if _, err := RunPluginEvent(ctx, h.env, PaneEvent{PaneID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	if n := slow.count(); n != 0 {
		t.Fatalf("snapshots for a pane without evidence = %d", n)
	}

	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500, cwd: repo, name: "icyknoll"})
	token := bindings.BashTokenFile(h.env.AcfsHome, repo, "IcyKnoll")
	if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunPluginEvent(ctx, h.env, PaneEvent{PaneID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	// The stub's tab is labelled "1", so the full check refuses the
	// Bash evidence after its dry run: one snapshot, no operation.
	if n := slow.count(); n != 1 {
		t.Fatalf("snapshots for a pane with a Bash token = %d", n)
	}
	if got := operationStatuses(t, h.env.DB); len(got) != 0 {
		t.Fatalf("operations = %v", got)
	}
}

// Two hooks import the same pane at once, each with its own database
// handle as two nhm-hook processes have: one binding, and no operation
// left 'claimed'.
func TestTwoHooksImportTheSamePaneAtOnce(t *testing.T) {
	h, slow, repo := bashHarness(t)
	// Both dry runs see the pane unbound before either imports it.
	slow.snapshotDelay = 50 * time.Millisecond
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500, cwd: repo, name: "icyknoll"})
	if _, err := agentmail.WriteIdentity(h.env.ConfigHome, repo, "w1:p1", "IcyKnoll", t0); err != nil {
		t.Fatal(err)
	}
	db2, err := store.Open(h.env.InstanceDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	env2 := h.env
	env2.DB, env2.Bindings = db2, bindings.NewStore(db2)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	reps := make([]Reconcile, 2)
	start := make(chan struct{})
	for i, env := range []Env{h.env, env2} {
		wg.Add(1)
		go func(i int, env Env) {
			defer wg.Done()
			<-start
			reps[i], errs[i] = RunPluginEvent(context.Background(), env, PaneEvent{PaneID: "w1:p1"})
		}(i, env)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("hook %d: %v (%+v)", i, err, reps[i])
		}
	}
	all, err := h.env.Bindings.List(context.Background(), bindings.Filter{ProjectKey: repo})
	if err != nil || len(all) != 1 || all[0].MailName != "IcyKnoll" || all[0].Status != bindings.Active {
		t.Fatalf("bindings = %+v %v (reports %+v)", all, err, reps)
	}
	if got := operationStatuses(t, h.env.DB); got["claimed"] != 0 || got["done"] == 0 {
		t.Fatalf("operations = %v", got)
	}
}

// The hook's deadline passes after the import claimed its operation: the
// hook returns within its budget, the operation is 'partial', not
// 'claimed', and the next hook imports the pane.
func TestHookBudgetWithImport(t *testing.T) {
	h, slow, repo := bashHarness(t)
	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500, cwd: repo, name: "icyknoll"})
	if _, err := agentmail.WriteIdentity(h.env.ConfigHome, repo, "w1:p1", "IcyKnoll", t0); err != nil {
		t.Fatal(err)
	}
	slow.processDelay = 10 * time.Second
	budget := 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	began := time.Now()
	if _, err := RunPluginEvent(ctx, h.env, PaneEvent{PaneID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > budget+time.Second {
		t.Fatalf("the hook took %v with a budget of %v", took, budget)
	}
	if got := operationStatuses(t, h.env.DB); got["claimed"] != 0 || got["partial"] != 1 {
		t.Fatalf("operations = %v", got)
	}
	if _, err := h.env.Bindings.ActiveByPane(context.Background(), "w1:p1"); !errors.Is(err, bindings.ErrNotFound) {
		t.Fatalf("a binding after the deadline: %v", err)
	}

	slow.setProcessDelay(0)
	if _, err := RunPluginEvent(context.Background(), h.env, PaneEvent{PaneID: "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	if b, err := h.env.Bindings.ActiveByPane(context.Background(), "w1:p1"); err != nil || b.MailName != "IcyKnoll" {
		t.Fatalf("the next hook did not import: %+v %v", b, err)
	}
}
