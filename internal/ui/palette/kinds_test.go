package palette

import (
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

func keysString(keys []KindKey) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k.Key + "=" + k.Kind
	}
	return strings.Join(parts, " ")
}

func TestKindKeysFromNhmsKinds(t *testing.T) {
	// ntm's digits for the kinds ntm knows; the free digits, in registry
	// order, for the rest.
	got := keysString(KindKeys(kinds.NewRegistry().All()))
	if want := "2=claude 3=codex 4=pi 5=agy 7=omp 8=opencode 9=grok"; got != want {
		t.Fatalf("keys = %s, want %s", got, want)
	}
	for _, k := range KindKeys(kinds.NewRegistry().All()) {
		if k.Key == KeyAll || k.Key == KeySelect {
			t.Fatalf("%s takes ntm's fixed digit %s", k.Kind, k.Key)
		}
	}
}

func TestKindKeysDoNotDependOnOrderOrPresence(t *testing.T) {
	// A config kind of ntm's gemini type takes ntm's 4; kinds past the
	// free digits get none.
	adapters := []kinds.Adapter{
		{Name: "x1", NTMType: "x1"}, {Name: "x2", NTMType: "x2"}, {Name: "x3", NTMType: "x3"},
		{Name: "claude", NTMType: "claude"}, {Name: "gem", NTMType: "gemini"},
		{Name: "x4", NTMType: "x4"}, {Name: "x5", NTMType: "x5"}, {Name: "x6", NTMType: "x6"},
		{Name: "claude2", NTMType: "claude"},
	}
	got := keysString(KindKeys(adapters))
	if want := "2=claude 3=x1 4=gem 5=x2 7=x3 8=x4 9=x5"; got != want {
		t.Fatalf("keys = %s, want %s", got, want)
	}
}
