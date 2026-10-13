package palette

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateDir(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"NHM_PALETTE_STATE_DIR": "/s", "XDG_STATE_HOME": "/x", "HOME": "/h"}, "/s"},
		{map[string]string{"XDG_STATE_HOME": "/x", "HOME": "/h"}, "/x/nhm/palette"},
		{map[string]string{"HOME": "/h"}, "/h/.local/state/nhm/palette"},
		{map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := StateDir(envOf(c.env)); got != c.want {
			t.Errorf("StateDir(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

// The fzf palette's files carry over: one key per line, other lines
// skipped.
func TestStoreReadsTheFzfPalettesFiles(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	writeFile(t, filepath.Join(s.Dir, PinsFile), "review\n\nnot a key\nfix\nreview\n")
	writeFile(t, filepath.Join(s.Dir, FavoritesFile), "review\nfix\nplan\n")
	writeFile(t, filepath.Join(s.Dir, RecentsFile), "plan\nreview\n")
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Pinned, ",") != "review,fix" || strings.Join(st.Favorites, ",") != "review,fix,plan" {
		t.Fatalf("state = %+v", st)
	}
	rec, err := s.Recents()
	if err != nil || strings.Join(rec, ",") != "plan,review" {
		t.Fatalf("recents = %v %v", rec, err)
	}
}

func TestStoreWritesWhatTheFzfPaletteReads(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "new", "palette")}
	if st, err := s.Load(); err != nil || len(st.Pinned) != 0 || len(st.Favorites) != 0 {
		t.Fatalf("missing files = %+v %v", st, err)
	}
	if err := s.Save(PaletteState{Pinned: []string{"fix"}, Favorites: []string{"fix", "review", "bad key"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, FavoritesFile))
	if err != nil || string(data) != "fix\nreview\n" {
		t.Fatalf("favorites file = %q %v", data, err)
	}
	if data, _ := os.ReadFile(filepath.Join(s.Dir, PinsFile)); string(data) != "fix\n" {
		t.Fatalf("pins file = %q", data)
	}
	if st, err := os.Stat(s.Dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("state dir = %v %v", st, err)
	}
	// A save that empties a list leaves an empty file.
	if err := s.Save(PaletteState{Favorites: []string{"review"}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Load(); len(st.Pinned) != 0 || strings.Join(st.Favorites, ",") != "review" {
		t.Fatalf("after save = %+v", st)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 2 {
		t.Fatalf("leftover temp files: %v", entries)
	}
}

func TestRecordRecentKeepsTheNewest(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "c", "bad key"} {
		if err := s.RecordRecent(k); err != nil {
			t.Fatal(err)
		}
	}
	rec, _ := s.Recents()
	if strings.Join(rec, ",") != "c,i,h,g,f,e,d,b" {
		t.Fatalf("recents = %v", rec)
	}
}

func TestStoreWithoutADirKeepsNothing(t *testing.T) {
	var s Store
	if err := s.Save(PaletteState{Pinned: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRecent("x"); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Load(); err != nil || st.Pinned != nil {
		t.Fatalf("= %+v %v", st, err)
	}
}
