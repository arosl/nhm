package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// fakeJobs is a runner client whose "nhm ui run" is a script: it keeps a
// copy of the job it got (<id>.seen) and ends it with FAKE_CODE,
// FAKE_ERROR and FAKE_RESULT.
func fakeJobs(t *testing.T, sock string, code, msg, result string) runner.Client {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "nhm")
	script := `#!/bin/sh
job="$3"; dir=$(dirname "$job"); id=$(basename "$job" .job.json)
cp "$job" "$dir/$id.seen"
printf '{"version":1,"id":"%s","verb":"x","code":%s,"error":"%s","result":%s}\n' "$id" "$FAKE_CODE" "$FAKE_ERROR" "$FAKE_RESULT" > "$dir/$id.tmp"
mv "$dir/$id.tmp" "$dir/$id.result.json"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return runner.Client{Bin: bin, JobDir: t.TempDir(), Context: ui.Context{SocketPath: sock, Cwd: t.TempDir()},
		Env: append(os.Environ(), "FAKE_CODE="+code, "FAKE_ERROR="+msg, "FAKE_RESULT="+result)}
}

func seenJob(t *testing.T, jobs runner.Client, id string) runner.Job {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(jobs.JobDir, id+".seen"))
	if err != nil {
		t.Fatal(err)
	}
	var j runner.Job
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatal(err)
	}
	return j
}

// A job's refusal comes back as the core's refusal, its result decoded.
func TestUIJobErrors(t *testing.T) {
	ctx := context.Background()
	jobs := fakeJobs(t, "/run/herdr.sock", "2", "refused: BlueLake is working", `{"mail_name":"BlueLake","refusals":["BlueLake is working"]}`)
	var res lifecycle.RetireResult
	err := uiJob(ctx, jobs, runner.VerbRetire, "op-1", lifecycle.RetireRequest{Target: "b2"}, &res, lifecycle.ErrRefused)
	if !errors.Is(err, lifecycle.ErrRefused) || len(res.Refusals) != 1 || err.Error() != "refused: BlueLake is working" {
		t.Fatalf("refusal = %v %+v", err, res)
	}
	jobs = fakeJobs(t, "/run/herdr.sock", "1", "herdr: socket closed", "null")
	if err := uiJob(ctx, jobs, runner.VerbRetire, "op-1", lifecycle.RetireRequest{}, &res, lifecycle.ErrRefused); err == nil ||
		errors.Is(err, lifecycle.ErrRefused) {
		t.Fatalf("an error read as a refusal: %v", err)
	}
	// An interrupt that refused its targets reports it in its result.
	jobs = fakeJobs(t, "/run/herdr.sock", "2", "", `{"status":"refused"}`)
	var out map[string]any
	if err := uiJob(ctx, jobs, runner.VerbInterrupt, "op-1", runner.InterruptRequest{}, &out, nil); err != nil || out["status"] != "refused" {
		t.Fatalf("interrupt = %v %v", err, out)
	}
	if err := uiJob(ctx, jobs, runner.VerbRetire, "", lifecycle.RetireRequest{}, &res, nil); err == nil {
		t.Fatal("a mutation ran without its operation id")
	}
}

// A dry run runs in the surface; a mutation is a job under the guard's
// id, with the handler's request as it was built.
func TestUICoreRunsMutationsAsJobs(t *testing.T) {
	t.Chdir(t.TempDir())
	s, inst, bs := waitInstance(t, func(pane string) map[string]any {
		if pane == "w1:p1" {
			return agentMap(pane, "claude", "idle", 4, 4)
		}
		return agentMap(pane, "codex", "working", 7, 0)
	})
	t.Setenv("HOME", t.TempDir())
	old := lifecycleFacts
	lifecycleFacts = cliFacts{}
	t.Cleanup(func() { lifecycleFacts = old })
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ie.db.Close()
	ownNhm(t, inst)
	icy := bs[0]
	jobs := fakeJobs(t, s.SocketPath, "0", "", `{"mail_name":"IcyKnoll","done":true}`)
	core := uiCore(ie, jobs.Context, jobs)
	ctx := context.Background()
	res, err := core.Retire(ctx, lifecycle.RetireRequest{Target: icy.ID, ExpectedGeneration: icy.Generation, DryRun: true})
	if err != nil || !res.DryRun || len(res.Refusals) != 0 {
		t.Fatalf("dry run = %+v %v", res, err)
	}
	if entries, _ := runner.List(jobs.JobDir, 0); len(entries) != 0 {
		t.Fatalf("a dry run started a job: %+v", entries)
	}
	res, err = core.Retire(ctx, lifecycle.RetireRequest{Target: icy.ID, ExpectedGeneration: icy.Generation, OperationID: "op-ui-1",
		Reason: "retire"})
	if err != nil || !res.Done {
		t.Fatalf("retire = %+v %v", res, err)
	}
	j := seenJob(t, jobs, "op-ui-1")
	var req lifecycle.RetireRequest
	if j.Verb != runner.VerbRetire || j.Decode(&req) != nil || req.ExpectedGeneration != icy.Generation || req.Target != icy.ID {
		t.Fatalf("job = %+v %+v", j, req)
	}
	if _, err := core.Interrupt(ctx, map[string]int64{icy.ID: icy.Generation}, "op-ui-2"); err != nil {
		t.Fatal(err)
	}
	var ir runner.InterruptRequest
	if j := seenJob(t, jobs, "op-ui-2"); j.Verb != runner.VerbInterrupt || j.Decode(&ir) != nil || ir.Expect[icy.ID] != icy.Generation {
		t.Fatalf("interrupt job = %+v", j)
	}
	// The send base is nhm send's for the workspace, with no selector.
	if _, err := uiSendBase(ctx, ie, ui.Context{}); err == nil {
		t.Fatal("a send base without a workspace")
	}
	base, err := uiSendBase(ctx, ie, ui.Context{SocketPath: s.SocketPath, WorkspaceID: "w1"})
	if err != nil || base.WorkspaceID != "w1" || len(base.Selector.Panes) != 0 || !strings.Contains(string(mustJSON(t, base.Scope)), "w1") {
		t.Fatalf("send base = %+v %v", base, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
