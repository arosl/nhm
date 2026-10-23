package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/core/enrich"
)

// uiSurfaces are the surfaces [ui] in nhm's config named while the fzf
// palette, menu and board were the default.
var uiSurfaces = []string{"menu", "palette", "board"}

// checkUIConfig checks [ui] in nhm's config. The Go surfaces are the only
// ones since the operator accepted them (nhm-qbi.12), so "go", which
// chose them before, is accepted and does nothing:
//
//	[ui]
//	menu = "go"
//	palette = "go"
//	board = "go"
//
// "fzf" is an error that says the fzf surfaces are gone, and so is an
// unknown key or value, so that a typo shows in the popup. A missing file
// or block is no error.
func checkUIConfig() error {
	path, err := enrich.ConfigPath()
	if err != nil {
		// No HOME and no XDG_CONFIG_HOME: no config to check.
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f struct {
		UI map[string]any `toml:"ui"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, key := range slices.Sorted(maps.Keys(f.UI)) {
		s, ok := f.UI[key].(string)
		switch {
		case !slices.Contains(uiSurfaces, key):
			return fmt.Errorf("%s: [ui] takes %v, not %s", path, uiSurfaces, key)
		case ok && s == "fzf":
			return fmt.Errorf("%s: ui.%s is \"fzf\", but nhm no longer has the fzf palette, menu and board: delete the line (the Go %s is the default)",
				path, key, key)
		case !ok || s != "go":
			return fmt.Errorf("%s: ui.%s is \"go\" or not set", path, key)
		}
	}
	return nil
}
