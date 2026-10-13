package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
)

// wsCore records what the workspace handlers asked the core for.
type wsCore struct {
	kills   []lifecycle.KillRequest
	killRes lifecycle.KillResult
	reaps   []lifecycle.ReapRequest
	reapRes []lifecycle.ReapItem
	woken   []ui.Target
	wakeRep wake.Report
	created []string
	spawns  []SpawnRequest
	spawnR  spawn.Result
	opIDs   []string
	err     error
}

func (w *wsCore) core() Core {
	return Core{
		Kill: func(_ context.Context, r lifecycle.KillRequest) (lifecycle.KillResult, error) {
			w.kills = append(w.kills, r)
			return w.killRes, w.err
		},
		Reap: func(_ context.Context, r lifecycle.ReapRequest, op string) ([]lifecycle.ReapItem, error) {
			w.reaps, w.opIDs = append(w.reaps, r), append(w.opIDs, op)
			return w.reapRes, w.err
		},
		Wake: func(_ context.Context, ts []ui.Target, op string) (wake.Report, error) {
			w.woken, w.opIDs = ts, append(w.opIDs, op)
			return w.wakeRep, w.err
		},
		New: func(_ context.Context, label, project, op string) (string, error) {
			w.created, w.opIDs = append(w.created, label+"@"+project), append(w.opIDs, op)
			return "w9", w.err
		},
		Spawn: func(_ context.Context, r SpawnRequest, op string) (spawn.Result, error) {
			w.spawns, w.opIDs = append(w.spawns, r), append(w.opIDs, op)
			return w.spawnR, w.err
		},
	}
}

var ctxKill = ui.Context{SocketPath: "/run/herdr.sock", WorkspaceID: "w1", WorkspaceLabel: "proj-a", Cwd: "/data/p"}

func do(t *testing.T, id string, core Core, req Request) (Result, error) {
	t.Helper()
	if req.OperationID == "" {
		req.OperationID = "op-1"
	}
	return mustAction(t, id).Do(context.Background(), core, req)
}

func TestKillReviewThenTypedLabel(t *testing.T) {
	w := &wsCore{killRes: lifecycle.KillResult{WorkspaceID: "w1", Refusals: []string{},
		Agents: []lifecycle.RetireResult{{MailName: "IcyKnoll", BindingID: "b1", DryRun: true, Actions: []string{"close pane w1:p1"}}}},
		err: fmt.Errorf("%w: give --yes", lifecycle.ErrRefused)}
	res, err := do(t, "workspace.kill", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamDryRun: "1"}})
	if err != nil || res.Status != "checked" || len(res.Targets) != 1 || res.Targets[0].Status != "checked" {
		t.Fatalf("review = %+v %v", res, err)
	}
	if k := w.kills[0]; k.Yes || k.OperationID != "" || k.WorkspaceID != "w1" {
		t.Fatalf("the review asked %+v", k)
	}
	// A wrong label reaches no core.
	if _, err := do(t, "workspace.kill", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamConfirm: "proj-b"}}); !errors.Is(err, ErrConfirm) || len(w.kills) != 1 {
		t.Fatalf("wrong label = %v (%d kills)", err, len(w.kills))
	}
	w.killRes, w.err = lifecycle.KillResult{OperationID: "op-7", Closed: true, Refusals: []string{}}, nil
	res, err = do(t, "workspace.kill", w.core(), Request{Context: ctxKill, OperationID: "op-7",
		Params: map[string]string{ParamConfirm: " proj-a ", ParamNoBeads: "1"}})
	if err != nil || res.Status != "closed" {
		t.Fatalf("kill = %+v %v", res, err)
	}
	if k := w.kills[1]; !k.Yes || k.OperationID != "op-7" || !k.NoBeads {
		t.Fatalf("the kill asked %+v", k)
	}
	// A review that refuses says why.
	w.killRes, w.err = lifecycle.KillResult{Refusals: []string{"BlueLake: it is working"}}, fmt.Errorf("%w: BlueLake", lifecycle.ErrRefused)
	if res, err := do(t, "workspace.kill", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamDryRun: "1"}}); err == nil ||
		res.Status != "refused" || res.Lines[0] != "BlueLake: it is working" {
		t.Fatalf("refused review = %+v %v", res, err)
	}
	if KillLabel(ui.Context{WorkspaceID: "w4", WorkspaceLabel: " "}) != "w4" {
		t.Fatal("a blank label is not the workspace id")
	}
}

