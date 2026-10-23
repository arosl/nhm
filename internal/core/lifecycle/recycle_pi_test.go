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

// pi recycles as Claude does: /exit stops it, and its successor starts
// with pi's --model and --thinking flags. Nothing adds --yolo or a
// settings file: nhm has no guard for pi (nhm-y7q).
func TestRecyclePi(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b, err := w.env.Bindings.Create(ctx, bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p", ProjectID: "proj",
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
	w.env.LaunchSettings = `{"hooks":{}}`
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Continue the bead."})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(w.h.prompts) == 0 || w.h.prompts[0] != "w1:p1=/exit" || len(w.h.starts) != 1 ||
		w.h.starts[0] != "icyknoll pi w1:p1 [--model gpt-5 --thinking high]" {
		t.Fatalf("prompts %v, starts %v", w.h.prompts, w.h.starts)
	}
	if len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "Continue the bead.") {
		t.Fatalf("kickoff = %+v", w.sent)
	}
}
