package lifecycle

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Job is a process of a pane's shell session other than the shell: a job
// the shell started, in the foreground or not.
type Job struct {
	PID int
	// State is /proc's state letter: R, S, D, T (stopped by a signal), t
	// (stopped by a tracer), Z …
	State byte
	Argv  []string
}

// Stopped reports a job that is stopped, as ctrl+z leaves one.
func (j Job) Stopped() bool { return j.State == 'T' || j.State == 't' }

// Runs reports a job whose program, or the script its interpreter runs, is
// command (a base name, such as claude).
func (j Job) Runs(command string) bool {
	for _, arg := range j.Argv[:min(len(j.Argv), 2)] {
		if filepath.Base(arg) == command {
			return true
		}
	}
	return false
}

// procDir is where SessionJobs reads the processes.
var procDir = "/proc"

// SessionJobs lists the processes whose session the shell shellPID leads,
// the shell left out, from /proc. A process that ends while it is read is
// left out.
func SessionJobs(shellPID int) ([]Job, error) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil, fmt.Errorf("the shell's jobs: %w", err)
	}
	var jobs []Job
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == shellPID {
			continue
		}
		state, session, err := procStat(pid)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("the shell's jobs: %w", err)
		}
		if session != shellPID {
			continue
		}
		job := Job{PID: pid, State: state}
		if data, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline")); err == nil {
			for _, arg := range bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0}) {
				job.Argv = append(job.Argv, string(arg))
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// procStat reads a process's state and session id from /proc/<pid>/stat.
// The command name in parentheses may hold spaces and parentheses, so the
// fields are read after its last ')'.
func procStat(pid int) (byte, int, error) {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, err
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("/proc/%d/stat: no command name", pid)
	}
	// After the name: state, ppid, pgrp, session, …
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 4 || len(fields[0]) != 1 {
		return 0, 0, fmt.Errorf("/proc/%d/stat: too few fields", pid)
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return 0, 0, fmt.Errorf("/proc/%d/stat: session: %w", pid, err)
	}
	return fields[0][0], session, nil
}
