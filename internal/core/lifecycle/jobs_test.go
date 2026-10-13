package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SessionJobs reads /proc: a process of the shell's session, the shell
// left out, with its state and argv. A command name with spaces and
// parentheses does not move the fields; a process that is gone, a
// process of another session and a non-process entry are left out.
func TestSessionJobsReadsProc(t *testing.T) {
	dir := t.TempDir()
	write := func(pid, stat, cmdline string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if stat != "" {
			writeFile(t, filepath.Join(dir, pid, "stat"), stat)
		}
		writeFile(t, filepath.Join(dir, pid, "cmdline"), cmdline)
	}
	write("77", "77 (bash) S 1 77 77 34816 0", "-bash\x00")
	write("123", "123 (my (odd) name) T 77 123 77 34816 0", "node\x00/opt/claude/cli.js\x00")
	write("124", "124 (claude) S 1 124 78 0 0", "claude\x00")
	write("125", "", "")
	write("self", "1 (x) S 1 1 77", "")
	old := procDir
	procDir = dir
	t.Cleanup(func() { procDir = old })

	jobs, err := SessionJobs(77)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("SessionJobs = %+v %v", jobs, err)
	}
	j := jobs[0]
	if j.PID != 123 || j.State != 'T' || !j.Stopped() || !slices.Equal(j.Argv, []string{"node", "/opt/claude/cli.js"}) {
		t.Fatalf("job = %+v", j)
	}
	if j.Runs("claude") || !j.Runs("cli.js") || !(Job{Argv: []string{"/usr/bin/claude"}}).Runs("claude") ||
		(Job{Argv: []string{"sh", "-c", "claude"}}).Runs("claude") {
		t.Fatalf("Runs does not read argv[0] and argv[1] only")
	}
	writeFile(t, filepath.Join(dir, "126", "stat"), "126 no name")
	if _, err := SessionJobs(77); err == nil || !strings.Contains(err.Error(), "126") {
		t.Fatalf("an unreadable stat = %v", err)
	}
}

// On the real /proc: a child of this test is in the test's session, and
// it shows as stopped after SIGSTOP, as ctrl+z leaves an agent.
func TestSessionJobsFindsAStoppedChild(t *testing.T) {
	_, session, err := procStat(os.Getpid())
	if err != nil {
		t.Skipf("no /proc here: %v", err)
	}
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		jobs, err := SessionJobs(session)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(jobs, func(j Job) bool { return j.PID == cmd.Process.Pid })
		if i >= 0 && jobs[i].Stopped() && jobs[i].Runs("sleep") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stopped child %d is not among the session's jobs: %+v", cmd.Process.Pid, jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
