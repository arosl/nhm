package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// writeJob writes a job file as runner.Start does, without starting it.
func writeJob(t *testing.T, verb, socket, dir string, req any) (runner.Job, string) {
	t.Helper()
	j, err := runner.NewJob(verb, ui.Context{SocketPath: socket, Cwd: dir}, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	path := runner.JobPath(t.TempDir(), j.ID)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return j, path
}

func jobResult(t *testing.T, path string, j runner.Job) runner.Result {
	t.Helper()
	r, ok, err := runner.Read(filepath.Dir(path), j.ID)
	if err != nil || !ok {
		t.Fatalf("no result for %s: %v", j.ID, err)
	}
	return r
}

// A job runs once, in its directory; a second run is refused and leaves
// the first result.
func TestUIRunRunsAJobOnce(t *testing.T) {
	t.Chdir(t.TempDir())
	s, inst, _ := newSendInstance(t)
	var created []string
	s.Handle("workspace.create", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Label string `json:"label"`
			Cwd   string `json:"cwd"`
		}
		_ = json.Unmarshal(r.Params, &p)
		created = append(created, p.Label+"@"+p.Cwd)
		return map[string]any{"type": "workspace_created",
			"workspace": map[string]any{"workspace_id": "w7", "number": 7, "label": p.Label, "focused": false, "pane_count": 1, "tab_count": 1, "active_tab_id": "w7:t1", "agent_status": "unknown"},
			"tab":       map[string]any{"tab_id": "w7:t1", "workspace_id": "w7", "number": 1, "label": "1", "focused": true, "pane_count": 1, "agent_status": "unknown"},
			"root_pane": map[string]any{"pane_id": "w7:p1", "tab_id": "w7:t1", "workspace_id": "w7", "terminal_id": "t", "agent_status": "unknown", "focused": true, "revision": 1}}, nil
	})
	ownNhm(t, inst)
	project := t.TempDir()
	j, path := writeJob(t, runner.VerbNew, s.SocketPath, project, runner.NewRequest{Label: "fresh", Project: project})
	if code, out, errOut := run("ui", "run", path); code != 0 {
		t.Fatalf("ui run = %d %s %s", code, out, errOut)
	}
	r := jobResult(t, path, j)
	var res newResult
	if r.Code != 0 || r.Error != "" || json.Unmarshal(r.Result, &res) != nil || res.WorkspaceID != "w7" || len(created) != 1 {
		t.Fatalf("result = %+v %s %v", r, r.Result, created)
	}
	if wd, _ := os.Getwd(); wd != project {
		t.Fatalf("the job ran in %s", wd)
	}
	if code, _, errOut := run("ui", "run", path); code != 2 || !strings.Contains(errOut, "ran before") || len(created) != 1 {
		t.Fatalf("second run = %d %s %v", code, errOut, created)
	}
	if again := jobResult(t, path, j); again.Code != 0 || !again.EndedAt.Equal(r.EndedAt) {
		t.Fatalf("the second run changed the result: %+v", again)
	}
}

// No job reads a missing selection as "all".
func TestUIRunRefusesAnEmptySelection(t *testing.T) {
	t.Chdir(t.TempDir())
	s, inst, _ := newSendInstance(t)
	ownNhm(t, inst)
	dir := t.TempDir()
	reap, reapPath := writeJob(t, runner.VerbReap, s.SocketPath, dir, runner.ReapRequest{WorkspaceID: "w1", Idle: time.Minute})
	if code, _, _ := run("ui", "run", reapPath); code != 2 {
		t.Fatalf("reap with no set = %d", code)
	}
	if r := jobResult(t, reapPath, reap); r.Code != 2 || !strings.Contains(r.Error, "reviewed set is empty") {
		t.Fatalf("reap result = %+v", r)
	}
	wake, wakePath := writeJob(t, runner.VerbWake, s.SocketPath, dir, runner.WakeRequest{})
	if code, _, _ := run("ui", "run", wakePath); code != 2 {
		t.Fatalf("wake with no recipient = %d", code)
	}
	if r := jobResult(t, wakePath, wake); r.Code != 2 || !strings.Contains(r.Error, "no recipient") {
		t.Fatalf("wake result = %+v", r)
	}
	odd, oddPath := writeJob(t, "takeover", s.SocketPath, dir, struct{}{})
	if code, _, _ := run("ui", "run", oddPath); code != 2 {
		t.Fatalf("unknown verb = %d", code)
	}
	if r := jobResult(t, oddPath, odd); r.Code != 2 || !strings.Contains(r.Error, `no "takeover" job`) {
		t.Fatalf("unknown verb result = %+v", r)
	}
}

