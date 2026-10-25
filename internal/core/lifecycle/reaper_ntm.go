// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/send.go (collectPaneDescendants, reapOrphanProcesses,
// orphanReapGrace, orphanReapMaxDepth, orphanReapFanout,
// orphanReapExcluded), internal/process/liveness.go (GetChildPIDs's read
// of the kernel's child list) and internal/cli/reap_unix.go (termProcess,
// killProcess); see LICENSE in this directory and PROVENANCE.md. Local
// changes: a ProcReaper value holds the /proc root, the grace and the
// signal function, so a test never signals a host process; Reap returns
// the processes it signalled; no native fallback where /proc lacks the
// child list.
package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcReaper is the host's Reaper: the kernel's child lists under Root
// (/proc), then SIGTERM, Grace, and SIGKILL for what is left, as ntm's
// kill does. A ProcReaper without Signal signals nothing.
type ProcReaper struct {
	Root   string
	Grace  time.Duration
	Signal func(pid int, sig syscall.Signal) error
}

// HostReaper is the reaper nhm kill runs: /proc, and ntm's 750 ms grace.
func HostReaper() ProcReaper {
	return ProcReaper{Root: "/proc", Grace: 750 * time.Millisecond, Signal: syscall.Kill}
}

// The walk's bounds, against a runaway process tree.
const (
	walkDepth  = 4
	walkFanout = 64
)

// Descendants are the processes below pid, children first: never pid
// itself, this process, its parent or init.
func (r ProcReaper) Descendants(pid int) []int {
	seen := map[int]bool{}
	var out []int
	var walk func(p, depth int)
	walk = func(p, depth int) {
		if depth > walkDepth {
			return
		}
		for _, c := range r.children(p) {
			if c <= 1 || c == os.Getpid() || c == os.Getppid() || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			walk(c, depth+1)
		}
	}
	if pid > 1 {
		walk(pid, 1)
	}
	return out
}

// children reads the kernel's list of a process's children. A process
// that is gone, or whose list cannot be read, has none to end.
func (r ProcReaper) children(pid int) []int {
	data, err := os.ReadFile(filepath.Join(r.Root, strconv.Itoa(pid), "task", strconv.Itoa(pid), "children"))
	if err != nil {
		return nil
	}
	var out []int
	for _, f := range strings.Fields(string(data)) {
		if c, err := strconv.Atoi(f); err == nil && c > 0 {
			out = append(out, c)
			if len(out) == walkFanout {
				break
			}
		}
	}
	return out
}

// Reap sends SIGTERM to each process that is still alive, waits Grace,
// then sends SIGKILL to any that remain. It returns the processes it
// signalled.
func (r ProcReaper) Reap(pids []int) []int {
	if r.Signal == nil {
		return nil
	}
	var termed []int
	for _, pid := range pids {
		if pid <= 1 || pid == os.Getpid() || r.Signal(pid, 0) != nil {
			continue
		}
		_ = r.Signal(pid, syscall.SIGTERM)
		termed = append(termed, pid)
	}
	if len(termed) == 0 {
		return nil
	}
	time.Sleep(r.Grace)
	for _, pid := range termed {
		if r.Signal(pid, 0) == nil {
			_ = r.Signal(pid, syscall.SIGKILL)
		}
	}
	return termed
}
