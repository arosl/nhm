package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The spawn admission lock excludes a second holder until the first
// releases it; a wait that ends is ErrSpawnAdmissionBusy, a cancelled
// context its own error.
func TestLockSpawnAdmissionExcludes(t *testing.T) {
	dir := t.TempDir()
	release, err := LockSpawnAdmission(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := LockSpawnAdmission(ctx, dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second holder = %v, want the context's deadline", err)
	}
	defer func(w time.Duration) { SpawnAdmissionWait = w }(SpawnAdmissionWait)
	SpawnAdmissionWait = 50 * time.Millisecond
	if _, err := LockSpawnAdmission(context.Background(), dir); !errors.Is(err, ErrSpawnAdmissionBusy) {
		t.Fatalf("busy = %v, want ErrSpawnAdmissionBusy", err)
	}
	gone, stop := context.WithCancel(context.Background())
	stop()
	if _, err := LockSpawnAdmission(gone, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
	release()
	release() // idempotent
	again, err := LockSpawnAdmission(context.Background(), dir)
	if err != nil {
		t.Fatalf("after release = %v", err)
	}
	again()
}
