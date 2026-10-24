package session

import (
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
)

// The operator's ruling (2026-10-24): an index K is pane M=K of the
// session's agent tab; with several agent tabs, the active tab's.
func TestPaneSelectorIndex(t *testing.T) {
	one := []AgentTab{{N: 3, TabID: "w1:t2"}}
	two := []AgentTab{{N: 3, TabID: "w1:t2"}, {N: 5, TabID: "w1:t4"}}
	for _, c := range []struct {
		text, active string
		tabs         []AgentTab
		want         string
		wantErr      string
	}{
		{"2", "", one, "3.2", ""},
		{"0", "w1:t9", one, "3.0", ""},
		{"1", "w1:t4", two, "5.1", ""},
		{"1", "w1:t2", two, "3.1", ""},
		{"1", "w1:t9", two, "", "pane 1: the session's agents are in tabs 3, 5, and the active tab holds none; give the slot N.M (nhm status shows it)"},
		{"1", "", nil, "", "pane 1: the session has no bound agents"},
	} {
		got, err := ParsePane(c.text, c.tabs, c.active)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr {
				t.Errorf("%s (active %s): %v", c.text, c.active, err)
			}
			continue
		}
		if err != nil || got.Form != PaneSlot || got.Slot != c.want {
			t.Errorf("%s (active %s): %+v, %v; want slot %s", c.text, c.active, got, err, c.want)
		}
	}
}

func TestPaneSelectorWindowDotPane(t *testing.T) {
	got, err := ParsePane("17.02", nil, "")
	if err != nil || got.Form != PaneSlot || got.Slot != "17.2" {
		t.Fatalf("%+v, %v", got, err)
	}
}

func TestPaneSelectorPercentID(t *testing.T) {
	if _, err := ParsePane("%12", nil, ""); err == nil ||
		err.Error() != "pane %12: herdr has no tmux pane ids; give a pane index, the slot N.M (nhm status shows it) or a herdr pane id" {
		t.Fatalf("%v", err)
	}
}

func TestPaneSelectorHerdrForms(t *testing.T) {
	if got, err := ParsePane("wQ:pS", nil, ""); err != nil || got.Form != PaneHerdrID || got.PaneID != "wQ:pS" {
		t.Errorf("pane id: %+v, %v", got, err)
	}
	for _, w := range []string{"TealHawk", "tealhawk", "cc", "claude"} {
		if got, err := ParsePane(w, nil, ""); err != nil || got.Form != PaneWord || got.Word != w {
			t.Errorf("%s: %+v, %v", w, got, err)
		}
	}
}

func TestAgentTabs(t *testing.T) {
	bs := []bindings.Binding{{Slot: "5.1", TabID: "w1:t4"}, {Slot: "3.2", TabID: "w1:t2"}, {Slot: "3.1", TabID: "w1:t2"}, {Slot: ""}}
	got := AgentTabs(bs)
	if len(got) != 2 || got[0] != (AgentTab{N: 3, TabID: "w1:t2"}) || got[1] != (AgentTab{N: 5, TabID: "w1:t4"}) {
		t.Fatalf("%+v", got)
	}
}

// ntm's JSON ints: pane and pane_index are the slot's M, window_index its
// N.
func TestJSONPaneIndexIsSlotM(t *testing.T) {
	if w, p := SlotIndexes("17.2"); w != 17 || p != 2 {
		t.Errorf("17.2: %d, %d", w, p)
	}
	if w, p := SlotIndexes(""); w != 0 || p != 0 {
		t.Errorf("no slot: %d, %d", w, p)
	}
}
