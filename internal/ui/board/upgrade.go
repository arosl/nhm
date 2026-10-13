package board

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUpgraded means the nhm binary changed on disk after the board
// started: the board takes no new mutations until it is reopened, so an
// old board never acts with a newer nhm's state (plan-v4 section 7).
var ErrUpgraded = errors.New("board: nhm was upgraded on disk; reopen the board (prefix+d) to act again")

// ErrUnwatched means the board could not watch its binary, so it could
// not see an upgrade: it takes no mutations at all (it fails closed).
var ErrUnwatched = errors.New("board: the nhm binary cannot be watched, so this board takes no actions")

// Binary watches the nhm binary the board runs from: the path it was
// started by, and the file that path resolved to then.
type Binary struct {
	launch string
	path   string
	info   os.FileInfo
	err    error
}

// LaunchPath is the path the running nhm was started by, made absolute:
// os.Args[0] when it names a file, else what PATH resolves it to, else
// the running executable. herdr starts a plugin pane's "../nhm" as
// "<plugin dir>/../nhm" in the workspace's directory (checked on herdr
// 0.9.3), and `nhm board --go` in a shell is PATH's nhm link. Call it
// before anything changes the working directory.
func LaunchPath() (string, error) {
	arg := os.Args[0]
	if !strings.ContainsRune(arg, os.PathSeparator) {
		if p, err := exec.LookPath(arg); err == nil {
			arg = p
		} else if exe, err := os.Executable(); err == nil {
			return exe, nil
		} else {
			return "", fmt.Errorf("board: the nhm binary: %w", err)
		}
	}
	return filepath.Abs(arg)
}

// WatchBinary records the binary that launch names (LaunchPath when it
// is ""): the path, and the file it resolves to now. An install keeps
// the old release and switches the links (nhm, current, releases/<v>)
// to the new one, so the file the board runs from never changes: Check
// resolves the path again each time.
func WatchBinary(launch string) (*Binary, error) {
	if launch == "" {
		var err error
		if launch, err = LaunchPath(); err != nil {
			return nil, err
		}
	}
	path, err := filepath.EvalSymlinks(launch)
	if err != nil {
		return nil, fmt.Errorf("board: the nhm binary: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("board: the nhm binary: %w", err)
	}
	return &Binary{launch: launch, path: path, info: info}, nil
}

// Unwatched is the binary of a board that could not watch it: Check
// always fails, with ErrUnwatched and why.
func Unwatched(why error) *Binary {
	return &Binary{err: fmt.Errorf("%w (%v); reopen it from an installed nhm", ErrUnwatched, why)}
}

// Check returns ErrUpgraded when the launch path no longer resolves to
// the file the board started from: another target (a switched link),
// another file at the same path (a rename over it), another size or
// another modification time. A path that no longer resolves counts too.
func (b *Binary) Check() error {
	if b.err != nil {
		return b.err
	}
	path, err := filepath.EvalSymlinks(b.launch)
	if err != nil || path != b.path {
		return ErrUpgraded
	}
	now, err := os.Stat(path)
	if err != nil || !os.SameFile(b.info, now) || now.Size() != b.info.Size() || !now.ModTime().Equal(b.info.ModTime()) {
		return ErrUpgraded
	}
	return nil
}

// Path is the file the launch path resolved to at start.
func (b *Binary) Path() string { return b.path }
