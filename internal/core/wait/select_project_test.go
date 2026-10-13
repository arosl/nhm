package wait

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
)

// TestSelectRefusesANameOfTwoProjects: one workspace holds a YellowPeak of
// /p and one of /acfs. The name alone selects neither, so an interrupt
// never reaches both; --project, the herdr name or the slot selects one
// (nhm-y2e).
func TestSelectRefusesANameOfTwoProjects(t *testing.T) {
	_, s := openDB(t)
	ctx := context.Background()
	mine := create(t, s, "YellowPeak", "w1:t1", "w1:p1", "claude")
	other, err := s.Create(ctx, bindings.New{MailName: "YellowPeak", MailProjectKey: "/acfs", ProjectID: "acfs",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p2",
			HerdrAlias: "yellowpeak-2", Kind: "codex"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Select(ctx, s, "w1", "", Selector{Names: []string{"YellowPeak"}})
	if !errors.Is(err, bindings.ErrAmbiguous) || !strings.Contains(err.Error(), "slot "+other.Slot+", /acfs") ||
		!strings.Contains(err.Error(), "name one by its slot") {
		t.Fatalf("no project: %v", err)
	}
	for name, sel := range map[string]struct {
		project string
		sel     Selector
		want    string
	}{
		"--project /acfs":   {"/acfs", Selector{Names: []string{"YellowPeak"}}, other.ID},
		"--project /p":      {"/p", Selector{Names: []string{"yellowpeak"}}, mine.ID},
		"the herdr name":    {"", Selector{Names: []string{"yellowpeak-2"}}, other.ID},
		"the slot":          {"", Selector{Slots: []string{other.Slot}}, other.ID},
		"no name: both are": {"", Selector{}, ""},
		// The other lists narrow the name first (ScarletBass's review).
		"the name and its slot": {"", Selector{Names: []string{"YellowPeak"}, Slots: []string{mine.Slot}}, mine.ID},
		"the name and its kind": {"", Selector{Names: []string{"YellowPeak"}, Kinds: []string{"codex"}}, other.ID},
	} {
		got, err := Select(ctx, s, "w1", sel.project, sel.sel)
		switch {
		case err != nil:
			t.Errorf("%s: %v", name, err)
		case sel.want == "" && len(got) != 2:
			t.Errorf("%s: %d agents, want both", name, len(got))
		case sel.want != "" && (len(got) != 1 || got[0].ID != sel.want):
			t.Errorf("%s: %+v, want %s", name, got, sel.want)
		}
	}
}
