package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// LocksDir is the instance directory's lock files: one per binding
// (send's target lock) and one per workspace.
const LocksDir = "locks"

// ErrWorkspaceBusy means another nhm process holds the workspace lock
// and the wait ended.
var ErrWorkspaceBusy = errors.New("store: another nhm process holds the workspace lock")

// workspaceIDPattern is what a workspace id may hold to name a file.
// herdr's ids are short ("w1", "wQ").
var workspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidWorkspaceID reports whether id can name a lock or state file.
func ValidWorkspaceID(id string) bool { return workspaceIDPattern.MatchString(id) }

// workspaceLockPoll is how often a waiting workspace lock tries again.
const workspaceLockPoll = 20 * time.Millisecond

// LockWorkspace takes a workspace's lock: an exclusive flock on
// <instance>/locks/workspace-<id>.lock. It orders nhm's own writers of
// the workspace's UI state (the board's launch record); it is not the
// owner lock and does not exclude herdr or a human. ctx bounds the wait;
// ErrWorkspaceBusy when it ends.
func LockWorkspace(ctx context.Context, instanceDir, workspaceID string) (func(), error) {
	if !ValidWorkspaceID(workspaceID) {
		return nil, fmt.Errorf("store: workspace id %q cannot name a lock file", workspaceID)
	}
	dir := filepath.Join(instanceDir, LocksDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	path := filepath.Join(dir, "workspace-"+workspaceID+".lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("store: lock %s: %w", path, err)
		}
		timer := time.NewTimer(workspaceLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, fmt.Errorf("%w (%s)", ErrWorkspaceBusy, workspaceID)
		case <-timer.C:
		}
	}
}
