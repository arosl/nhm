package session

import (
	"slices"
	"testing"
)

func TestPaneTags(t *testing.T) {
	for _, c := range []struct {
		label string
		want  []string
	}{
		{"IcyKnoll opus[frontend,api]", []string{"frontend", "api"}},
		{"IcyKnoll [ frontend , ,api ] ", []string{"frontend", "api"}},
		{"IcyKnoll opus", nil},
		{"IcyKnoll[]", nil},
		{"IcyKnoll [a] opus", nil},
		{"", nil},
	} {
		if got := PaneTags(c.label); !slices.Equal(got, c.want) {
			t.Errorf("PaneTags(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

func TestHasAnyTag(t *testing.T) {
	if !HasAnyTag([]string{"Frontend", "api"}, []string{"backend", "FRONTEND"}) {
		t.Error("one match, in another case, is enough")
	}
	if HasAnyTag([]string{"api"}, []string{"backend"}) || HasAnyTag(nil, []string{"api"}) {
		t.Error("no match must not match")
	}
}
