package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// An operation's run lock never waits: a second taker gets
// ErrOperationBusy at once, another operation has its own lock, and the
// released lock can be taken again.
func TestLockOperationExcludes(t *testing.T) {
	dir := t.TempDir()
	unlock, err := LockOperation(dir, "sp-a1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockOperation(dir, "sp-a1"); !errors.Is(err, ErrOperationBusy) {
		t.Fatalf("second lock = %v, want ErrOperationBusy", err)
	}
	other, err := LockOperation(dir, "op:2.b")
	if err != nil {
		t.Fatalf("another operation = %v", err)
	}
	other()
	unlock()
	again, err := LockOperation(dir, "sp-a1")
	if err != nil {
		t.Fatalf("after the release = %v", err)
	}
	again()
	st, err := os.Stat(filepath.Join(dir, LocksDir, "operation-sp-a1.lock"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("lock file = %v, %v", st, err)
	}
}

func TestLockOperationRefusesBadIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "../op", ".op", "op/1", "op 1"} {
		if _, err := LockOperation(dir, id); err == nil {
			t.Errorf("LockOperation(%q) took a lock", id)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, LocksDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused id made the locks directory: %v", err)
	}
}