// The per-agent jobs run the handler's request; a dry run or an empty
// interrupt is no job.
func TestUIRunAgentJobs(t *testing.T) {
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
	ownNhm(t, inst)
	dir := t.TempDir()
	var blue string
	var gen int64
	for _, b := range bs {
		if b.MailName == "BlueLake" {
			blue, gen = b.ID, b.Generation
		}
	}
	j, path := writeJob(t, runner.VerbRetire, s.SocketPath, dir, lifecycle.RetireRequest{Target: blue, ExpectedGeneration: gen, Reason: "retire"})
	if code, _, _ := run("ui", "run", path); code != 2 {
		t.Fatalf("retire of a working agent = %d", code)
	}
	r := jobResult(t, path, j)
	var res lifecycle.RetireResult
	if json.Unmarshal(r.Result, &res) != nil || len(res.Refusals) == 0 || !strings.Contains(r.Error, "refused") {
		t.Fatalf("retire result = %+v %s", r, r.Result)
	}
	for _, c := range []struct {
		verb string
		req  any
	}{
		{runner.VerbRetire, lifecycle.RetireRequest{Target: blue, DryRun: true}},
		{runner.VerbInterrupt, runner.InterruptRequest{}},
		{runner.VerbAdopt, bindings.AdoptRequest{MailName: "GreenPike"}},
		{runner.VerbEnsembleStop, runner.EnsembleStopRequest{Run: "no-such-run"}},
	} {
		j, path := writeJob(t, c.verb, s.SocketPath, dir, c.req)
		if code, _, _ := run("ui", "run", path); code != 2 {
			t.Errorf("%s %+v = %d", c.verb, c.req, code)
		}
		if r := jobResult(t, path, j); r.Code != 2 || r.Error == "" {
			t.Errorf("%s result = %+v", c.verb, r)
		}
	}
}

// A kill job runs kill's checks again: a refusal ends it before any
// record, and the result says why.
func TestUIRunKillRefusal(t *testing.T) {
	t.Chdir(t.TempDir())
	s, inst, _ := waitInstance(t, func(pane string) map[string]any {
		if pane == "w1:p1" {
			return agentMap(pane, "claude", "idle", 4, 4)
		}
		return agentMap(pane, "codex", "working", 7, 0)
	})
	t.Setenv("HOME", t.TempDir())
	old := lifecycleFacts
	lifecycleFacts = cliFacts{}
	t.Cleanup(func() { lifecycleFacts = old })
	ownNhm(t, inst)
	j, path := writeJob(t, runner.VerbKill, s.SocketPath, t.TempDir(), runner.KillRequest{WorkspaceID: "w1"})
	if code, _, _ := run("ui", "run", path); code != 2 {
		t.Fatalf("kill job = %d", code)
	}
	r := jobResult(t, path, j)
	var res lifecycle.KillResult
	if r.Code != 2 || json.Unmarshal(r.Result, &res) != nil || len(res.Refusals) == 0 || res.OperationID != "" {
		t.Fatalf("kill result = %+v %s", r, r.Result)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if ops, err := lifecycle.RecentOperations(context.Background(), db, 0); err != nil || len(ops) != 0 {
		t.Fatalf("a refused kill job left %+v %v", ops, err)
	}
}
