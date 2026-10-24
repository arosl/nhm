// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/doctor_test.go at commit 9d5ed977: the cases of
// TestClassifyContentionProcess, TestSameOrChildPath,
// TestCommandHasExecutable and TestTerminateContentionHolderGuards; see
// LICENSE in this directory and PROVENANCE.md. Local changes: nhm's names;
// a br case, a blocked and a sleeping case; a real SIGTERM, an ended
// holder, and a scan of a fake /proc.
package contention

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ntm's TestClassifyContentionProcess.
func TestClassify(t *testing.T) {
	got := Classify(Process{Command: "rsync registry", Status: "D", OpenFiles: []string{"/work/ntm/.beads/beads.db-wal"}})
	if strings.Join(got, ",") != "beads_db,rsync,uninterruptible_io" {
		t.Fatalf("resources = %v", got)
	}
	if got := Classify(Process{Command: "go test ./..."}); len(got) != 0 {
		t.Fatalf("non-contention process classified as %v", got)
	}
	if got := Classify(Process{Command: "/usr/bin/cargo test"}); len(got) != 1 || got[0] != "cargo_registry" {
		t.Fatalf("cargo process classified as %v", got)
	}
	if got := Classify(Process{Command: "/usr/local/bin/br ready --json"}); len(got) != 1 || got[0] != "beads_db" {
		t.Fatalf("br process classified as %v", got)
	}
	// A scan's word for state D (the operator's ruling: ntm never finds it).
	if got := Classify(Process{Command: "dd if=/dev/sda", Status: "blocked"}); len(got) != 1 || got[0] != "uninterruptible_io" {
		t.Fatalf("blocked process classified as %v", got)
	}
	if got := Classify(Process{Command: "sleep 9", Status: "sleep"}); len(got) != 0 {
		t.Fatalf("sleeping process classified as %v", got)
	}
}

// ntm's TestSameOrChildPath and TestCommandHasExecutable.
func TestPathsAndExecutables(t *testing.T) {
	if !sameOrChildPath("/work/ntm/subdir", "/work/ntm") || sameOrChildPath("/work/other", "/work/ntm") || sameOrChildPath("", "/work/ntm") {
		t.Fatal("sameOrChildPath")
	}
	if !commandHasExecutable("/usr/local/bin/br ready --json", "br") || commandHasExecutable("brr ready", "br") || commandHasExecutable("", "br") {
		t.Fatal("commandHasExecutable")
	}
}

// ntm's TestTerminateContentionHolderGuards.
func TestTerminateGuards(t *testing.T) {
	holders := []Holder{{PID: 123, InSwarm: true}}
	if err := Terminate(holders, 123, false, false); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("missing confirmation error = %v", err)
	}
	if err := Terminate(holders, 123, true, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("in-swarm refusal error = %v", err)
	}
	if err := Terminate(holders, 999, true, false); err == nil || !strings.Contains(err.Error(), "not a detected") {
		t.Fatalf("unknown PID error = %v", err)
	}
	if err := Terminate(holders, 0, false, false); err != nil {
		t.Fatalf("no pid = %v", err)
	}
}

// A listed holder outside the swarm gets SIGTERM.
func TestTerminateSendsSIGTERM(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if err := Terminate([]Holder{{PID: cmd.Process.Pid}}, cmd.Process.Pid, true, false); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("sleep ended with %v, want SIGTERM", err)
	}
	// Reaped, it is gone: ntm's error for a holder that ended.
	if err := Terminate([]Holder{{PID: cmd.Process.Pid}}, cmd.Process.Pid, true, false); err == nil ||
		err.Error() != "open PID "+strconv.Itoa(cmd.Process.Pid)+": process does not exist" {
		t.Fatalf("ended holder = %v", err)
	}
}

func writeProc(t *testing.T, root, pid, cmdline, cwd, stat string, fds map[string]string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if cwd != "" {
		if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
			t.Fatal(err)
		}
	}
	for fd, target := range fds {
		if err := os.Symlink(target, filepath.Join(dir, "fd", fd)); err != nil {
			t.Fatal(err)
		}
	}
}

// stat is a /proc/<pid>/stat line with state and start time (field 22).
func stat(pid, comm, state string, start int) string {
	fields := []string{pid, "(" + comm + ")", state}
	for i := 4; i <= 21; i++ {
		fields = append(fields, "0")
	}
	fields = append(fields, strconv.Itoa(start), "0", "0")
	return strings.Join(fields, " ") + "\n"
}

// The scan reads /proc: the command line, cwd, state, age and open files;
// kernel threads (no command line) and non-pid entries are left out.
func TestHoldersFromProc(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 2 3\nbtime 1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProc(t, root, "300", "cargo\x00\x00build\x00", "/work/other", stat("300", "cargo", "D", 5000), nil)
	writeProc(t, root, "100", "/usr/bin/sqlite3\x00x\x00", "/work/p/sub", stat("100", "sqlite3 (x)", "S", 1000),
		map[string]string{"3": "/work/p/.beads/beads.db", "4": "pipe:[1]"})
	writeProc(t, root, "200", "", "", stat("200", "kthreadd", "S", 0), nil)
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := Scanner{Root: root, ClockTicks: 100, Now: func() time.Time { return time.Unix(1100, 0) }}
	holders, err := s.Holders(context.Background(), "/work/p")
	if err != nil {
		t.Fatal(err)
	}
	// 300 is in state D: the scan reads it as "blocked", and nhm reports
	// it as uninterruptible_io, where ntm finds nothing.
	if len(holders) != 3 {
		t.Fatalf("holders = %+v", holders)
	}
	if h := holders[0]; h.PID != 100 || h.Resource != "beads_db" || !h.InSwarm || h.CWD != "/work/p/sub" || h.Command != "/usr/bin/sqlite3 x" || h.Age != "1m30s" {
		t.Fatalf("holder 100 = %+v", h)
	}
	if h := holders[1]; h.PID != 300 || h.Resource != "cargo_registry" || h.InSwarm || h.Age != "50s" || h.Command != "cargo build" {
		t.Fatalf("holder 300 = %+v", h)
	}
	if h := holders[2]; h.PID != 300 || h.Resource != "uninterruptible_io" || h.InSwarm {
		t.Fatalf("holder 300 io = %+v", h)
	}
	procs, err := s.Processes(context.Background())
	if err != nil || len(procs) != 2 || procs[0].PID != 100 || procs[0].Status != "sleep" || procs[1].Status != "blocked" {
		t.Fatalf("processes = %+v %v", procs, err)
	}
	if _, err := (Scanner{Root: filepath.Join(root, "nosuch")}).Holders(context.Background(), ""); err == nil || !strings.HasPrefix(err.Error(), "list processes: ") {
		t.Fatalf("no /proc: %v", err)
	}
}
