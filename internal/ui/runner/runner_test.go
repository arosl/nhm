package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui"
)

func testJob(t *testing.T, verb string, req any, at time.Time) Job {
	t.Helper()
	j, err := NewJob(verb, ui.Context{SocketPath: "/run/herdr.sock", Cwd: t.TempDir()}, req, at)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestJobCheckAndDecode(t *testing.T) {
	j := testJob(t, VerbReap, ReapRequest{WorkspaceID: "w1", Idle: time.Hour, Only: []lifecycle.ReapTarget{{BindingID: "b1", Generation: 2}}}, time.Now())
	if err := j.Check(); err != nil {
		t.Fatal(err)
	}
	var r ReapRequest
	if err := j.Decode(&r); err != nil || r.Only[0].Generation != 2 || r.Idle != time.Hour {
		t.Fatalf("decode = %+v %v", r, err)
	}
	// A request from another nhm (an unknown field) is refused.
	odd := j
	odd.Request = json.RawMessage(`{"workspace_id":"w1","all":true}`)
	if err := odd.Decode(&r); err == nil {
		t.Fatal("an unknown field was accepted")
	}
	for _, bad := range []func(*Job){
		func(j *Job) { j.Version = 2 },
		func(j *Job) { j.ID = "../x" },
		func(j *Job) { j.Verb = "" },
		func(j *Job) { j.Socket = "" },
		func(j *Job) { j.Dir = "rel" },
		func(j *Job) { j.Request = json.RawMessage(`{`) },
	} {
		b := j
		bad(&b)
		if b.Check() == nil {
			t.Errorf("job %+v passed the check", b)
		}
	}
}

// fakeNhm is a stand-in for nhm: it writes its argv, cwd and session id.
func fakeNhm(t *testing.T) (bin, out string) {
	t.Helper()
	dir := t.TempDir()
	bin, out = filepath.Join(dir, "nhm"), filepath.Join(dir, "out")
	script := "#!/bin/sh\nsid=$(awk '{print $6}' /proc/$$/stat)\nprintf '%s\\n%s\\n%s\\n' \"$1 $2 $3\" \"$(pwd)\" \"$sid\" > \"$OUT_FILE.tmp\"\nmv \"$OUT_FILE.tmp\" \"$OUT_FILE\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, out
}

func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
	return ""
}

// The job runs as nhm ui run <file>, in its directory, in a session of
// its own; the same id does not start twice.
func TestStartDetaches(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	bin, out := fakeNhm(t)
	dir := t.TempDir()
	j := testJob(t, VerbKill, KillRequest{WorkspaceID: "w1"}, time.Now())
	if err := Start(bin, append(os.Environ(), "OUT_FILE="+out), dir, j); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(waitFile(t, out)), "\n")
	if len(lines) != 3 || lines[0] != "ui run "+JobPath(dir, j.ID) || lines[1] != j.Dir {
		t.Fatalf("child saw %q", lines)
	}
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	// The session is the sixth field; the command (the second) holds no
	// space for a Go test binary.
	mine, err := strconv.Atoi(strings.Fields(string(stat))[5])
	if err != nil {
		t.Fatal(err)
	}
	if sid, err := strconv.Atoi(lines[2]); err != nil || sid == mine {
		t.Fatalf("child session %q, ours %d", lines[2], mine)
	}
	loaded, err := Load(JobPath(dir, j.ID))
	var kr KillRequest
	if err != nil || loaded.ID != j.ID || loaded.Decode(&kr) != nil || kr.WorkspaceID != "w1" {
		t.Fatalf("load = %+v %+v %v", loaded, kr, err)
	}
	if err := Start(bin, nil, dir, j); err == nil {
		t.Fatal("the same job started twice")
	}
}

// A job that cannot start ends at once, with its reason.
func TestStartFailureIsFinal(t *testing.T) {
	dir := t.TempDir()
	j := testJob(t, VerbNew, NewRequest{Label: "p", Project: "/p"}, time.Now())
	if err := Start(filepath.Join(t.TempDir(), "missing"), nil, dir, j); err == nil {
		t.Fatal("a missing binary started")
	}
	r, ok, err := Read(dir, j.ID)
	if err != nil || !ok || r.Code != CodeError || !strings.Contains(r.Error, "did not start") {
		t.Fatalf("result = %+v %v %v", r, ok, err)
	}
	if err := Claim(dir, j.ID); !errors.Is(err, ErrRan) {
		t.Fatalf("a job that did not start can be run: %v", err)
	}
}

func TestClaimFinishReadList(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	var jobs []Job
	for i := 0; i < 3; i++ {
		j := testJob(t, VerbWake, WakeRequest{Recipients: []ui.Target{{BindingID: "b1", Generation: 1}}}, t0.Add(time.Duration(i)*time.Minute))
		if err := write(dir, j); err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, j)
	}
	if _, ok, err := Read(dir, jobs[0].ID); ok || err != nil {
		t.Fatalf("a result before the end: %v %v", ok, err)
	}
	if err := Claim(dir, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := Claim(dir, jobs[0].ID); !errors.Is(err, ErrRan) {
		t.Fatalf("second claim = %v", err)
	}
	if err := Finish(dir, Result{ID: jobs[0].ID, Verb: VerbWake, Code: CodeRefused, Error: "nhm does not own this server"}); err != nil {
		t.Fatal(err)
	}
	if err := Claim(dir, jobs[1].ID); err != nil {
		t.Fatal(err)
	}
	list, err := List(dir, 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if list[0].Job.ID != jobs[2].ID || list[0].Claimed || list[0].Result != nil {
		t.Fatalf("newest = %+v", list[0])
	}
	if !list[1].Claimed || list[1].Result != nil {
		t.Fatalf("running = %+v", list[1])
	}
	if !list[2].Claimed || list[2].Result == nil || list[2].Result.Code != CodeRefused {
		t.Fatalf("ended = %+v", list[2])
	}
	// List reads the newest jobs by name, no more than its limit (the
	// review's L3): with limit 1, the newest job comes back, also beside
	// an old file it cannot read.
	if err := os.WriteFile(filepath.Join(dir, "op-1000000000000-old.job.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if short, _ := List(dir, 1); len(short) != 1 || short[0].Job.ID != jobs[2].ID {
		t.Fatalf("limit 1 = %+v", short)
	}
	// The order is the files' write time, not the ids' time: a job whose
	// id is old (a review left open) but that was submitted last comes
	// first (the re-check's low 9).
	late := testJob(t, VerbWake, WakeRequest{Recipients: []ui.Target{{BindingID: "b1", Generation: 1}}}, t0.Add(-time.Hour))
	if err := write(dir, late); err != nil {
		t.Fatal(err)
	}
	for i, j := range append(jobs, late) {
		at := t0.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(JobPath(dir, j.ID), at, at); err != nil {
			t.Fatal(err)
		}
	}
	if short, _ := List(dir, 1); len(short) != 1 || short[0].Job.ID != late.ID {
		t.Fatalf("limit 1 after a late submit = %+v", short)
	}
	if _, err := Load(filepath.Join(dir, "other.job.json")); err == nil {
		t.Fatal("a missing job loaded")
	}
	// A job file under another name is refused.
	data, _ := os.ReadFile(JobPath(dir, jobs[1].ID))
	renamed := filepath.Join(dir, "op-1-x.job.json")
	if err := os.WriteFile(renamed, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(renamed); err == nil {
		t.Fatal("a job under another id loaded")
	}
}
