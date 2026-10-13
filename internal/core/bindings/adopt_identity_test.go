package bindings

import (
	"context"
	"errors"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// TestAdoptChecksTheReviewedSession: a caller that reviewed an unbound
// agent names its session; adopt refuses, binding nothing, when another
// agent started in the pane since (nhm-qbi.9).
func TestAdoptChecksTheReviewedSession(t *testing.T) {
	h := session(paneSpec{pane: "w1:p1", kind: "claude", session: "sess-2"}, paneSpec{pane: "w1:p2", kind: "claude"},
		paneSpec{pane: "w1:p3", kind: "codex", session: "sess-3"})
	env, _ := testEnv(t, h)
	ctx := context.Background()
	project := t.TempDir()
	for name, req := range map[string]AdoptRequest{
		"another session": {MailName: "IcyKnoll", PaneID: "w1:p1", ExpectedSession: "id:sess-1"},
		"no session now":  {MailName: "IcyKnoll", PaneID: "w1:p2", ExpectedSession: "id:sess-1"},
		// The reviewed claude was replaced by a codex: changed, not failed.
		"another kind": {MailName: "IcyKnoll", PaneID: "w1:p3", Kind: "claude", ExpectedSession: "id:sess-1"},
	} {
		req.ProjectKey, req.ProjectID = project, "p"
		res, err := Adopt(ctx, env, req)
		if !errors.Is(err, ErrAgentChanged) {
			t.Errorf("%s: %v", name, err)
		}
		// Its record says what the CLI's exit 2 says: refused, not failed.
		if op, err := env.DB.Operation(ctx, res.OperationID); err != nil || op.Status != "refused" {
			t.Errorf("%s: the record = %+v (%v)", name, op, err)
		}
	}
	if open, err := env.Bindings.List(ctx, Filter{MailName: "IcyKnoll"}); err != nil || len(open) != 0 {
		t.Fatalf("a refused adopt bound %+v (%v)", open, err)
	}
	res, err := Adopt(ctx, env, AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p1", ExpectedSession: "id:sess-2",
		ProjectKey: project, ProjectID: "p"})
	if err != nil || !res.Created || res.Binding.PaneID != "w1:p1" {
		t.Fatalf("the reviewed session = %+v %v", res, err)
	}
	if SessionKey(&herdr.AgentSession{Kind: "id", Value: "v"}) != "id:v" || SessionKey(nil) != "" {
		t.Fatal("SessionKey")
	}
}