func TestReapReviewThenExactlyTheSet(t *testing.T) {
	w := &wsCore{reapRes: []lifecycle.ReapItem{{MailName: "IcyKnoll", BindingID: "b1", Generation: 3, Action: "would_retire"},
		{MailName: "BlueLake", BindingID: "b2", Generation: 1, Action: "kept", Reason: "the keep list holds it"}}}
	res, err := do(t, "workspace.reap", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamDryRun: "1", ParamIdle: "90"}})
	if err != nil || res.Status != "reviewed" || res.Targets[0].Target.Generation != 3 || res.Targets[0].Status != "would_retire" {
		t.Fatalf("review = %+v %v", res, err)
	}
	if r := w.reaps[0]; !r.DryRun || r.Idle != 90*time.Minute || r.Only != nil || r.WorkspaceID != "w1" {
		t.Fatalf("the review asked %+v", r)
	}
	if _, err := do(t, "workspace.reap", w.core(), Request{Context: ctxKill}); !errors.Is(err, ErrNothingToReap) || len(w.reaps) != 1 {
		t.Fatalf("a confirm with no set = %v", err)
	}
	if _, err := do(t, "workspace.reap", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamOnly: "b1@x"}}); !errors.Is(err, ErrTargets) {
		t.Fatalf("a malformed set = %v", err)
	}
	set := []lifecycle.ReapTarget{{BindingID: "b1", Generation: 3}, {BindingID: "b3", Generation: 7}}
	w.reapRes = []lifecycle.ReapItem{{MailName: "IcyKnoll", BindingID: "b1", Action: "retired"},
		{MailName: "RedStone", BindingID: "b3", Action: "refused", Reason: "it changed since it was reviewed"}}
	res, err = do(t, "workspace.reap", w.core(), Request{Context: ctxKill, OperationID: "op-9", Params: map[string]string{ParamOnly: OnlyParam(set)}})
	if err != nil || res.Status != "partial" || res.Targets[1].Reason != "it changed since it was reviewed" {
		t.Fatalf("reap = %+v %v", res, err)
	}
	if r := w.reaps[1]; r.DryRun || len(r.Only) != 2 || r.Only[1] != set[1] || r.Idle != DefaultIdle || w.opIDs[1] != "op-9" {
		t.Fatalf("the reap asked %+v %v", r, w.opIDs)
	}
}

func TestWakeNewAndSpawn(t *testing.T) {
	b1 := ui.Target{BindingID: "b1", Generation: 2}
	w := &wsCore{wakeRep: wake.Report{Decisions: []wake.Decision{{BindingID: "b1", MailName: "IcyKnoll", Action: wake.ActionNudged,
		Outcome: "submitted"}}, Unbound: []string{"Ghost"}}}
	res, err := do(t, "workspace.wake", w.core(), Request{Context: ctxKill, Targets: []ui.Target{b1}})
	if err != nil || res.Status != "nudged" || res.Targets[0].Target != b1 || res.Targets[0].Status != "nudged (submitted)" ||
		!strings.Contains(res.Lines[0], "Ghost") || w.woken[0] != b1 {
		t.Fatalf("wake = %+v %v", res, err)
	}
	if _, err := do(t, "workspace.wake", w.core(), Request{Context: ctxKill}); !errors.Is(err, ui.ErrEmptySelection) {
		t.Fatalf("a wake with no agent = %v", err)
	}
	// new: the label is checked; the project is the context's directory.
	for _, bad := range []map[string]string{{}, {ParamLabel: "-x"}, {ParamLabel: "a\x1bb"}, {ParamLabel: "p", ParamProject: "rel"}} {
		if _, err := do(t, "workspace.new", w.core(), Request{Context: ctxKill, Params: bad}); err == nil {
			t.Errorf("new %v was accepted", bad)
		}
	}
	res, err = do(t, "workspace.new", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamLabel: " fresh "}})
	if err != nil || res.Status != "created" || len(w.created) != 1 || w.created[0] != "fresh@/data/p" {
		t.Fatalf("new = %+v %v %v", res, err, w.created)
	}
	// spawn and add keep their difference.
	param, err := SpawnParam(SpawnRequest{Session: "ignored", Agents: []spawn.Want{{Kind: "claude", Count: 2}}, Memory: true})
	if err != nil {
		t.Fatal(err)
	}
	w.spawnR = spawn.Result{Admission: spawn.Admission{Decision: spawn.Admit}, Targets: []spawn.Target{{Kind: "claude", Status: "planned"}}}
	res, err = do(t, "spawn.add", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamSpawn: param, ParamDryRun: "1"}})
	if err != nil || res.Status != "admitted" || w.spawns[0].Session != "w1" || !w.spawns[0].Add || !w.spawns[0].DryRun || !w.spawns[0].Memory {
		t.Fatalf("add's review = %+v %v %+v", res, err, w.spawns)
	}
	if _, err := do(t, "spawn.new", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamSpawn: param}}); err != nil ||
		w.spawns[1].Session != "ignored" || w.spawns[1].Add || w.spawns[1].DryRun {
		t.Fatalf("spawn = %v %+v", err, w.spawns[1])
	}
	empty, _ := SpawnParam(SpawnRequest{Session: "s", Agents: []spawn.Want{{Kind: "claude", Count: 0}}})
	if _, err := do(t, "spawn.new", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamSpawn: empty}}); !errors.Is(err, ErrNothingToSpawn) {
		t.Fatalf("an empty spawn = %v", err)
	}
	w.spawnR = spawn.Result{Admission: spawn.Admission{Decision: spawn.Refuse, Reasons: []string{"claude: quota at 95%"}}}
	w.err = spawn.ErrRefused
	if res, err := do(t, "spawn.add", w.core(), Request{Context: ctxKill, Params: map[string]string{ParamSpawn: param}}); err == nil ||
		res.Status != "refused" || !strings.Contains(strings.Join(res.Lines, "\n"), "quota at 95%") {
		t.Fatalf("a refused admission = %+v %v", res, err)
	}
}
