package bindings

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestResolveStaysInTheNamedProject: a named project limits the name, its
// other spelling, the herdr alias and the pane id; a miss there is not
// found, never another project's binding (nhm-ktm).
func TestResolveStaysInTheNamedProject(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	b, err := s.Create(ctx, newBinding("YellowPeak", "/nhm", "w1", "w1:t1", "w1:p9"))
	if err != nil {
		t.Fatal(err)
	}
	// Only a retired row in /nhm: a named project /acfs must not find it.
	old, err := s.Create(ctx, newBinding("OldOwl", "/nhm", "w1", "w1:t2", "w1:p8"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retire(ctx, old.ID, 1, "done"); err != nil {
		t.Fatal(err)
	}
	h := session(paneSpec{pane: "w1:p9", kind: "claude", alias: "yellowpeak"})
	noEnv := func(string) string { return "" }
	for _, target := range []string{"yellowpeak", "YellowPeak", "w1:p9", "OldOwl"} {
		_, err := Resolve(ctx, s, h, Query{Target: target, ProjectKey: "/acfs"}, noEnv)
		if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "in /acfs") {
			t.Errorf("%s in /acfs: %v", target, err)
		}
	}
	for _, target := range []string{"yellowpeak", "YellowPeak", "w1:p9"} {
		r, err := Resolve(ctx, s, h, Query{Target: target, ProjectKey: "/nhm"}, noEnv)
		if err != nil || r.Binding.ID != b.ID {
			t.Errorf("%s in /nhm: %+v %v", target, r.Binding, err)
		}
	}
	if r, err := Resolve(ctx, s, h, Query{Target: "OldOwl", ProjectKey: "/nhm"}, noEnv); err != nil || r.Binding.ID != old.ID {
		t.Errorf("OldOwl in /nhm: %+v %v", r.Binding, err)
	}
	if _, err := s.ActiveByAlias(ctx, "/acfs", "yellowpeak"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ActiveByAlias in /acfs: %v", err)
	}
	if got, err := s.ActiveByAlias(ctx, "", "yellowpeak"); err != nil || got.ID != b.ID {
		t.Errorf("ActiveByAlias in every project: %+v %v", got, err)
	}
}
