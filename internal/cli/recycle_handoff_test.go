package cli

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/lifecycle"
)

// Recycle's handoff line names the bead whose handoff comment it holds,
// or, when that bead has none, says so in the degraded part (nhm-d2f).
func TestHandoffLine(t *testing.T) {
	for _, c := range []struct {
		res  lifecycle.RecycleResult
		want string
	}{
		{lifecycle.RecycleResult{HandoffBead: "nhm-qbi.11", Handoff: &lifecycle.Handoff{Sources: []string{"bead", "cass", "cm"}}},
			"handoff: bead nhm-qbi.11, cass, cm"},
		{lifecycle.RecycleResult{HandoffBead: "nhm-4mx", Handoff: &lifecycle.Handoff{Sources: []string{"cass", "cm"},
			Degraded: []string{"bead: no handoff comment on nhm-4mx"}}},
			"handoff: cass, cm (degraded: bead: no handoff comment on nhm-4mx)"},
		{lifecycle.RecycleResult{Handoff: &lifecycle.Handoff{Sources: []string{"excerpt"}}}, "handoff: excerpt"},
	} {
		sources := strings.Join(c.res.Handoff.Sources, ",")
		if got := handoffLine(c.res); got != c.want {
			t.Errorf("handoffLine = %q, want %q", got, c.want)
		}
		if after := strings.Join(c.res.Handoff.Sources, ","); after != sources {
			t.Errorf("handoffLine changed the result's sources: %s, was %s", after, sources)
		}
	}
}
