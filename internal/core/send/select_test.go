package send

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// TestSelectRefusesANameOfTwoProjects: one workspace holds a YellowPeak of
// /p and one of /acfs. The name alone is ambiguous and sends nothing;
// with --project it selects that project's agent (nhm-ktm).
func TestSelectRefusesANameOfTwoProjects(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "YellowPeak", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	other, err := w.env.Bindings.Create(ctx, bindings.New{MailName: "YellowPeak", MailProjectKey: "/acfs", ProjectID: "acfs",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t2", PaneID: "w1:p2",
			HerdrAlias: "yellowpeak-2", Kind: "claude", Model: "m-claude"}})
	if err != nil {
		t.Fatal(err)
	}
	w.fake.add("w1:p2", "claude", "yellowpeak-2", herdr.StatusIdle, 10)
	agents, _ := w.fake.AgentList(ctx)
	sel := Selector{Names: []string{"YellowPeak"}}
	_, _, err = Select(ctx, w.env.Bindings, agents, "w1", "", "", sel)
	if !errors.Is(err, bindings.ErrAmbiguous) || !strings.Contains(err.Error(), "give --project") {
		t.Fatalf("no project: %v", err)
	}
	if _, err := Send(ctx, w.env, base(Request{Message: "hi", Selector: sel})); !errors.Is(err, bindings.ErrAmbiguous) || w.fake.promptCount() != 0 {
		t.Fatalf("send without a project: %v, %d prompts", err, w.fake.promptCount())
	}
	ts, _, err := Select(ctx, w.env.Bindings, agents, "w1", "/acfs", "", sel)
	if err != nil || len(ts) != 1 || ts[0].Binding.ID != other.ID {
		t.Fatalf("--project /acfs: %+v %v", ts, err)
	}
	// The herdr name is one agent's alone.
	ts, _, err = Select(ctx, w.env.Bindings, agents, "w1", "", "", Selector{Names: []string{"yellowpeak-2"}})
	if err != nil || len(ts) != 1 || ts[0].Binding.ID != other.ID {
		t.Fatalf("yellowpeak-2: %+v %v", ts, err)
	}
	res, err := Send(ctx, w.env, base(Request{Message: "hi", ProjectKey: "/acfs", Selector: sel}))
	if err != nil || len(res.Targets) != 1 || res.Targets[0].PaneID != "w1:p2" || w.fake.promptCount() != 1 {
		t.Fatalf("send with --project: %+v %v, %d prompts", res.Targets, err, w.fake.promptCount())
	}
}
