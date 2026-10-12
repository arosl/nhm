package ui

import (
	"strings"
	"testing"
)

func TestUsesKey(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   bool
	}{
		{"[keys]\nnew_tab = 'prefix+a'\n", true},
		{"[keys]\nnew_tab = \"Prefix+A\"\n", true},
		{"[keys]\nzoom = [\"ctrl+z\", 'prefix+a']\n", true},
		{"[keys]\nzoom = [\n  \"ctrl+z\",\n  \"prefix+a\",\n]\n", true},
		{"[[keys.command]]\nkey = \"prefix+a\"\n", true},
		{"# zoom = \"prefix+a\"\n", false},
		{"[keys]\nzoom = \"prefix+ab\"\n", false},
		{"[[keys.command]]\ncommand = \"echo prefix+a\"\n", false},
	} {
		if got := usesKey(tc.config, "prefix+a") != ""; got != tc.want {
			t.Errorf("usesKey(%q) = %v, want %v", tc.config, got, tc.want)
		}
	}
}

func TestBindRefusesWhatHerdrWouldIgnore(t *testing.T) {
	_, err := Bind("[keys]\ncommand = [{ key = \"prefix+x\", type = \"shell\", command = \"x\" }]\n")
	if err == nil || !strings.Contains(err.Error(), "would not parse") {
		t.Fatalf("an inline keys.command array: %v", err)
	}
	if _, err := Bind("[keys]\nnew_tab = 'prefix+d'\n"); err == nil || !strings.Contains(err.Error(), "prefix+d") {
		t.Fatalf("a single-quoted taken key: %v", err)
	}
}

func TestUnbindKeepsACFSBlock(t *testing.T) {
	acfs := "# >>> acfs palette >>>\nx = 1\n# <<< acfs palette <<<\n"
	bound, err := Bind(acfs)
	if err != nil || strings.Contains(bound, "acfs palette") {
		t.Fatalf("Bind kept ACFS's block: %q %v", bound, err)
	}
	got, err := Unbind(acfs + "\n" + Block())
	if err != nil || !strings.Contains(got, "# >>> acfs palette >>>") || strings.Contains(got, BlockBegin) {
		t.Fatalf("Unbind = %q %v", got, err)
	}
}
