package forms

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui/runner"
)

type fakeOps struct {
	answers [][]lifecycle.RecentOperation
	jobs    []runner.Entry
	reads   int
}

func (f *fakeOps) RecentOperations(_ context.Context, limit int) ([]lifecycle.RecentOperation, error) {
	f.reads++
	ops := f.answers[0]
	if len(f.answers) > 1 {
		f.answers = f.answers[1:]
	}
	return ops, nil
}

func (f *fakeOps) RecentJobs(int) ([]runner.Entry, error) { return f.jobs, nil }

func TestOperationsRows(t *testing.T) {
	now := time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)
	done := now.Add(-time.Minute)
	ops := []lifecycle.RecentOperation{
		{OperationID: "op-3", Verb: lifecycle.VerbKill, Status: lifecycle.KillStopped, Scope: json.RawMessage(`{"workspace_id":"w1"}`),
			CreatedAt: now.Add(-3 * time.Minute), UpdatedAt: done, Completed: &done, Targets: []lifecycle.OperationTarget{
				{Index: 0, BindingID: "b1", Generation: 2, Status: lifecycle.KillRetired, Result: json.RawMessage(`{"mail_name":"IcyKnoll"}`)},
				{Index: 1, BindingID: "b2", Generation: 1, Status: lifecycle.KillRefused,
					Result: json.RawMessage(`{"mail_name":"Blue\u001b[31mLake","refusals":["it is working"],"error":"refused"}`)},
				{Index: 2, BindingID: "b3", Status: "pending"},
			}},
		{OperationID: "op-2", Verb: "send", Status: "dispatching", CreatedAt: now.Add(-4 * time.Minute), UpdatedAt: now.Add(-4 * time.Minute)},
		{OperationID: "op-1", Verb: "retire", Status: "claimed", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{OperationID: "op-k", Verb: lifecycle.VerbKill, Status: lifecycle.KillDispatching, CreatedAt: now.Add(-5 * time.Minute),
			UpdatedAt: now.Add(-5 * time.Minute)},
	}
	ended := runner.Result{ID: "op-k", Code: runner.CodeError, Error: "kill stopped on an internal error", EndedAt: now.Add(-4 * time.Minute)}
	refused := runner.Result{ID: "op-n", Code: runner.CodeRefused, Error: "workspace fresh exists (w2)", EndedAt: now.Add(-time.Minute)}
	jobs := []runner.Entry{
		{Job: runner.Job{ID: "op-r", Verb: runner.VerbReap, CreatedAt: now.Add(-time.Minute), Request: json.RawMessage(`{"workspace_id":"w1"}`)}, Claimed: true},
		{Job: runner.Job{ID: "op-n", Verb: runner.VerbNew, CreatedAt: now.Add(-2 * time.Minute)}, Claimed: true, Result: &refused},
		{Job: runner.Job{ID: "op-k", Verb: runner.VerbKill, CreatedAt: now.Add(-5 * time.Minute)}, Claimed: true, Result: &ended},
	}
	v := NewOperationsView(&fakeOps{answers: [][]lifecycle.RecentOperation{ops}, jobs: jobs}, 0, func() time.Time { return now })
	if tick := v.Update(do(t, v.Init())); tick == nil {
		t.Fatal("no refresh tick after a read")
	}
	rows := v.Rows()
	ids := ""
	for _, r := range rows {
		ids += r.ID + " "
	}
	if ids != "op-r op-n op-3 op-2 op-k op-1 " {
		t.Fatalf("order = %s", ids)
	}
	byID := map[string]OperationRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if r := byID["op-3"]; r.State != OpFinal || r.Workspace != "w1" || len(r.Targets) != 3 {
		t.Fatalf("kill = %+v", r)
	}
	if byID["op-2"].State != OpOpen || byID["op-1"].State != OpQuiet {
		t.Fatalf("open and quiet = %+v %+v", byID["op-2"], byID["op-1"])
	}
	// A record with no final outcome whose job ended is unknown.
	if r := byID["op-k"]; r.State != OpUnknown || r.Detail != ended.Error || r.Job == nil {
		t.Fatalf("lost kill = %+v", r)
	}
	// Jobs with no record of their own get a row.
	if r := byID["op-r"]; r.Verb != "reap" || r.Status != "running" || r.State != OpOpen || r.Workspace != "w1" {
		t.Fatalf("running reap = %+v", r)
	}
	if r := byID["op-n"]; r.Status != "refused" || r.State != OpFinal || r.Detail != refused.Error {
		t.Fatalf("refused new = %+v", r)
	}
	tg := byID["op-3"].Targets
	if tg[0].Name != "IcyKnoll" || tg[2].Name != "b3" {
		t.Fatalf("targets = %+v", tg)
	}
	// Terminal controls in stored text are removed.
	if tg[1].Name != "BlueLake" || tg[1].Detail != "it is working; refused" || tg[1].Status != lifecycle.KillRefused {
		t.Fatalf("refused target = %+v", tg[1])
	}
	if v.Selected() != "op-r" {
		t.Fatalf("selected = %q", v.Selected())
	}
}

// The selection follows the operation when a newer one arrives; a stale
// tick is dropped; one read runs at a time.
func TestOperationsRefresh(t *testing.T) {
	now := time.Now()
	a := lifecycle.RecentOperation{OperationID: "op-a", Verb: "send", CreatedAt: now.Add(-2 * time.Second), UpdatedAt: now}
	b := lifecycle.RecentOperation{OperationID: "op-b", Verb: "send", CreatedAt: now.Add(-time.Second), UpdatedAt: now}
	c := lifecycle.RecentOperation{OperationID: "op-c", Verb: "kill_workspace", CreatedAt: now, UpdatedAt: now}
	src := &fakeOps{answers: [][]lifecycle.RecentOperation{{b, a}, {c, b, a}}}
	v := NewOperationsView(src, 20, nil)
	first := v.Init()
	if v.Refresh() != nil {
		t.Fatal("a second read started while one runs")
	}
	v.Update(do(t, first))
	v.Move(1)
	if v.Selected() != "op-a" {
		t.Fatalf("selected = %q", v.Selected())
	}
	oldTick := opsTick{request: v.request}
	second := v.Update(oldTick)
	if second == nil {
		t.Fatal("the tick did not read")
	}
	if v.Update(oldTick) != nil {
		t.Fatal("a stale tick read again")
	}
	v.Update(do(t, second))
	if v.Selected() != "op-a" || len(v.Rows()) != 3 || v.Rows()[0].ID != "op-c" {
		t.Fatalf("after the refresh: %q %+v", v.Selected(), v.Rows())
	}
	v.Move(-5)
	if v.Selected() != "op-c" || src.reads != 2 {
		t.Fatalf("selected = %q, reads %d", v.Selected(), src.reads)
	}
}
