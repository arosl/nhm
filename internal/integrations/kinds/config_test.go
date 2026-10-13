package kinds

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// herdr093Keys are key names herdr 0.9.3 takes and refuses (invalid_key),
// probed on a private server in nhm's sandbox (nhm-1b2, 2026-10-13):
// agent.send_keys (the call nhm's interrupt uses, to a named fake agent)
// and pane.send_keys give the same answer for each name.
var herdr093Keys = struct{ takes, refuses []string }{
	takes: []string{"esc", "escape", "enter", "return", "tab", "shift+tab", "backspace", "space", "up", "down", "left", "right",
		"f1", "f12", "ctrl+a", "ctrl+c", "ctrl+e", "ctrl+u", "ctrl+z", "ctrl+space", "alt+a", "shift+a", "a", "1"},
	refuses: []string{"home", "end", "pageup", "pagedown", "pgup", "pgdn", "delete", "del", "insert", "backtab", "btab",
		"C-a", "C-e"},
}

// Every key name nhm lets a config kind send is one herdr takes, and no
// name herdr refuses passes (nhm-1b2).
func TestKeysAreHerdrs(t *testing.T) {
	takes := map[string]bool{}
	for _, k := range herdr093Keys.takes {
		takes[k] = true
	}
	for k := range keyNames {
		if !takes[k] {
			t.Errorf("nhm allows %q, which herdr 0.9.3 was not seen to take", k)
		}
	}
	for c := 'a'; c <= 'z'; c++ {
		if k := "ctrl+" + string(c); !KeyAllowed(k) {
			t.Errorf("%q is refused", k)
		}
	}
	for _, k := range herdr093Keys.refuses {
		if KeyAllowed(k) {
			t.Errorf("nhm allows %q, which herdr 0.9.3 refuses", k)
		}
	}
}

// A config kind's C-a … C-z, nhm's old spelling, loads as herdr's ctrl+a
// … ctrl+z; herdr's own names load as they are; a name herdr refuses does
// not load.
func TestConfigInterruptKeysAreHerdrs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("[kinds.mycli]\nflags = [\"my\"]\ninterrupt_keys = [\"C-e\", \"ctrl+u\", \"esc\"]\n")
	r := NewRegistry()
	if err := r.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if m, ok := r.ByFlag("my"); !ok || !slices.Equal(m.InterruptKeys, []string{"ctrl+e", "ctrl+u", "esc"}) {
		t.Fatalf("mycli's interrupt keys = %q", m.InterruptKeys)
	}
	for _, bad := range []string{"C-E", "C-ab", "ctrl+", "home", "pagedown", "delete"} {
		write("[kinds.mycli]\nflags = [\"my\"]\ninterrupt_keys = [\"" + bad + "\"]\n")
		if err := NewRegistry().LoadConfig(path); err == nil {
			t.Errorf("interrupt key %q loaded", bad)
		}
	}
}
