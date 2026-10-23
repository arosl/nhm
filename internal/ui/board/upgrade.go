package board

import (
	"errors"
	"fmt"
	"io/fs"
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

// ErrUnchecked means a check of the binary failed for another reason
// than a missing file (a permission, a stale handle): the board fails
// closed as after an upgrade, and says so.
var ErrUnchecked = errors.New("board: nhm's binary cannot be checked")

// selfExe names the running binary (a test points it at a fixture).
var selfExe = "/proc/self/exe"

// Binary watches the nhm binary the board runs from: the path it was
// started by, and the file that path resolved to then.
type Binary struct {
	launch string
	path   string
	info   os.FileInfo
	err    error
}

// LaunchPath is the path the running nhm was started by, made absolute:
// os.Args[0] when it names a file, else what PATH resolves it to. herdr
// starts a plugin pane's "../nhm" as "<plugin dir>/../nhm" in the
// workspace's directory (checked on herdr 0.9.3), and `nhm board` in a
// shell is PATH's nhm link. A bare name that PATH does not resolve
// is an error: the running executable is the resolved release file,
// which an install never changes, so watching it would never freeze the
// board. Call it before anything changes the working directory.
func LaunchPath() (string, error) {
	arg := os.Args[0]
	if !strings.ContainsRune(arg, os.PathSeparator) {
		p, err := exec.LookPath(arg)
		if err != nil {
			return "", fmt.Errorf("board: the path nhm was started by: %w", err)
		}
		arg = p
	}
	return filepath.Abs(arg)
}

// WatchRunning is WatchBinary for the running board: the launch path
// must resolve to the running binary. An install that switched the links
// between herdr's start of the board and this call would otherwise be
// taken for the board's own binary, and never freeze it.
func WatchRunning(launch string) (*Binary, error) {
	b, err := WatchBinary(launch)
	if err != nil {
		return nil, err
	}
	self, err := os.Stat(selfExe)
	if err != nil {
		return nil, fmt.Errorf("board: the running binary: %w", err)
	}
	if !os.SameFile(b.info, self) {
		return nil, fmt.Errorf("board: %s is no longer the running nhm (an install switched it as the board started)", launch)
	}
	return b, nil
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
// Any other error is ErrUnchecked: the board fails closed all the same.
func (b *Binary) Check() error {
	if b.err != nil {
		return b.err
	}
	path, err := filepath.EvalSymlinks(b.launch)
	if err != nil {
		return checkErr(err)
	}
	if path != b.path {
		return ErrUpgraded
	}
	now, err := os.Stat(path)
	if err != nil {
		return checkErr(err)
	}
	if !os.SameFile(b.info, now) || now.Size() != b.info.Size() || !now.ModTime().Equal(b.info.ModTime()) {
		return ErrUpgraded
	}
	return nil
}

// checkErr is a failed check's error: a missing file is an upgrade (an
// install removed or switched it away), anything else is ErrUnchecked.
func checkErr(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrUpgraded
	}
	return fmt.Errorf("%w (%v), so this board takes no new actions; reopen it (prefix+d)", ErrUnchecked, err)
}

// Path is the file the launch path resolved to at start.
func (b *Binary) Path() string { return b.path }
