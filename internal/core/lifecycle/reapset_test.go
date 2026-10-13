package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// The menu's reap: a dry run, then exactly the reviewed set. A binding
// that changed or went away is refused by name; an agent the review did
// not show is never retired.
func TestReapRetiresOnlyTheReviewedSet(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i, name := range []string{"IcyKnoll", "BlueLake", "RedStone", "GreenPike"} {
		w.bind(t, name, "w1:p"+string(rune('1'+i)), herdr.StatusIdle)
	}
	req := ReapRequest{Idle: 10 * time.Minute, WorkspaceID: "w1", DryRun: true}
	if _, err := Reap(ctx, w.env, nil, req); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(11 * time.Minute)
	review, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	gen := map[string]ReapItem{}
	for _, it := range review {
		if it.Action != "would_retire" || it.Generation == 0 {
			t.Fatalf("review = %+v", it)
		}
		gen[it.MailName] = it
	}
	var only []ReapTarget
	for _, name := range []string{"IcyKnoll", "BlueLake", "RedStone"} {
		only = append(only, ReapTarget{BindingID: gen[name].BindingID, Generation: gen[name].Generation})
	}
	// After the review: BlueLake is recycled (a new generation), RedStone
	// is retired by hand.
	blue, err := w.env.Bindings.Get(ctx, gen["BlueLake"].BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.env.Bindings.Occupy(ctx, blue.ID, blue.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: blue.TabID,
		PaneID: blue.PaneID, HerdrAlias: blue.HerdrAlias, Kind: blue.Kind, Model: blue.Model, NativeSession: blue.NativeSession}); err != nil {
		t.Fatal(err)
	}
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "RedStone"}); err != nil {
		t.Fatal(err)
	}
	req.DryRun, req.Only = false, only
	items, err := Reap(ctx, w.env, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ReapItem{}
	for _, it := range items {
		got[it.MailName] = it
	}
	if len(items) != 3 || got["IcyKnoll"].Action != "retired" {
		t.Fatalf("items = %+v", items)
	}
	if b := got["BlueLake"]; b.Action != "refused" || !strings.Contains(b.Reason, "changed since it was reviewed") {
		t.Fatalf("BlueLake = %+v", b)
	}
	if r := got["RedStone"]; r.Action != "refused" || !strings.Contains(r.Reason, "no longer an active binding") {
		t.Fatalf("RedStone = %+v", r)
	}
	if green, err := w.env.Bindings.Get(ctx, gen["GreenPike"].BindingID); err != nil || green.Status != bindings.Active {
		t.Fatalf("GreenPike, outside the review = %+v %v", green, err)
	}
	// An explicitly empty set is refused, never read as "all".
	req.Only = []ReapTarget{}
	if _, err := Reap(ctx, w.env, nil, req); !errors.Is(err, ErrRefused) {
		t.Fatalf("an empty set = %v", err)
	}
}
