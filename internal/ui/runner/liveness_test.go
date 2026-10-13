package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// deadPid is the pid of a process that has ended and been reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func writeClaim(t *testing.T, dir, id, line string) {
	t.Helper()
	if err := os.WriteFile(claimPath(dir, id), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A claim's process is gone when its pid is free, or when the pid now
// belongs to a process that started later; a claim being written, or
// one with a live pid and no start time, is not gone.
func TestGone(t *testing.T) {
	dir := t.TempDir()
	if Gone(dir, "op-none") {
		t.Fatal("a job with no claim is gone")
	}
	if err := Claim(dir, "op-mine"); err != nil {
		t.Fatal(err)
	}
	if Gone(dir, "op-mine") {
		t.Fatal("this process's claim is gone")
	}
	cases := []struct {
		line string
		gone bool
	}{
		{fmt.Sprintf("%d\n", deadPid(t)), true},
		{"", false},
		{fmt.Sprintf("%d\n", os.Getpid()), false},
	}
	if _, st, err := procStat(os.Getpid()); err == nil {
		cases = append(cases, struct {
			line string
			gone bool
		}{fmt.Sprintf("%d %d\n", os.Getpid(), st+1), true})
	}
	for i, c := range cases {
		id := fmt.Sprintf("op-%d", i)
		writeClaim(t, dir, id, c.line)
		if got := Gone(dir, id); got != c.gone {
			t.Errorf("claim %q: gone = %v, want %v", c.line, got, c.gone)
		}
	}
	// A claim that still names no process after ClaimTimeout is gone: its
	// write failed.
	old := time.Now().Add(-ClaimTimeout - time.Second)
	if err := os.Chtimes(claimPath(dir, "op-1"), old, old); err != nil {
		t.Fatal(err)
	}
	if !Gone(dir, "op-1") {
		t.Error("an old claim with no pid is not gone")
	}
	// A zombie has ended; a live pid at its start time has not.
	st := uint64(0)
	if _, s, err := procStat(os.Getpid()); err == nil {
		st = s
	}
	state := "Z"
	procStat = func(int) (string, uint64, error) { return state, st, nil }
	t.Cleanup(func() { procStat = readProcStat })
	writeClaim(t, dir, "op-z", fmt.Sprintf("%d %d\n", os.Getpid(), st))
	if !Gone(dir, "op-z") {
		t.Error("a zombie is not gone")
	}
	state = "S"
	if Gone(dir, "op-z") {
		t.Error("a sleeping process at its start time is gone")
	}
}

// check reads the result again when it finds the claimant gone: a job
// that wrote its result and ended in between is no lost answer.
func TestCheckReadsAgain(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c := Client{JobDir: dir, Now: func() time.Time { return now }}
	j := testJob(t, VerbReap, nil, now)
	if err := write(dir, j); err != nil {
		t.Fatal(err)
	}
	writeClaim(t, dir, j.ID, fmt.Sprintf("%d\n", deadPid(t)))
	if err := Finish(dir, Result{ID: j.ID, Verb: j.Verb, Code: CodeOK}); err != nil {
		t.Fatal(err)
	}
	if r, done, err := c.check(j.ID); !done || err != nil || r.Code != CodeOK || r.ID != j.ID {
		t.Fatalf("check = %+v %v %v", r, done, err)
	}
}

// A void that comes after the job's own claim loses: the job runs, and
// nothing is written for it.
func TestVoidLosesToTheClaim(t *testing.T) {
	dir := t.TempDir()
	j := testJob(t, VerbWake, nil, time.Now())
	if err := write(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := Claim(dir, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Void(dir, j.ID, j.Verb, time.Now()); !errors.Is(err, ErrRan) {
		t.Fatalf("void after the claim = %v", err)
	}
	if _, ok, _ := Read(dir, j.ID); ok {
		t.Fatal("a lost void wrote a result")
	}
}

// Wait ends when a claimed job's process ended without a result
// (ErrGone), and returns a result that came in before it ended.
func TestWaitGone(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c := Client{JobDir: dir, Now: func() time.Time { return now }}
	j := testJob(t, VerbReap, nil, now)
	if err := write(dir, j); err != nil {
		t.Fatal(err)
	}
	writeClaim(t, dir, j.ID, fmt.Sprintf("%d\n", deadPid(t)))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Wait(ctx, j.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("wait on a gone job = %v", err)
	}
	if es, err := List(dir, 0); err != nil || len(es) != 1 || !es[0].Gone || es[0].Result != nil {
		t.Fatalf("list = %+v %v", es, err)
	}
	if err := Finish(dir, Result{ID: j.ID, Verb: j.Verb, Code: CodeOK}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Wait(ctx, j.ID); err != nil || r.Code != CodeOK {
		t.Fatalf("wait on an ended job = %+v %v", r, err)
	}
	if es, _ := List(dir, 0); es[0].Gone || es[0].Result == nil {
		t.Fatalf("list after the result = %+v", es)
	}
}

// A job that nhm ui run did not claim within ClaimTimeout is void: Wait
// takes its claim and returns its result (nothing ran), and the job
// never runs. A job inside the timeout is left alone.
func TestWaitVoids(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c := Client{JobDir: dir, Now: func() time.Time { return now }}
	fresh := testJob(t, VerbWake, nil, now.Add(-time.Second))
	late := testJob(t, VerbWake, nil, now.Add(-ClaimTimeout-time.Second))
	for _, j := range []Job{fresh, late} {
		if err := write(dir, j); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*WaitPoll)
	defer cancel()
	if _, err := c.Wait(ctx, fresh.ID); !errors.Is(err, context.DeadlineExceeded) || Claimed(dir, fresh.ID) {
		t.Fatalf("wait on a fresh job = %v, claimed %v", err, Claimed(dir, fresh.ID))
	}
	r, err := c.Wait(context.Background(), late.ID)
	if err != nil || r.Code != CodeError || !strings.Contains(r.Error, "nothing ran") {
		t.Fatalf("wait on a late job = %+v %v", r, err)
	}
	if err := Claim(dir, late.ID); !errors.Is(err, ErrRan) {
		t.Fatalf("claim of a void job = %v", err)
	}
	if stored, ok, err := Read(dir, late.ID); err != nil || !ok || stored.Error != r.Error {
		t.Fatalf("void result = %+v %v %v", stored, ok, err)
	}
}
