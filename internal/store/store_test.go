package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

func enroll(t *testing.T) Instance {
	t.Helper()
	inst, created, err := Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", t0)
	if err != nil || !created {
		t.Fatalf("Enroll = %+v, %v, %v", inst, created, err)
	}
	return inst
}

func TestStateRoot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x/state")
	if got, err := StateRoot(); err != nil || got != "/x/state/nhm" {
		t.Fatalf("StateRoot = %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/x/home")
	if got, err := StateRoot(); err != nil || got != "/x/home/.local/state/nhm" {
		t.Fatalf("StateRoot = %q, %v", got, err)
	}
}

func TestEnrollIsIdempotentPerSocket(t *testing.T) {
	root := t.TempDir()
	a, created, err := Enroll(root, "/run/herdr/herdr.sock", "default", t0)
	if err != nil || !created {
		t.Fatalf("first Enroll: %v %v", created, err)
	}
	b, created, err := Enroll(root, "/run/herdr/../herdr/herdr.sock", "renamed", t0.Add(time.Hour))
	if err != nil || created || b.ID != a.ID || b.Session != "renamed" || !b.EnrolledAt.Equal(t0) {
		t.Fatalf("second Enroll = %+v, %v, %v", b, created, err)
	}
	c, created, err := Enroll(root, "/run/other/herdr.sock", "default", t0)
	if err != nil || !created || c.ID == a.ID {
		t.Fatalf("other socket = %+v, %v, %v", c, created, err)
	}
	got, err := Lookup(root, "/run/herdr/herdr.sock")
	if err != nil || got.ID != a.ID || got.Dir != filepath.Join(root, a.ID) {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
	if _, err := Lookup(root, "/nowhere.sock"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("Lookup of an unknown socket = %v", err)
	}
	if _, err := Lookup(t.TempDir()+"/missing", "/run/herdr/herdr.sock"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("Lookup in a missing root = %v", err)
	}
	list, err := List(root)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestEnrollLayoutAndModes(t *testing.T) {
	inst := enroll(t)
	for _, p := range []string{"", SecretsDir, TranscriptsDir, RunsDir} {
		info, err := os.Stat(filepath.Join(inst.Dir, p))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%q: %v, %v", p, info, err)
		}
	}
	for _, f := range []string{InstanceFile, DatabaseFile} {
		if _, err := os.Stat(filepath.Join(inst.Dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	db, err := Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if id, err := db.Meta(context.Background(), "instance_id"); err != nil || id != inst.ID {
		t.Fatalf("meta instance_id = %q, %v", id, err)
	}
	if v, err := db.UserVersion(context.Background()); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d, %v", v, err)
	}
}

func TestEnrollTightensAWideStateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nhm")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(root, "/run/herdr/herdr.sock", "default", t0); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state root = %v %v", info, err)
	}
}

// chmod follows a symlink, so a symlinked state root is refused and the
// link's target keeps its mode.
func TestEnrollRefusesASymlinkedStateRoot(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "nhm")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Enroll(root, "/run/herdr/herdr.sock", "default", t0); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("Enroll = %v", err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("the link's target changed: %v %v", info, err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("Enroll wrote through the link: %v", entries)
	}
}

func TestConcurrentEnrollMakesOneInstance(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inst, _, err := Enroll(root, "/run/herdr/herdr.sock", "default", t0)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = inst.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids differ: %v", ids)
		}
	}
}

func TestDirectoryWithoutInstanceFileIsNotEnrolled(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inst-000000000000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if list, err := List(root); err != nil || len(list) != 0 {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	inst := enroll(t)
	db, err := Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(inst.Dir); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open of a newer schema = %v", err)
	}
}

