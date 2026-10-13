package bindings

import "testing"

func TestProjectTabLabel(t *testing.T) {
	for in, want := range map[string]string{
		"/srv/work/nhm":        "nhm",
		"/srv/work/nhm/":       "nhm",
		"/srv/repos/acfs-fork": "acfs-fork",
		"":                     "",
	} {
		if got := ProjectTabLabel(in); got != want {
			t.Errorf("ProjectTabLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
