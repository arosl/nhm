// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/config/config.go
// at commit 9d5ed977: the PaletteCmd and PaletteState types; see LICENSE in
// this directory and PROVENANCE.md. Local changes: PaletteCmd.Source; no
// TOML tags; the commands come from nhm's palette catalogs
// (templates.ReadCatalog) in the fzf palette's order, not from ntm's config.

package palette

import (
	"os"
	"path/filepath"

	"github.com/arosl/nhm/internal/core/templates"
)

// PaletteCmd represents a command in the palette
type PaletteCmd struct {
	Key      string
	Label    string
	Prompt   string
	Category string
	Tags     []string
	// Source is the catalog file the command came from.
	Source string
}

// PaletteState stores user palette preferences (favorites/pins).
// nhm keeps it in the fzf palette's state files (Store).
type PaletteState struct {
	Pinned    []string
	Favorites []string
}

// CatalogName is a palette catalog's file name.
const CatalogName = "command_palette.md"

// CatalogPaths returns the palette catalogs to read, in order: file
// alone when it is given (--file, else $NHM_PALETTE_FILE); otherwise
// command_palette.md in dir and at the project root,
// $XDG_CONFIG_HOME/nhm/command_palette.md, then ACFS's catalog under
// ${ACFS_HOME:-~/.acfs}. Each existing file is listed once, by its
// resolved path. These are the fzf palette's files, in its order.
func CatalogPaths(getenv func(string) string, dir, root, file string) []string {
	if file == "" {
		file = getenv("NHM_PALETTE_FILE")
	}
	if file != "" {
		return []string{file}
	}
	home := getenv("HOME")
	config := getenv("XDG_CONFIG_HOME")
	if config == "" && home != "" {
		config = filepath.Join(home, ".config")
	}
	acfs := getenv("ACFS_HOME")
	if acfs == "" && home != "" {
		acfs = filepath.Join(home, ".acfs")
	}
	var candidates []string
	for _, d := range []string{dir, root} {
		if d != "" {
			candidates = append(candidates, filepath.Join(d, CatalogName))
		}
	}
	if config != "" {
		candidates = append(candidates, filepath.Join(config, "nhm", CatalogName))
	}
	if acfs != "" {
		candidates = append(candidates, filepath.Join(acfs, "onboard", "docs", "ntm", CatalogName))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		st, err := os.Stat(c)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		real, err := filepath.EvalSymlinks(c)
		if err != nil {
			real = c
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, c)
	}
	return out
}

// LoadCommands reads the catalogs in order; a key that an earlier file
// gave is skipped. A file that does not read is an error in errs, and
// the other files still load.
func LoadCommands(paths []string) (cmds []PaletteCmd, errs []error) {
	seen := map[string]bool{}
	for _, p := range paths {
		entries, err := templates.ReadCatalog(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			if seen[e.Key] {
				continue
			}
			seen[e.Key] = true
			label := e.Label
			if label == "" {
				label = e.Key
			}
			cmds = append(cmds, PaletteCmd{Key: e.Key, Label: label, Prompt: e.Body, Category: e.Category, Source: p})
		}
	}
	return cmds, errs
}
