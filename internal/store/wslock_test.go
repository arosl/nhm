package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockWorkspaceExcludes(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockWorkspace(context.Background(), dir, "w1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := LockWorkspace(ctx, dir, "w1"); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("second lock = %v, want ErrWorkspaceBusy", err)
	}
	// Another workspace has its own lock.
	other, err := LockWorkspace(context.Background(), dir, "w2")
	if err != nil {
		t.Fatalf("w2 = %v", err)
	}
	other()

	var got atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		u, err := LockWorkspace(context.Background(), dir, "w1")
		if err == nil {
			got.Store(true)
			u()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if got.Load() {
		t.Fatal("the waiter took a held lock")
	}
	unlock()
	<-done
	if !got.Load() {
		t.Fatal("the waiter never took the released lock")
	}
	st, err := os.Stat(filepath.Join(dir, LocksDir, "workspace-w1.lock"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("lock file = %v, %v", st, err)
	}
}

func TestLockWorkspaceRefusesBadIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "../w1", "w/1", "w 1", "w1\x00"} {
		if _, err := LockWorkspace(context.Background(), dir, id); err == nil {
			t.Errorf("LockWorkspace(%q) took a lock", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, LocksDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused id made the locks directory: %v", err)
	}
}
