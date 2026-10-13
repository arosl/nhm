package bindings

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// The names nhm gives a mail name: its alias, or that with -2 … -9.
func TestAliasFits(t *testing.T) {
	for name, want := range map[string]bool{
		"bluelake": true, "bluelake-2": true, "bluelake-9": true,
		"bluelake-1": false, "bluelake-10": false, "bluelake-02": false, "bluelake-x": false,
		"bluelakes": false, "BlueLake": false, "": false,
	} {
		if got := AliasFits(name, "BlueLake"); got != want {
			t.Errorf("AliasFits(%q, BlueLake) = %v, want %v", name, got, want)
		}
	}
	if AliasFits("x", "Blue Lake") {
		t.Error("a mail name with no valid alias fits")
	}
}

// FreeAlias: the bare alias when free, else the first free suffix; the
// agent's own panes hold nothing; a preferred name that fits and is
// free wins (nhm-4xt).
func TestFreeAlias(t *testing.T) {
	live := func(names ...string) []herdr.AgentInfo {
		var out []herdr.AgentInfo
		for i, n := range names {
			out = append(out, herdr.AgentInfo{Name: n, PaneID: "w9:p" + string(rune('1'+i))})
		}
		return out
	}
	for _, c := range []struct {
		name   string
		live   []herdr.AgentInfo
		prefer string
		own    []string
		want   string
	}{
		{"free", live("redstone"), "", nil, "bluelake"},
		{"taken elsewhere", live("bluelake"), "", nil, "bluelake-2"},
		{"the next free suffix", live("bluelake", "bluelake-2"), "", nil, "bluelake-3"},
		{"held by its own pane", live("bluelake"), "", []string{"w9:p1"}, "bluelake"},
		{"keeps what herdr shows", live("bluelake"), "bluelake-4", nil, "bluelake-4"},
		{"a preferred name that is taken", live("bluelake", "bluelake-4"), "bluelake-4", nil, "bluelake-2"},
		{"a preferred name that does not fit", nil, "redstone", nil, "bluelake"},
		{"a handoff's source pane", live("bluelake-2"), "bluelake-2", []string{"w9:p8", "w9:p1"}, "bluelake-2"},
	} {
		if got, err := FreeAlias("BlueLake", c.live, c.prefer, c.own...); err != nil || got != c.want {
			t.Errorf("%s: FreeAlias = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	all := []string{"bluelake"}
	for i := 2; i <= 9; i++ {
		all = append(all, "bluelake-"+string(rune('0'+i)))
	}
	if _, err := FreeAlias("BlueLake", live(all...), ""); err == nil || !strings.Contains(err.Error(), "suffixes are taken") {
		t.Fatalf("every suffix taken = %v", err)
	}
	if _, err := FreeAlias("Blue Lake", nil, ""); err == nil {
		t.Fatal("a mail name with no valid alias got one")
	}
}
