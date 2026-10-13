package menu

import (
	"slices"
	"testing"

	"github.com/arosl/nhm/internal/ui"
)

// mvpLeaves are the MVP leaves of evidence/verb-coverage.md (with gap 3's
// recover inspect) that the menu reaches. The board's own keys and the
// palette reach some of them too; the menu reaches every one.
var mvpLeaves = []string{
	"status", "list", "snapshot", "activity", "health", "attention", "read", "watch",
	"compatibility status", "capabilities", "doctor", "version",
	"send", "send -t", "interrupt", "memory", "wake", "mail inbox",
	"spawn", "add", "new", "retire", "recycle", "reap", "kill", "adopt", "attach", "zoom",
	"quota", "limits",
	"modes list", "modes explain",
	"ensemble presets", "ensemble runs", "ensemble status", "ensemble stop",
	"feedback list", "admin recover inspect", "recent operations",
}

func TestEveryMVPLeafHasARow(t *testing.T) {
	covered := map[string]string{}
	for _, it := range Items {
		for _, c := range it.Covers {
			if prev, ok := covered[c]; ok {
				t.Errorf("%q is covered by %s and %s", c, prev, it.ID)
			}
			covered[c] = it.ID
		}
	}
	for _, leaf := range mvpLeaves {
		if _, ok := covered[leaf]; !ok {
			t.Errorf("MVP leaf %q has no menu row", leaf)
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

func TestLevelsAndKeys(t *testing.T) {
	ids := map[string]bool{}
	for _, it := range Items {
		if ids[it.ID] {
			t.Errorf("two rows have ID %s", it.ID)
		}
		ids[it.ID] = true
		if it.Label == "" || it.Help == "" || it.Kind == "" {
			t.Errorf("%s: a label, help and kind are required", it.ID)
		}
		if it.Kind == KindGroup && it.Parent != "" {
			t.Errorf("%s: a group in a group makes a third level", it.ID)
		}
		if it.Parent != "" {
			p, ok := ByID(it.Parent)
			if !ok || p.Kind != KindGroup {
				t.Errorf("%s: parent %q is not a group", it.ID, it.Parent)
			}
		}
	}
	for _, parent := range append([]string{""}, groupIDs()...) {
		keys := map[string]string{}
		level := Level(parent)
		if len(level) == 0 {
			t.Errorf("group %q has no rows", parent)
		}
		for _, it := range level {
			if slices.Contains(Reserved, it.Key) {
				t.Errorf("%s: key %q is reserved", it.ID, it.Key)
			}
			if prev, ok := keys[it.Key]; ok {
				t.Errorf("key %q picks %s and %s in level %q", it.Key, prev, it.ID, parent)
			}
			keys[it.Key] = it.ID
		}
	}
}

func groupIDs() []string {
	var out []string
	for _, it := range Items {
		if it.Kind == KindGroup {
			out = append(out, it.ID)
		}
	}
	return out
}

func TestUnavailableWithoutWorkspace(t *testing.T) {
	kill, _ := ByID("workspace.kill")
	if kill.Unavailable(ui.Context{}) == "" {
		t.Error("kill runs with no captured workspace")
	}
	if r := kill.Unavailable(ui.Context{WorkspaceID: "w1"}); r != "" {
		t.Errorf("kill in w1: %s", r)
	}
	status, _ := ByID("view.status")
	if r := status.Unavailable(ui.Context{}); r != "" {
		t.Errorf("status: %s", r)
	}
}
