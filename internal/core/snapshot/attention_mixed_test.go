package snapshot

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
)

// Status names a binding whose workspace holds other bindings, none of
// its project (nhm-8ze): SnowyCliff of /div among /acfs agents, and both
// agents of a workspace shared one to one. An agent alone in its
// workspace is not named; an absent binding of the project counts as one
// beside it; an unbound or displaced agent counts as none.
func TestAttentionNamesAMixedWorkspace(t *testing.T) {
	agent := func(ws, pane, name, project string, status bindings.Status) Agent {
		return Agent{PaneID: pane, WorkspaceID: ws, Kind: "claude", Binding: &BindingRef{ID: "bnd-" + name, Status: status,
			MailName: name, ProjectKey: project, NameMatches: true}}
	}
	m := &Model{Agents: []Agent{
		agent("w1", "w1:p1", "BlueLake", "/acfs", bindings.Active),
		agent("w1", "w1:p2", "RedStone", "/acfs", bindings.Active),
		agent("w1", "w1:p3", "SnowyCliff", "/div", bindings.Active),
		agent("w2", "w2:p1", "IcyKnoll", "/nhm", bindings.Active),
		agent("w2", "w2:p2", "OldFox", "/acfs", bindings.Displaced),
		{PaneID: "w2:p3", WorkspaceID: "w2", Kind: "codex"},
		agent("w3", "w3:p1", "GreenPike", "/p1", bindings.Active),
		agent("w3", "w3:p2", "TealOwl", "/p2", bindings.Active),
		agent("w4", "w4:p1", "Lone", "/x", bindings.Active),
	}, Absent: []bindings.Binding{
		{ID: "bnd-Mate", MailName: "Mate", MailProjectKey: "/x", WorkspaceID: "w4", PaneID: "w4:p2"},
		{ID: "bnd-Elsewhere", MailName: "Elsewhere", MailProjectKey: "/acfs", WorkspaceID: "w4", PaneID: "w4:p3"},
	}}
	var named []string
	for _, it := range Attention(m) {
		if it.Kind == AttnMixedWorkspace {
			if it.Severity != SevWarning || it.BindingID != "bnd-"+it.MailName {
				t.Errorf("item = %+v", it)
			}
			named = append(named, it.MailName)
			if it.MailName == "SnowyCliff" && !strings.Contains(it.Detail, "SnowyCliff of /div shares workspace w1 with agents of other projects only (/acfs)") {
				t.Errorf("detail = %q", it.Detail)
			}
		}
	}
	slices.Sort(named)
	// w4: Lone and Mate (absent) are of /x, so only Elsewhere is named.
	if want := []string{"Elsewhere", "GreenPike", "SnowyCliff", "TealOwl"}; !slices.Equal(named, want) {
		t.Fatalf("named %q, want %q", named, want)
	}
}

// A build for one project (nhm status --project) still sees the other
// projects in its agents' workspaces: SnowyCliff of /div among /p's
// agents in w1 is named in a /div build, as in an unscoped one; a /p
// build names none of /p's agents, which have each other.
func TestAttentionSeesOtherProjectsInAProjectBuild(t *testing.T) {
	db, h := setup(t)
	ctx := context.Background()
	if _, err := bindings.NewStore(db).Create(ctx, bindings.New{MailName: "SnowyCliff", MailProjectKey: "/div", ProjectID: "div",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t7", PaneID: "w1:p7",
			HerdrAlias: "snowycliff", Kind: "claude"}}); err != nil {
		t.Fatal(err)
	}
	mixed := func(project string) []string {
		m := Build(ctx, Deps{Herdr: h, DB: db, Now: func() time.Time { return t0 }}, Options{ProjectKey: project})
		var names []string
		for _, it := range Attention(m) {
			if it.Kind == AttnMixedWorkspace {
				names = append(names, it.MailName)
			}
		}
		return names
	}
	for _, c := range []struct {
		project string
		want    []string
	}{{"", []string{"SnowyCliff"}}, {"/div", []string{"SnowyCliff"}}, {"/p", nil}} {
		if got := mixed(c.project); !slices.Equal(got, c.want) {
			t.Errorf("build for %q names %q, want %q", c.project, got, c.want)
		}
	}
}
