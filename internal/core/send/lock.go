package send

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrTargetBusy means another nhm writer holds the target's lock.
var ErrTargetBusy = errors.New("another nhm writer holds this agent")

// lockPoll is how often a waiting lock tries again.
const lockPoll = 50 * time.Millisecond

// lockTarget takes the target's mutation lock: an exclusive flock on
// <instance>/locks/<binding_id>.lock. It coordinates nhm's own writers
// only; a human typing or a direct herdr prompt is not excluded.
func lockTarget(ctx context.Context, instanceDir, bindingID string) (func(), error) {
	dir := filepath.Join(instanceDir, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("send: locks: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, bindingID+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("send: lock %s: %w", bindingID, err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("send: lock %s: %w", bindingID, err)
		}
		timer := time.NewTimer(lockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, fmt.Errorf("%w (%s)", ErrTargetBusy, bindingID)
		case <-timer.C:
		}
	}
}
