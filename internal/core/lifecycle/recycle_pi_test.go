package lifecycle

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// piWorld holds one pi agent, IcyKnoll, done in w1:p1.
func piWorld(t *testing.T) (*world, bindings.Binding) {
	t.Helper()
	w := newWorld(t)
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "icyknoll", Kind: "pi", Model: "gpt-5", Effort: "high", LaunchID: "op-x.0"}})
	if err != nil {
		t.Fatal(err)
	}
	w.h.add("w1:p1", "pi", "icyknoll", "", herdr.StatusDone)
	if err := store.WriteSecret(w.env.InstanceDir, b.ID, "tok-IcyKnoll"); err != nil {
		t.Fatal(err)
	}
	if _, err := agentmail.WriteIdentity(w.env.MailConfigHome, "/p", "w1:p1", "IcyKnoll", time.Now()); err != nil {
		t.Fatal(err)
	}
	return w, b
}

// pi recycles as Claude does: /exit stops it, and its successor starts
// with pi's --model and --thinking flags (nhm-y7q) and with nhm's guard
// extension, as the launch had it (nhm-dyr). Nothing adds --yolo.
func TestRecyclePi(t *testing.T) {
	w, b := piWorld(t)
	w.env.LaunchSettings = map[string]string{"claude": `{"hooks":{}}`, "pi": "/i/pi/nhm-guard.ts"}
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Continue the bead."})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(w.h.prompts) == 0 || w.h.prompts[0] != "w1:p1=/exit" || len(w.h.starts) != 1 ||
		w.h.starts[0] != "icyknoll pi w1:p1 [--model gpt-5 --thinking high -e /i/pi/nhm-guard.ts --extension-policy permissive]" {
		t.Fatalf("prompts %v, starts %v", w.h.prompts, w.h.starts)
	}
	if len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "Continue the bead.") {
		t.Fatalf("kickoff = %+v", w.sent)
	}
}

// pi's only guard is nhm's extension on the launch: without it (no dcg),
// recycle refuses before it stops the agent, as spawn's admission would.
func TestRecyclePiWithoutItsGuard(t *testing.T) {
	w, b := piWorld(t)
	w.env.LaunchSettings = map[string]string{"claude": `{"hooks":{}}`}
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Continue the bead."})
	if err == nil || res.Stage != StageCheck || !strings.Contains(err.Error(), "IcyKnoll's successor would start without nhm's pi guard") {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(w.h.prompts) != 0 || len(w.h.starts) != 0 {
		t.Fatalf("prompts %v, starts %v", w.h.prompts, w.h.starts)
	}
	if nb, _ := w.env.Bindings.Get(context.Background(), b.ID); nb.Generation != b.Generation {
		t.Fatalf("binding = %+v", nb)
	}
}
