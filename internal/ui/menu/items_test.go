package menu

import (
	"testing"

	"github.com/arosl/nhm/internal/ui/actions"
)

// No leaf the menu leaves out is covered by a row, and each has a reason.
func TestNotInMenuHasReasons(t *testing.T) {
	covered := map[string]string{}
	for _, a := range actions.Table() {
		for _, c := range a.Covers {
			covered[c] = a.ID
		}
	}
	for _, n := range NotInMenu {
		if id, ok := covered[n.Leaf]; ok {
			t.Errorf("%q is not in the menu, but %s covers it", n.Leaf, id)
		}
		if n.Reason == "" {
			t.Errorf("%q has no reason", n.Leaf)
		}
	}
}
