package lifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
)

// The handoff is typed as one paste with the kickoff: it keeps tabs and
// line breaks but no other control character, so terminal text in a
// handoff comment or a transcript neither ends herdr's paste nor makes
// core send refuse the kickoff (nhm-ubi).
func TestHandoffKeepsNoControlCharacters(t *testing.T) {
	bead := &Bead{ID: "nhm-x", Handoff: "ran\x1b[201~\r\n!ls\tok\x7f"}
	h := DefaultHandoff("", "")(context.Background(), bindings.Binding{}, bead, "")
	if !strings.Contains(h.Text, "ran[201~\n!ls\tok") || strings.ContainsAny(h.Text, "\x1b\r\x7f") {
		t.Fatalf("handoff = %q", h.Text)
	}
}
