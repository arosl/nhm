package bindings

import (
	"context"
	"testing"
)

// adopt gives a free herdr name: while another project's IcyKnoll holds
// icyknoll, a new binding takes icyknoll-2 and herdr is renamed to it; a
// name the agent shows that fits stays; a handoff keeps its source's
// suffixed alias when it is free (nhm-4xt).
func TestAdoptTakesAFreeAlias(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, shown, want string
	}{
		{"another project holds the bare name", "", "icyknoll-2"},
		{"the name herdr shows fits", "icyknoll-3", "icyknoll-3"},
	} {
		h := session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll"},
			paneSpec{pane: "w1:p2", kind: "claude", alias: c.shown, session: "sess-2"})
		env, _ := testEnv(t, h)
		res, err := Adopt(ctx, env, AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p2", ProjectKey: t.TempDir(), ProjectID: "proj-b"})
		if err != nil || res.Binding.HerdrAlias != c.want {
			t.Fatalf("%s: adopt = %+v %v", c.name, res.Binding, err)
		}
		if got, renamed := h.renamed["w1:p2"]; c.shown != c.want && got != c.want || c.shown == c.want && renamed {
			t.Errorf("%s: herdr renames = %v", c.name, h.renamed)
		}
	}

	// A handoff of a suffixed binding keeps its alias in the new pane.
	h := session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll"}, paneSpec{pane: "w1:p3", kind: "claude", session: "sess-3"})
	env, _ := testEnv(t, h)
	project := t.TempDir()
	nb := newBinding("IcyKnoll", project, "w1", "w1:t9", "w1:p9")
	nb.HerdrAlias = "icyknoll-2"
	old, err := env.Bindings.Create(ctx, nb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Bindings.Displace(ctx, old.ID, 1, "the agent moved"); err != nil {
		t.Fatal(err)
	}
	res, err := Adopt(ctx, env, AdoptRequest{PaneID: "w1:p3", HandoffFrom: old.ID})
	if err != nil || res.Binding.HerdrAlias != "icyknoll-2" || h.renamed["w1:p3"] != "icyknoll-2" {
		t.Fatalf("handoff = %+v %v, renames %v", res.Binding, err, h.renamed)
	}
}
