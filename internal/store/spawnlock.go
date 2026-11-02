package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrSpawnAdmissionBusy means another nhm process held the spawn
// admission lock for the whole wait: ntm's ErrSpawnAdmissionBusy.
var ErrSpawnAdmissionBusy = errors.New("another spawn owns fleet admission")

// SpawnAdmissionWait is how long LockSpawnAdmission waits, ntm's 30 s;
// a variable so tests can wait less.
var SpawnAdmissionWait = 30 * time.Second

// LockSpawnAdmission takes the instance's spawn admission lock: an
// exclusive flock on <instance>/locks/spawn-admission.lock, nhm's place
// for ntm's fleet.lock (AcquireSpawnAdmission, nhm-a421). It waits up to
// SpawnAdmissionWait, then ErrSpawnAdmissionBusy; ctx ends the wait
// sooner with its error. The release is idempotent.
//
// Who takes it: a spawn or add under an agent cap ([spawn_pacing]), from
// before it counts the fleet until its last agent.start, so two spawns
// cannot spend the same headroom. The kernel releases it when the
// process ends.
func LockSpawnAdmission(ctx context.Context, instanceDir string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(instanceDir, LocksDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	path := filepath.Join(dir, "spawn-admission.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	wctx, cancel := context.WithTimeout(ctx, SpawnAdmissionWait)
	defer cancel()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			// A cancel that races the lock must not admit a spawn.
			if cerr := ctx.Err(); cerr != nil {
				_ = f.Close()
				return nil, cerr
			}
			return sync.OnceFunc(func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}), nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("store: lock %s: %w", path, err)
		}
		select {
		case <-wctx.Done():
			_ = f.Close()
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, ErrSpawnAdmissionBusy
		case <-time.After(workspaceLockPoll):
		}
	}
}
