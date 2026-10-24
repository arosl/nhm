// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/doctor.go
// (ContentionHolder, contentionCommandOptions, findContentionHolders,
// classifyContentionProcess, commandHasExecutable, sameOrChildPath,
// terminateContentionHolder) at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the processes are read from
// /proc (Linux, as nhm's other process reads), not through gopsutil, the
// way gopsutil reads them: the command line's fields joined by spaces, the
// cwd link, the start time from stat's field 22 and the boot time, the
// state in gopsutil's words, the fds' targets; the scan takes its /proc
// root and clock, so tests give their own.
package contention

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Holder is a process that holds a resource that often blocks multi-agent
// work: ntm's ContentionHolder.
type Holder struct {
	PID      int    `json:"pid"`
	CWD      string `json:"cwd,omitempty"`
	Command  string `json:"command"`
	Resource string `json:"resource"`
	Age      string `json:"age"`
	InSwarm  bool   `json:"in_swarm"`
}

// Process is what the scan reads of one process.
type Process struct {
	PID       int
	CWD       string
	Command   string
	Age       time.Duration
	Status    string // the state in gopsutil's words, as ntm reads it: running, sleep, blocked, …
	OpenFiles []string
}

// stateWord is gopsutil's word for a Linux state letter, which ntm's
// contention reads: D is "blocked", not "d".
func stateWord(letter string) string {
	switch letter {
	case "R":
		return "running"
	case "S":
		return "sleep"
	case "D":
		return "blocked"
	case "T", "t":
		return "stop"
	case "Z":
		return "zombie"
	case "I":
		return "idle"
	case "W":
		return "wait"
	}
	return ""
}

// Scanner reads processes from a /proc tree.
type Scanner struct {
	Root string // "/proc"
	Now  func() time.Time
	// ClockTicks is USER_HZ, the unit of stat's start time (100 on Linux).
	ClockTicks int64
}

// DefaultScanner reads the host's /proc.
func DefaultScanner() Scanner { return Scanner{Root: "/proc", Now: time.Now, ClockTicks: 100} }

// Processes lists every process with a command line; kernel threads have
// none and are left out.
func (s Scanner) Processes(ctx context.Context) ([]Process, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	boot := s.bootTime()
	now := s.Now()
	var out []Process
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(s.Root, e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		command := strings.Join(strings.FieldsFunc(string(raw), func(r rune) bool { return r == 0 }), " ")
		if strings.TrimSpace(command) == "" {
			continue
		}
		p := Process{PID: pid, Command: command}
		p.CWD, _ = os.Readlink(filepath.Join(dir, "cwd"))
		if state, start, ok := readStat(filepath.Join(dir, "stat")); ok {
			p.Status = stateWord(state)
			if !boot.IsZero() && s.ClockTicks > 0 {
				created := boot.Add(time.Duration(start) * time.Second / time.Duration(s.ClockTicks))
				if age := now.Sub(created); age > 0 {
					p.Age = age
				}
			}
		}
		if fds, err := os.ReadDir(filepath.Join(dir, "fd")); err == nil {
			for _, fd := range fds {
				if target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name())); err == nil && strings.HasPrefix(target, "/") {
					p.OpenFiles = append(p.OpenFiles, target)
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// readStat is a process's state letter and start time (field 22, in
// clock ticks after boot) from /proc/<pid>/stat.
func readStat(path string) (state string, start int64, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return "", 0, false
	}
	start, err = strconv.ParseInt(f[19], 10, 64)
	return f[0], start, err == nil
}

// bootTime is the btime line of /proc/stat.
func (s Scanner) bootTime() time.Time {
	data, err := os.ReadFile(filepath.Join(s.Root, "stat"))
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(sec, 0)
			}
		}
	}
	return time.Time{}
}

// Holders are the processes that hold a shared resource, one row per
// resource, by pid then resource; a holder whose cwd is in swarmCWD is in
// the swarm.
func (s Scanner) Holders(ctx context.Context, swarmCWD string) ([]Holder, error) {
	procs, err := s.Processes(ctx)
	if err != nil {
		return nil, err
	}
	holders := make([]Holder, 0)
	for _, p := range procs {
		for _, resource := range Classify(p) {
			holders = append(holders, Holder{PID: p.PID, CWD: p.CWD, Command: p.Command, Resource: resource,
				Age: p.Age.Round(time.Second).String(), InSwarm: sameOrChildPath(p.CWD, swarmCWD)})
		}
	}
	sort.Slice(holders, func(i, j int) bool {
		if holders[i].PID != holders[j].PID {
			return holders[i].PID < holders[j].PID
		}
		return holders[i].Resource < holders[j].Resource
	})
	return holders, nil
}

// Classify names the resources a process holds: the beads database (an
// open beads.db file, or a br or bd command), cargo's registry, rsync,
// and an uninterruptible wait (a status of "d"). A scan gives gopsutil's
// words, as ntm's does, so it never finds the last one: nhm does as ntm.
func Classify(p Process) []string {
	command := strings.ToLower(p.Command)
	resources := make([]string, 0, 3)
	hasBeadsDB := false
	for _, path := range p.OpenFiles {
		path = filepath.ToSlash(strings.ToLower(path))
		if strings.HasSuffix(path, "/.beads/beads.db") || strings.HasSuffix(path, "/.beads/beads.db-wal") || strings.HasSuffix(path, "/.beads/beads.db-shm") {
			resources = append(resources, "beads_db")
			hasBeadsDB = true
			break
		}
	}
	if !hasBeadsDB && (commandHasExecutable(command, "br") || commandHasExecutable(command, "bd")) {
		resources = append(resources, "beads_db")
	}
	if commandHasExecutable(command, "cargo") {
		resources = append(resources, "cargo_registry")
	}
	if strings.Contains(command, "rsync") {
		resources = append(resources, "rsync")
	}
	if strings.EqualFold(strings.TrimSpace(p.Status), "d") {
		resources = append(resources, "uninterruptible_io")
	}
	return resources
}

func commandHasExecutable(command, executable string) bool {
	fields := strings.Fields(command)
	return len(fields) > 0 && filepath.Base(fields[0]) == executable
}

func sameOrChildPath(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Terminate sends SIGTERM to a listed holder: only with confirm, and an
// in-swarm one only with force too.
func Terminate(holders []Holder, pid int, confirm, force bool) error {
	if pid <= 0 {
		return nil
	}
	if !confirm {
		return errors.New("refusing termination without --confirm")
	}
	var target *Holder
	for i := range holders {
		if holders[i].PID == pid {
			target = &holders[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("PID %d is not a detected contention holder", pid)
	}
	if target.InSwarm && !force {
		return fmt.Errorf("refusing to terminate in-swarm PID %d without --force", pid)
	}
	// A holder that ended after the scan: gopsutil's NewProcess error.
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("open PID %d: process does not exist", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("terminate PID %d: %w", pid, err)
	}
	return nil
}
