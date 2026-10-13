package forms

import (
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// An empty pick is refused, never read as every agent.
func TestWakeRefusesNoRecipients(t *testing.T) {
	jobs := &fakeJobs{}
	f := NewWakeForm(jobs)
	f.SetRecipients([]ui.Target{})
	if f.Confirm() != nil || !errors.Is(f.Err(), ErrNoRecipients) || f.Phase() != Editing || len(jobs.started) != 0 {
		t.Fatalf("empty wake: %v %v", f.Phase(), f.Err())
	}
}

func TestWakeRunsOnceForThePickedAgents(t *testing.T) {
	jobs := &fakeJobs{}
	f := NewWakeForm(jobs)
	picked := []ui.Target{{BindingID: "b1", Generation: 2}, {BindingID: "b3", Generation: 1}}
	f.SetRecipients(picked)
	picked[0].BindingID = "changed"
	cmd := f.Confirm()
	if cmd == nil || f.Confirm() != nil {
		t.Fatal("wake did not submit once")
	}
	f.SetRecipients(nil)
	f.Update(do(t, cmd))
	req := jobs.started[0].req.(runner.WakeRequest)
	if jobs.started[0].verb != runner.VerbWake || len(req.Recipients) != 2 || req.Recipients[0].BindingID != "b1" {
		t.Fatalf("job = %+v", jobs.started)
	}
	jobs.end(t, "op-1", 0, "", map[string]any{"decisions": []wake.Decision{{BindingID: "b1", MailName: "IcyKnoll", Action: wake.ActionNudged},
		{BindingID: "b3", MailName: "RedStone", Action: wake.ActionSkipped, Reason: "it is working"}}, "possibly_submitted": []any{}})
	poll(t, f.Update, f.job.request)
	if f.Phase() != Done || len(f.Report().Decisions) != 2 || f.Report().Decisions[1].Reason != "it is working" {
		t.Fatalf("wake: %v %+v %v", f.Phase(), f.Report(), f.Err())
	}
	failed := NewWakeForm(&fakeJobs{startErr: errors.New("runner: the job has no project directory")})
	failed.SetRecipients(picked[1:])
	failed.Update(do(t, failed.Confirm()))
	if failed.Phase() != Failed || failed.Err() == nil {
		t.Fatalf("failed wake: %v", failed.Phase())
	}
}