func TestOpenForHookNeverMigratesAndWaitsBriefly(t *testing.T) {
	inst := enroll(t)
	ctx := context.Background()
	db, err := Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	// Another writer holds the write lock.
	conn, err := db.SQL().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	hook, err := OpenForHook(ctx, inst.Dir, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("OpenForHook under a writer: %v", err)
	}
	start := time.Now()
	_, err = hook.SQL().ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('k', 'v')`)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("hook write under a writer = %v after %s", err, time.Since(start))
	}
	_ = hook.Close()
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	_ = conn.Close()
	// An older schema is left for the next nhm command.
	if _, err := db.SQL().Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := OpenForHook(ctx, inst.Dir, 100*time.Millisecond); !errors.Is(err, ErrSchemaOld) {
		t.Fatalf("OpenForHook of an older schema = %v", err)
	}
}

func TestBindingUniqueness(t *testing.T) {
	inst := enroll(t)
	db, err := Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insert := func(id, status, name, pane string) error {
		_, err := db.SQL().Exec(`INSERT INTO bindings (binding_id, generation, status, mail_name,
			mail_project_key, project_id, pane_id, kind, identity_provenance, created_at, updated_at)
			VALUES (?, 1, ?, ?, '/p', 'proj', ?, 'claude', 'created', 'now', 'now')`, id, status, name, pane)
		return err
	}
	if err := insert("b1", "active", "IcyKnoll", "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if err := insert("b2", "active", "IcyKnoll", "w1:p2"); err == nil {
		t.Error("two active bindings for one identity")
	}
	if err := insert("b3", "active", "BoldElm", "w1:p1"); err == nil {
		t.Error("two active bindings for one pane")
	}
	if err := insert("b4", "displaced", "IcyKnoll", "w1:p1"); err != nil {
		t.Errorf("a displaced binding was refused: %v", err)
	}
	if err := insert("b5", "lost", "GreenPike", "w1:p3"); err == nil {
		t.Error("an unknown status was accepted")
	}
}

func TestClaimOperation(t *testing.T) {
	inst := enroll(t)
	db, err := Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	req := ClaimRequest{ID: "op-1", Verb: "send", Scope: map[string]string{"workspace_id": "wH"}, Request: map[string]any{"text": "hi", "targets": []string{"b1"}}}

	first, err := db.ClaimOperation(ctx, req, t0)
	if err != nil || first.Existing || first.Operation.Status != OperationClaimed {
		t.Fatalf("first claim = %+v, %v", first, err)
	}
	if err := db.SetOperationStatus(ctx, "op-1", "submitted", true, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	again, err := db.ClaimOperation(ctx, req, t0.Add(time.Minute))
	if err != nil || !again.Existing || again.Operation.Status != "submitted" || again.Operation.CompletedAt == nil {
		t.Fatalf("second claim = %+v, %v", again, err)
	}
	other := req
	other.Request = map[string]any{"text": "bye"}
	if _, err := db.ClaimOperation(ctx, other, t0); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("different request = %v", err)
	}
	other = req
	other.Verb = "spawn"
	if _, err := db.ClaimOperation(ctx, other, t0); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("different verb = %v", err)
	}
	fresh, err := db.ClaimOperation(ctx, ClaimRequest{Verb: "send", Request: 1}, t0)
	if err != nil || fresh.Existing || fresh.Operation.ID == "" {
		t.Fatalf("generated id claim = %+v, %v", fresh, err)
	}
	if _, err := db.ClaimOperation(ctx, ClaimRequest{ID: "../x", Verb: "send"}, t0); err == nil {
		t.Fatal("an invalid id was accepted")
	}
	if err := db.SetOperationStatus(ctx, "op-missing", "x", false, t0); err == nil {
		t.Fatal("status of a missing operation was set")
	}
}

// Two processes (here: two connections) racing for one id: exactly one
// executes.
func TestConcurrentClaimsExecuteOnce(t *testing.T) {
	inst := enroll(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	executed := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(inst.Dir)
			if err != nil {
				t.Error(err)
				return
			}
			defer db.Close()
			c, err := db.ClaimOperation(context.Background(), ClaimRequest{ID: "op-race", Verb: "send", Request: "x"}, t0)
			if err != nil {
				t.Error(err)
				return
			}
			if !c.Existing {
				mu.Lock()
				executed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if executed != 1 {
		t.Fatalf("executed = %d, want 1", executed)
	}
}

func TestSecrets(t *testing.T) {
	inst := enroll(t)
	if err := WriteSecret(inst.Dir, "b1", "tok-123"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(inst.Dir, SecretsDir, "b1"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %v, %v", info, err)
	}
	if got, err := ReadSecret(inst.Dir, "b1"); err != nil || got != "tok-123" {
		t.Fatalf("ReadSecret = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a/b", "x y"} {
		if err := WriteSecret(inst.Dir, bad, "t"); err == nil {
			t.Errorf("binding id %q accepted", bad)
		}
	}
	if err := WriteSecret(inst.Dir, "b2", "two\nlines"); err == nil {
		t.Error("a two-line token was accepted")
	}
	if err := DeleteSecret(inst.Dir, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecret(inst.Dir, "b1"); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("ReadSecret after delete = %v", err)
	}
	if err := DeleteSecret(inst.Dir, "b1"); err != nil {
		t.Fatalf("second delete = %v", err)
	}
}
