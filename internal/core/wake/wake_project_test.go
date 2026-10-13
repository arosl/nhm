package wake

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// TestMailOfAnotherProjectWakesNobody: mail sent in /acfs to YellowPeak,
// by name, herdr name or pane id, never nudges the YellowPeak of /p
// (nhm-ktm).
func TestMailOfAnotherProjectWakesNobody(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "YellowPeak", "w1:p9", "claude", herdr.StatusIdle)
	for _, name := range []string{"YellowPeak", "yellowpeak", "w1:p9"} {
		if _, err := Resolve(ctx, w.env.Bindings, "/acfs", name); !errors.Is(err, ErrNoBinding) || !strings.Contains(err.Error(), "in /acfs") {
			t.Errorf("%s in /acfs: %v", name, err)
		}
		if got, err := Resolve(ctx, w.env.Bindings, "/p", name); err != nil || got.ID != b.ID {
			t.Errorf("%s in /p: %+v %v", name, got, err)
		}
	}
	s := sent(9, "SageOak", "acfs-2", "YellowPeak", "yellowpeak", "w1:p9")
	s.ProjectKey = "/acfs"
	rep, err := MailSent(ctx, w.env, []Sent{s})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recorded != 0 || len(rep.Unbound) != 3 || len(rep.Decisions) != 0 || len(w.h.prompts) != 0 {
		t.Fatalf("report = %+v, prompts %+v", rep, w.h.prompts)
	}
	if got := pendingIDs(t, w, "YellowPeak"); len(got) != 0 {
		t.Fatalf("YellowPeak of /p has pending mail of /acfs: %v", got)
	}
}
