package bindings

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A workspace that holds other projects' agents only is named, once each
// and sorted; one that holds an agent of the project, or none, is not
// (nhm-8ze).
func TestOnlyOtherProjects(t *testing.T) {
	for name, c := range map[string]struct {
		projects []string
		key      string
		want     []string
	}{
		"empty":          {nil, "/div", nil},
		"one of its own": {[]string{"/acfs", "/div"}, "/div", nil},
		"others only":    {[]string{"/nhm", "/acfs", "/acfs"}, "/div", []string{"/acfs", "/nhm"}},
	} {
		if got := OnlyOtherProjects(c.projects, c.key); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// OtherProjectsIn reads the active bindings of one workspace: a retired
// binding of the project does not count as one.
func TestOtherProjectsIn(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	for _, n := range []New{newBinding("BlueLake", "/acfs", "w1", "w1:t1", "w1:p1"), newBinding("RedStone", "/acfs", "w1", "w1:t2", "w1:p2"),
		newBinding("SnowyCliff", "/div", "w2", "w2:t1", "w2:p1")} {
		if _, err := s.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	old, err := s.Create(ctx, newBinding("OldOwl", "/div", "w1", "w1:t3", "w1:p3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retire(ctx, old.ID, 1, "done"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ws, key string
		want    []string
	}{
		{"w1", "/div", []string{"/acfs"}},
		{"w1", "/acfs", nil},
		{"w2", "/div", nil},
		{"w3", "/div", nil},
		{"", "/div", nil},
	} {
		got, err := s.OtherProjectsIn(ctx, c.ws, c.key)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s for %s: %q %v, want %q", c.ws, c.key, got, err, c.want)
		}
	}
}

// Adopt warns, and still binds, when the pane's workspace holds other
// projects' agents only; an agent of a project already there gets no
// such warning.
func TestAdoptWarnsOfAMixedWorkspace(t *testing.T) {
	h := session(paneSpec{pane: "w1:p1", kind: "claude"}, paneSpec{pane: "w1:p2", kind: "claude"}, paneSpec{pane: "w1:p3", kind: "claude"})
	env, _ := testEnv(t, h)
	ctx := context.Background()
	acfs, div := t.TempDir(), t.TempDir()
	if _, err := env.Bindings.Create(ctx, newBinding("BlueLake", acfs, "w1", "w1:t2", "w1:p2")); err != nil {
		t.Fatal(err)
	}
	mixed := func(ws []string) bool {
		return slices.ContainsFunc(ws, func(w string) bool { return strings.Contains(w, "holds agents of other projects only") })
	}
	res, err := Adopt(ctx, env, AdoptRequest{MailName: "SnowyCliff", PaneID: "w1:p1", ProjectKey: div, ProjectID: "div"})
	if err != nil || !res.Created || !mixed(res.Warnings) ||
		!slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "("+acfs+"), not of "+div+":") }) {
		t.Fatalf("adopt into another project's workspace = %+v %v", res, err)
	}
	res, err = Adopt(ctx, env, AdoptRequest{MailName: "RedStone", PaneID: "w1:p3", ProjectKey: acfs, ProjectID: "acfs"})
	if err != nil || !res.Created || mixed(res.Warnings) {
		t.Fatalf("adopt beside its own project = %+v %v", res, err)
	}
}
