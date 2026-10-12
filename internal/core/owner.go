package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/store"
)

// The owner switch (plan-v8 section 1, F.24): one owner per herdr
// instance. owner.toml in the instance directory names it:
//
//	owner = "nhm"        # or "acfs-bash"
//	generation = 3       # takeover and handback increment it
//	updated_at = 2026-10-12T04:00:00Z
//
// A missing file means acfs-bash, generation 0, during the migration.
// A corrupt file refuses every mutation.
//
// The lock is owner.lock beside it, a flock(2) lock. Every mutator, in
// nhm and in ACFS's Bash, takes it shared, reads the owner, and holds it
// through its bounded side-effect phase:
//
//	exec {fd}>"$dir/owner.lock"; flock -s -w 10 "$fd"   # Bash
//
// A takeover or handback takes it exclusive, so it waits for those holders
// and no mutator starts while it switches. owner.toml is replaced by an
// atomic rename; the lock is on the separate file because a rename would
// leave a lock on the old inode.

// Owners.
const (
	OwnerBash = "acfs-bash"
	OwnerNhm  = "nhm"
)

// Owner errors.
var (
	ErrOwnerCorrupt     = errors.New("owner.toml is corrupt; every mutation is refused")
	ErrNotOwner         = errors.New("this herdr instance is owned by another implementation")
	ErrOwnerLockTimeout = errors.New("timed out waiting for the owner lock")
)

// Owner is the content of owner.toml.
type Owner struct {
	Owner      string    `toml:"owner"`
	Generation int64     `toml:"generation"`
	UpdatedAt  time.Time `toml:"updated_at"`
}

// ReadOwner reads owner.toml in an instance directory.
func ReadOwner(instanceDir string) (Owner, error) {
	data, err := os.ReadFile(filepath.Join(instanceDir, store.OwnerFile))
	if os.IsNotExist(err) {
		return Owner{Owner: OwnerBash}, nil
	}
	if err != nil {
		return Owner{}, fmt.Errorf("core: %w", err)
	}
	var o Owner
	md, err := toml.Decode(string(data), &o)
	if err != nil {
		return Owner{}, fmt.Errorf("%w: %v", ErrOwnerCorrupt, err)
	}
	if len(md.Undecoded()) > 0 || !md.IsDefined("owner") || !md.IsDefined("generation") {
		return Owner{}, fmt.Errorf("%w: unknown or missing keys", ErrOwnerCorrupt)
	}
	if (o.Owner != OwnerBash && o.Owner != OwnerNhm) || o.Generation < 0 {
		return Owner{}, fmt.Errorf("%w: owner %q, generation %d", ErrOwnerCorrupt, o.Owner, o.Generation)
	}
	return o, nil
}

// WithOwnerShared runs fn under the shared owner lock with the current
// owner. fn is a mutator's bounded side-effect phase; it must check the
// owner (RequireOwner) and must not wait long. ctx bounds the wait for the
// lock.
func WithOwnerShared(ctx context.Context, instanceDir string, fn func(Owner) error) error {
	unlock, err := lockOwner(ctx, instanceDir, syscall.LOCK_SH)
	if err != nil {
		return err
	}
	defer unlock()
	o, err := ReadOwner(instanceDir)
	if err != nil {
		return err
	}
	return fn(o)
}

// RequireOwner refuses unless o names want.
func RequireOwner(o Owner, want string) error {
	if o.Owner != want {
		return fmt.Errorf("%w: owner is %s (generation %d), not %s", ErrNotOwner, o.Owner, o.Generation, want)
	}
	return nil
}

// SwitchOwner moves the instance to a new owner under the exclusive lock:
// takeover (to nhm) or handback (to acfs-bash). prepare runs first, under
// the lock, with the current owner; it imports or exports the state and
// validates it. The switch happens only when prepare succeeds. It refuses
// a corrupt owner.toml and a switch to the current owner.
func SwitchOwner(ctx context.Context, instanceDir, to string, now time.Time, prepare func(current Owner) error) (Owner, error) {
	if to != OwnerBash && to != OwnerNhm {
		return Owner{}, fmt.Errorf("core: unknown owner %q", to)
	}
	unlock, err := lockOwner(ctx, instanceDir, syscall.LOCK_EX)
	if err != nil {
		return Owner{}, err
	}
	defer unlock()
	current, err := ReadOwner(instanceDir)
	if err != nil {
		return Owner{}, err
	}
	if current.Owner == to {
		return current, fmt.Errorf("core: the owner is already %s (generation %d)", to, current.Generation)
	}
	if prepare != nil {
		if err := prepare(current); err != nil {
			return current, fmt.Errorf("core: switch to %s refused: %w", to, err)
		}
	}
	next := Owner{Owner: to, Generation: current.Generation + 1, UpdatedAt: now.UTC().Truncate(time.Second)}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(next); err != nil {
		return current, fmt.Errorf("core: encode owner: %w", err)
	}
	if err := store.WriteFileAtomic(filepath.Join(instanceDir, store.OwnerFile), buf.Bytes(), 0o600); err != nil {
		return current, err
	}
	return next, nil
}

// lockOwner takes owner.lock with how (LOCK_SH or LOCK_EX), polling until
// ctx ends.
func lockOwner(ctx context.Context, instanceDir string, how int) (func(), error) {
	path := filepath.Join(instanceDir, store.OwnerLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("core: %w", err)
	}
	delay := 5 * time.Millisecond
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("core: lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s", ErrOwnerLockTimeout, path)
		case <-time.After(delay):
		}
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}
