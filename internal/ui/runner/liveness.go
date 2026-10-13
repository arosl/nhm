package runner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ClaimTimeout is how long a started job may stay unclaimed. A waiting
// surface then voids it (Void): it never runs, and nothing ran. A claim
// that names no process this long after it was made (its write failed) is
// gone.
const ClaimTimeout = 15 * time.Second

// ErrGone is a claimed job whose process ended without a result: a kill,
// a crash or a full disk. Some of its effects may have happened, so its
// outcome is unknown until its record is read.
var ErrGone = errors.New("runner: the job ended without a result; its outcome is unknown")

// procStat reads a process's state and start time from /proc; a variable
// so tests can replace it.
var procStat = readProcStat

// readProcStat reads the state (field 3) and the start time in clock
// ticks after boot (field 22) of /proc/<pid>/stat. A pid that is reused
// gets a new start time.
func readProcStat(pid int) (state string, start uint64, err error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, err
	}
	// Field 2, the command, is in parentheses and may hold spaces: the
	// fields after it start after its last ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", 0, fmt.Errorf("runner: /proc/%d/stat: no command field", pid)
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); field 22 is f[19].
	if len(f) < 20 {
		return "", 0, fmt.Errorf("runner: /proc/%d/stat: %d fields", pid, len(f)+2)
	}
	start, err = strconv.ParseUint(f[19], 10, 64)
	return f[0], start, err
}

// claimLine is what a claim holds: the claimant's pid and, where /proc
// gives it, the process's start time, so that a reused pid is not taken
// for the job.
func claimLine() string {
	pid := os.Getpid()
	if _, st, err := procStat(pid); err == nil {
		return fmt.Sprintf("%d %d\n", pid, st)
	}
	return fmt.Sprintf("%d\n", pid)
}

// Claimed reports whether a job has its claim.
func Claimed(dir, id string) bool {
	_, err := os.Stat(claimPath(dir, id))
	return err == nil
}

// Gone reports whether the process that claimed a job has ended. A claim
// that names no process yet is not gone while it is new: it is being
// written. One that still names none after ClaimTimeout is: its write
// failed (a full disk), or its process was killed before it.
func Gone(dir, id string) bool {
	path := claimPath(dir, id)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	f := strings.Fields(string(data))
	pid := 0
	if len(f) > 0 {
		pid, _ = strconv.Atoi(f[0])
	}
	if pid <= 0 {
		fi, err := os.Stat(path)
		return err == nil && time.Since(fi.ModTime()) >= ClaimTimeout
	}
	kerr := syscall.Kill(pid, 0)
	if errors.Is(kerr, syscall.ESRCH) {
		return true
	}
	if len(f) < 2 {
		// No start time (no /proc, or a claim of v0.1.6): kill's answer
		// stands. EPERM: another user's process holds the pid; the job ran
		// as this user, so it is not the job.
		return errors.Is(kerr, syscall.EPERM)
	}
	state, st, err := procStat(pid)
	if err != nil {
		// The claim has a start time, so /proc showed the process: when
		// its entry is gone, so is the process.
		return errors.Is(err, fs.ErrNotExist)
	}
	// A zombie has ended; its parent has not reaped it yet.
	return state == "Z" || strconv.FormatUint(st, 10) != f[1]
}

// Void ends a job that nhm ui run has not claimed: it takes the claim, so
// the job never runs, and writes its result, which says that nothing
// ran. ErrRan: the job was claimed first, and it runs.
func Void(dir, id, verb string, now time.Time) (Result, error) {
	if err := Claim(dir, id); err != nil {
		return Result{}, err
	}
	r := Result{ID: id, Verb: verb, Code: CodeError, StartedAt: now.UTC(), EndedAt: now.UTC(),
		Error: fmt.Sprintf("runner: %s did not start within %s; it will not run: nothing ran", verb, ClaimTimeout)}
	return r, Finish(dir, r)
}

// check looks at a job that had no result at the last read. done is true
// when the wait is over: the job ended (its result came in since), its
// process ended without a result (ErrGone), or it was not claimed in time
// and is now void.
func (c Client) check(id string) (r Result, done bool, err error) {
	if Claimed(c.JobDir, id) {
		if !Gone(c.JobDir, id) {
			return Result{}, false, nil
		}
		// The job may have written its result just before it ended.
		if r, ok, err := c.Result(id); err != nil || ok {
			return r, true, err
		}
		return Result{}, true, ErrGone
	}
	verb, created := "job", time.Time{}
	if j, err := Load(JobPath(c.JobDir, id)); err == nil {
		verb, created = j.Verb, j.CreatedAt
	} else if fi, err := os.Stat(JobPath(c.JobDir, id)); err == nil {
		// A job file that does not load is never claimed: void it too.
		created = fi.ModTime()
	} else {
		return Result{}, false, nil
	}
	if c.now().Sub(created) < ClaimTimeout {
		return Result{}, false, nil
	}
	r, err = Void(c.JobDir, id, verb, c.now())
	if errors.Is(err, ErrRan) {
		// The job was claimed first: it runs, and the wait goes on.
		return Result{}, false, nil
	}
	return r, true, err
}
