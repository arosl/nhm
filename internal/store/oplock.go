package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrOperationBusy means another nhm process holds the operation's lock:
// it still runs the operation.
var ErrOperationBusy = errors.New("store: another nhm process runs this operation")

// LockOperation takes an operation's run lock without a wait: an
// exclusive flock on <instance>/locks/operation-<id>.lock, or
// ErrOperationBusy. The kernel releases it when the process ends, so a
// rerun that takes it knows that the first run is over, ended or killed
// (nhm-vcf).
//
// Who takes it: spawn --assign with --op-id, from before the spawn until
// its last send, around the owner lock (it never waits, so it cannot
// deadlock with it).
func LockOperation(instanceDir, opID string) (func(), error) {
	if !operationIDPattern.MatchString(opID) {
		return nil, fmt.Errorf("store: operation id %q cannot name a lock file", opID)
	}
	dir := filepath.Join(instanceDir, LocksDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	path := filepath.Join(dir, "operation-"+opID+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			_ = f.Close()
			return nil, fmt.Errorf("%w (%s)", ErrOperationBusy, opID)
		}
		_ = f.Close()
		return nil, fmt.Errorf("store: lock %s: %w", path, err)
	}
}
