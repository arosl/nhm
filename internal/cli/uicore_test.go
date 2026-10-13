package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
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
[ "$FAKE_CODE" = none ] && exit 0
printf '{"version":1,"id":"%s","verb":"x","code":%s,"error":"%s","unknown":%s,"result":%s}\n' "$id" "$FAKE_CODE" "$FAKE_ERROR" "${FAKE_UNKNOWN:-false}" "$FAKE_RESULT" > "$dir/$id.tmp"
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

// A job whose answer does not come in is unknown under its id; a job that
// did not start has none: nothing ran.
func TestUIJobLostAndNotStarted(t *testing.T) {
	jobs := fakeJobs(t, "/run/herdr.sock", "none", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	res, err := uiRun(ctx, jobs, runner.VerbSend, "op-lost", send.Request{}, nil, func(r *send.Result) *string { return &r.OperationID })
	var lost *uiLostError
	if !errors.As(err, &lost) || res.OperationID != "op-lost" || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, actions.ErrNoAnswer) {
		t.Fatalf("lost answer = %+v %v", res, err)
	}
	jobs.Bin = filepath.Join(t.TempDir(), "missing")
	res, err = uiRun(context.Background(), jobs, runner.VerbSend, "op-gone", send.Request{}, nil, func(r *send.Result) *string { return &r.OperationID })
	if err == nil || errors.As(err, &lost) || errors.Is(err, actions.ErrNoAnswer) || res.OperationID != "" {
		t.Fatalf("a job that did not start = %+v %v", res, err)
	}
}

// A job that stopped after its claim (a panic or a signal) is unknown
// under its id, never "nothing ran" (the palette review's row 4).
func TestUIJobStopped(t *testing.T) {
	jobs := fakeJobs(t, "/run/herdr.sock", "1", "send stopped on an internal error", "null")
	jobs.Env = append(jobs.Env, "FAKE_UNKNOWN=true")
	res, err := uiRun(context.Background(), jobs, runner.VerbSend, "op-stop", send.Request{}, nil, func(r *send.Result) *string { return &r.OperationID })
	var lost *uiLostError
	if !errors.As(err, &lost) || res.OperationID != "op-stop" || !errors.Is(err, actions.ErrNoAnswer) ||
		!strings.Contains(err.Error(), "internal error") {
		t.Fatalf("stopped job = %+v %v", res, err)
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
	// kill's review runs here and refuses: BlueLake works.
	kres, err := core.Kill(ctx, lifecycle.KillRequest{WorkspaceID: "w1"})
	if !errors.Is(err, lifecycle.ErrRefused) || len(kres.Refusals) == 0 {
		t.Fatalf("kill review = %+v %v", kres, err)
	}
	if _, err := core.Kill(ctx, lifecycle.KillRequest{WorkspaceID: "w1", Yes: true, NoBeads: true, OperationID: "op-ui-3"}); err != nil {
		t.Fatal(err)
	}
	var kr runner.KillRequest
	if j := seenJob(t, jobs, "op-ui-3"); j.Verb != runner.VerbKill || j.Decode(&kr) != nil || kr != (runner.KillRequest{WorkspaceID: "w1", NoBeads: true}) {
		t.Fatalf("kill job = %+v %+v", j, kr)
	}
	// reap's dry run runs here; the sweep is a job with exactly the set.
	items, err := core.Reap(ctx, lifecycle.ReapRequest{WorkspaceID: "w1", Idle: time.Minute, DryRun: true}, "op-ui-x")
	if err != nil || len(items) != 2 {
		t.Fatalf("reap review = %+v %v", items, err)
	}
	if _, err := os.Stat(filepath.Join(jobs.JobDir, "op-ui-x.seen")); err == nil {
		t.Fatal("reap's dry run started a job")
	}
	only := []lifecycle.ReapTarget{{BindingID: icy.ID, Generation: icy.Generation}}
	if _, err := core.Reap(ctx, lifecycle.ReapRequest{WorkspaceID: "w1", Idle: time.Minute, Only: only}, "op-ui-4"); err != nil {
		t.Fatal(err)
	}
	var rr runner.ReapRequest
	if j := seenJob(t, jobs, "op-ui-4"); j.Verb != runner.VerbReap || j.Decode(&rr) != nil || len(rr.Only) != 1 || rr.Only[0] != only[0] {
		t.Fatalf("reap job = %+v %+v", j, rr)
	}
	if _, err := core.New(ctx, "fresh", "/data/p", "op-ui-5"); err != nil || seenJob(t, jobs, "op-ui-5").Verb != runner.VerbNew {
		t.Fatalf("new job: %v", err)
	}
	if _, err := core.Wake(ctx, []ui.Target{{BindingID: icy.ID, Generation: icy.Generation}}, "op-ui-6"); err != nil ||
		seenJob(t, jobs, "op-ui-6").Verb != runner.VerbWake {
		t.Fatalf("wake job: %v", err)
	}
	if _, err := core.Spawn(ctx, actions.SpawnRequest{Session: "w1", Add: true, Agents: []spawn.Want{{Kind: "claude", Count: 1}},
		Memory: true}, "op-ui-7"); err != nil {
		t.Fatal(err)
	}
	var sr runner.SpawnRequest
	if j := seenJob(t, jobs, "op-ui-7"); j.Verb != runner.VerbSpawn || j.Decode(&sr) != nil || !sr.Add || !sr.Memory || sr.Agents[0].Count != 1 {
		t.Fatalf("spawn job = %+v %+v", j, sr)
	}
	if _, err := core.Operation(ctx, "op-none"); !errors.Is(err, lifecycle.ErrNoOperation) {
		t.Fatalf("a missing operation = %v", err)
	}
	if _, err := core.Adopt(ctx, bindings.AdoptRequest{MailName: "GreenPike", PaneID: "w1:p9", ExpectedSession: "claude:s9",
		OperationID: "op-ui-8"}); err != nil {
		t.Fatal(err)
	}
	var ar bindings.AdoptRequest
	if j := seenJob(t, jobs, "op-ui-8"); j.Verb != runner.VerbAdopt || j.Decode(&ar) != nil || ar.ExpectedSession != "claude:s9" || ar.PaneID != "w1:p9" {
		t.Fatalf("adopt job = %+v %+v", j, ar)
	}
	// zoom finds a bound agent by its binding and refuses one at another
	// generation; it runs in the surface.
	var herdrCalls []string
	for _, name := range []string{"workspace.focus", "pane.zoom"} {
		name := name
		s.Handle(name, func(r herdrtest.Request) (any, *herdrtest.Error) {
			herdrCalls = append(herdrCalls, name+" "+string(r.Params))
			if name == "pane.zoom" {
				return map[string]any{"type": "pane_zoom", "zoom": map[string]any{"pane_id": "w1:p1", "zoomed": true}}, nil
			}
			return map[string]any{"type": "ok"}, nil
		})
	}
	stale := ui.Target{BindingID: icy.ID, Generation: icy.Generation + 1, PaneID: "w1:p1"}
	if _, err := core.Zoom(ctx, stale); err == nil || !strings.Contains(err.Error(), "gone") || len(herdrCalls) != 0 {
		t.Fatalf("zoom of a stale target = %v %v", err, herdrCalls)
	}
	if pane, err := core.Zoom(ctx, ui.Target{BindingID: icy.ID, Generation: icy.Generation}); err != nil || pane != "w1:p1" ||
		len(herdrCalls) != 2 || !strings.Contains(herdrCalls[0], `"w1"`) {
		t.Fatalf("zoom = %q %v %v", pane, err, herdrCalls)
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
