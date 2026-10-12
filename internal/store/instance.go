package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// ErrNotEnrolled means no instance directory names this herdr socket.
// Hooks and plugin actions from such a server exit 0 and do nothing.
var ErrNotEnrolled = errors.New("store: herdr instance is not enrolled")

// Instance is the content of instance.toml.
type Instance struct {
	ID         string    `toml:"id"`
	Session    string    `toml:"session"`
	SocketPath string    `toml:"socket_path"`
	EnrolledAt time.Time `toml:"enrolled_at"`

	// Dir is the instance directory; it is not stored.
	Dir string `toml:"-"`
}

var instanceIDPattern = regexp.MustCompile(`^inst-[0-9a-f]{12}$`)

// Enroll returns the instance enrolled for socketPath, or enrolls one: it
// creates the instance directory (0700) with its subdirectories,
// instance.toml and the database. created says which. Enrolling the same
// socket again returns the existing instance and refreshes its session.
func Enroll(root, socketPath, session string, now time.Time) (inst Instance, created bool, err error) {
	socketPath, err = cleanSocketPath(socketPath)
	if err != nil {
		return Instance{}, false, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Instance{}, false, fmt.Errorf("store: %w", err)
	}
	// MkdirAll keeps an existing directory's mode; the state root holds
	// every instance, so it is 0700 whatever made it.
	if err := os.Chmod(root, 0o700); err != nil {
		return Instance{}, false, fmt.Errorf("store: %w", err)
	}
	unlock, err := lockFile(filepath.Join(root, ".enroll.lock"))
	if err != nil {
		return Instance{}, false, err
	}
	defer unlock()

	existing, err := Lookup(root, socketPath)
	switch {
	case err == nil:
		if existing.Session != session {
			existing.Session = session
			if err := writeInstance(existing); err != nil {
				return Instance{}, false, err
			}
		}
		return existing, false, nil
	case !errors.Is(err, ErrNotEnrolled):
		return Instance{}, false, err
	}

	id, err := newInstanceID()
	if err != nil {
		return Instance{}, false, err
	}
	inst = Instance{ID: id, Session: session, SocketPath: socketPath, EnrolledAt: now.UTC(), Dir: filepath.Join(root, id)}
	if err := os.Mkdir(inst.Dir, 0o700); err != nil {
		return Instance{}, false, fmt.Errorf("store: %w", err)
	}
	for _, sub := range []string{SecretsDir, TranscriptsDir, RunsDir} {
		if err := os.Mkdir(filepath.Join(inst.Dir, sub), 0o700); err != nil {
			return Instance{}, false, fmt.Errorf("store: %w", err)
		}
	}
	db, err := Open(inst.Dir)
	if err != nil {
		return Instance{}, false, err
	}
	if err := db.setMeta("instance_id", inst.ID); err != nil {
		_ = db.Close()
		return Instance{}, false, err
	}
	if err := db.Close(); err != nil {
		return Instance{}, false, err
	}
	// instance.toml comes last: an instance without it is not enrolled.
	if err := writeInstance(inst); err != nil {
		return Instance{}, false, err
	}
	return inst, true, nil
}

// Lookup finds the instance enrolled for socketPath.
func Lookup(root, socketPath string) (Instance, error) {
	socketPath, err := cleanSocketPath(socketPath)
	if err != nil {
		return Instance{}, err
	}
	instances, err := List(root)
	if err != nil {
		return Instance{}, err
	}
	for _, inst := range instances {
		if inst.SocketPath == socketPath {
			return inst, nil
		}
	}
	return Instance{}, fmt.Errorf("%w: %s", ErrNotEnrolled, socketPath)
}

// List returns every enrolled instance under root. A directory with a
// missing or unreadable instance.toml is skipped.
func List(root string) ([]Instance, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: %w", err)
	}
	var out []Instance
	for _, e := range entries {
		if !e.IsDir() || !instanceIDPattern.MatchString(e.Name()) {
			continue
		}
		inst, err := ReadInstance(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// ReadInstance reads instance.toml from an instance directory.
func ReadInstance(dir string) (Instance, error) {
	var inst Instance
	if _, err := toml.DecodeFile(filepath.Join(dir, InstanceFile), &inst); err != nil {
		return Instance{}, fmt.Errorf("store: %s: %w", filepath.Join(dir, InstanceFile), err)
	}
	if inst.ID != filepath.Base(dir) || !instanceIDPattern.MatchString(inst.ID) {
		return Instance{}, fmt.Errorf("store: %s: id %q does not match its directory", dir, inst.ID)
	}
	inst.Dir = dir
	return inst, nil
}

func writeInstance(inst Instance) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(inst); err != nil {
		return fmt.Errorf("store: encode instance: %w", err)
	}
	return WriteFileAtomic(filepath.Join(inst.Dir, InstanceFile), buf.Bytes(), 0o600)
}

func cleanSocketPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("store: empty herdr socket path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	return abs, nil
}

func newInstanceID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: instance id: %w", err)
	}
	return "inst-" + hex.EncodeToString(b), nil
}

// lockFile takes an exclusive flock on path, creating it, and returns the
// release function.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store: lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
