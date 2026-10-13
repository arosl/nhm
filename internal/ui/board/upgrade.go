package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrUpgraded means the nhm binary changed on disk after the board
// started: the board takes no new mutations until it is reopened, so an
// old board never acts with a newer nhm's state (plan-v4 section 7).
var ErrUpgraded = errors.New("board: nhm was upgraded on disk; reopen the board (prefix+d) to act again")

// Binary watches the nhm binary the board runs from.
type Binary struct {
	path string
	info os.FileInfo
}

// WatchBinary records the binary at path, or the running one when path
// is "". It resolves the path at once: after a replace, Linux names the
// running file "(deleted)".
func WatchBinary(path string) (*Binary, error) {
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("board: the nhm binary: %w", err)
		}
		path = exe
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("board: the nhm binary: %w", err)
	}
	return &Binary{path: path, info: info}, nil
}

// Check returns ErrUpgraded when the file at the binary's path is not the
// one the board started from: another file (a rename over it), another
// size or another modification time. A file that is gone counts too.
func (b *Binary) Check() error {
	now, err := os.Stat(b.path)
	if err != nil || !os.SameFile(b.info, now) || now.Size() != b.info.Size() || !now.ModTime().Equal(b.info.ModTime()) {
		return ErrUpgraded
	}
	return nil
}

// Path is the binary's resolved path.
func (b *Binary) Path() string { return b.path }
