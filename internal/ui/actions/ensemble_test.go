package actions

import (
	"context"
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/core/ensemble"
)

// stop's review reads the run; a stopped run has nothing to confirm; the
// stop runs under the review's id.
func TestEnsembleStop(t *testing.T) {
	type call struct {
		run    string
		dryRun bool
		opID   string
	}
	var calls []call
	status := ensemble.RunRunning
	core := Core{EnsembleStop: func(_ context.Context, run string, dryRun bool, opID string) (ensemble.Run, error) {
		calls = append(calls, call{run, dryRun, opID})
		r := ensemble.Run{ID: "run-1", Preset: "triad", Session: "s1", Status: status,
			Angles: []ensemble.Angle{{Index: 0, Mode: "skeptic", MailName: "IcyKnoll", PromptStatus: "submitted"}}}
		if !dryRun {
			r.Status = ensemble.RunStopped
		}
		return r, nil
	}}
	stop := mustAction(t, "ensemble.stop")
	do := func(p map[string]string) (Result, error) {
		return stop.Do(context.Background(), core, Request{Context: ctxKill, OperationID: "op-5", Params: p})
	}
	if _, err := do(map[string]string{}); err == nil {
		t.Fatal("a stop with no run was accepted")
	}
	res, err := do(map[string]string{ParamRun: "s1", ParamDryRun: "1"})
	if err != nil || res.Status != "reviewed" || res.Targets[0].Name != "IcyKnoll" || !calls[0].dryRun {
		t.Fatalf("review = %+v %v %+v", res, err, calls)
	}
	res, err = do(map[string]string{ParamRun: "s1"})
	if err != nil || res.Status != ensemble.RunStopped || calls[1].dryRun || calls[1].opID != "op-5" || calls[1].run != "s1" {
		t.Fatalf("stop = %+v %v %+v", res, err, calls)
	}
	status = ensemble.RunStopped
	if res, err := do(map[string]string{ParamRun: "s1", ParamDryRun: "1"}); !errors.Is(err, ErrRunStopped) || res.Status != "refused" {
		t.Fatalf("review of a stopped run = %+v %v", res, err)
	}
}
