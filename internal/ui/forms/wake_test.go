package forms

import (
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/ui"
)

// An empty pick is refused, never read as every agent.
func TestWakeRefusesNoRecipients(t *testing.T) {
	fc := &fakeCore{}
	f := NewWakeForm(newRunner(t, "workspace.wake", fc.core()), ctxW)
	f.SetRecipients([]ui.Target{})
	if f.Confirm() != nil || !errors.Is(f.Err(), ErrNoRecipients) || f.Phase() != Editing || len(fc.woken) != 0 {
		t.Fatalf("empty wake: %v %v", f.Phase(), f.Err())
	}
}

func TestWakeRunsOnceForThePickedAgents(t *testing.T) {
	fc := &fakeCore{}
	f := NewWakeForm(newRunner(t, "workspace.wake", fc.core()), ctxW)
	picked := []ui.Target{{BindingID: "b1", Generation: 2}, {BindingID: "b3", Generation: 1}}
	f.SetRecipients(picked)
	picked[0].BindingID = "changed"
	cmd := f.Confirm()
	if cmd == nil || f.Confirm() != nil {
		t.Fatal("wake did not submit once")
	}
	f.SetRecipients(nil)
	f.Update(do(t, cmd))
	if f.Phase() != Done || len(fc.woken) != 1 || fc.woken[0][0].BindingID != "b1" || fc.wakeOps[0] != "op-1" ||
		len(f.Result().Targets) != 2 || f.Result().Status != "nudged" {
		t.Fatalf("wake: %v %+v %+v %v", f.Phase(), fc.woken, f.Result(), f.Err())
	}
}
