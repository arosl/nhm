package runner

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// TestEnded: a job's run is over once it has a result, or once the
// process that claimed it has ended; never while it is not written, not
// claimed or running, because then it may still run (actions.Guard
// releases a held request on Ended).
func TestEnded(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	c := Client{JobDir: dir, Now: func() time.Time { return now }}
	j := testJob(t, VerbReap, nil, now)
	if c.Ended(j.ID) {
		t.Fatal("a job with no file has ended")
	}
	if err := write(dir, j); err != nil {
		t.Fatal(err)
	}
	if c.Ended(j.ID) {
		t.Fatal("a job not claimed has ended")
	}
	writeClaim(t, dir, j.ID, fmt.Sprintf("%d\n", os.Getpid()))
	if c.Ended(j.ID) {
		t.Fatal("a running job has ended")
	}
	if err := Finish(dir, Result{ID: j.ID, Verb: j.Verb, Code: CodeOK}); err != nil {
		t.Fatal(err)
	}
	if !c.Ended(j.ID) {
		t.Fatal("a job with its result has not ended")
	}
	// Claimed by a process that ended before it wrote a result.
	dead := testJob(t, VerbReap, nil, now.Add(time.Millisecond))
	if err := write(dir, dead); err != nil {
		t.Fatal(err)
	}
	writeClaim(t, dir, dead.ID, fmt.Sprintf("%d\n", deadPid(t)))
	if !c.Ended(dead.ID) {
		t.Fatal("a job whose process is gone has not ended")
	}
	if c.Ended("../" + j.ID) {
		t.Fatal("an invalid id has ended")
	}
}
