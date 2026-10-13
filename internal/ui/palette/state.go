package palette

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// State file names in the state directory: one key per line. The fzf
// palette (scripts/nhm-palette) reads and writes the same files.
const (
	PinsFile      = "pins"
	FavoritesFile = "favorites"
	RecentsFile   = "recents"
)

// RecentsMax is how many recent keys the recents file keeps, newest
// first (the fzf palette's PALETTE_RECENTS_MAX, ntm's maxRecents).
const RecentsMax = 8

// stateKey is a key the state files keep; other lines are skipped, as
// the fzf palette skips them.
var stateKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// StateDir is where the palette keeps its state:
// $NHM_PALETTE_STATE_DIR, else $XDG_STATE_HOME/nhm/palette
// (~/.local/state/nhm/palette); "" when neither that nor HOME is set.
func StateDir(getenv func(string) string) string {
	if d := getenv("NHM_PALETTE_STATE_DIR"); d != "" {
		return d
	}
	state := getenv("XDG_STATE_HOME")
	if state == "" {
		home := getenv("HOME")
		if home == "" {
			return ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "nhm", "palette")
}

// Store reads and writes the palette's pins, favorites and recents. A
// Store with no Dir keeps nothing.
type Store struct {
	Dir string
}

// Load reads the pins and favorites.
func (s Store) Load() (PaletteState, error) {
	pins, err := s.list(PinsFile)
	if err != nil {
		return PaletteState{}, err
	}
	favs, err := s.list(FavoritesFile)
	if err != nil {
		return PaletteState{}, err
	}
	return PaletteState{Pinned: pins, Favorites: favs}, nil
}

// Save writes the pins and favorites.
func (s Store) Save(st PaletteState) error {
	if err := s.write(PinsFile, st.Pinned); err != nil {
		return err
	}
	return s.write(FavoritesFile, st.Favorites)
}

// Recents reads the recent keys, newest first.
func (s Store) Recents() ([]string, error) {
	return s.list(RecentsFile)
}

// RecordRecent puts key first in the recents, keeping the newest
// RecentsMax.
func (s Store) RecordRecent(key string) error {
	if !stateKey.MatchString(key) {
		return nil
	}
	old, err := s.list(RecentsFile)
	if err != nil {
		return err
	}
	keys := append([]string{key}, slices.DeleteFunc(old, func(k string) bool { return k == key })...)
	if len(keys) > RecentsMax {
		keys = keys[:RecentsMax]
	}
	return s.write(RecentsFile, keys)
}

// list reads one state file: its valid keys, each once, in file order.
// A missing file is an empty list.
func (s Store) list(name string) ([]string, error) {
	if s.Dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if stateKey.MatchString(line) && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out, nil
}

// write replaces one state file atomically.
func (s Store) write(name string, keys []string) error {
	if s.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, k := range keys {
		if stateKey.MatchString(k) {
			b.WriteString(k + "\n")
		}
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.Dir, name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
