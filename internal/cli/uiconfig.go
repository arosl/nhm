package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/BurntSushi/toml"

	"github.com/arosl/nhm/internal/core/enrich"
)

// The engines of a surface. Until a Go surface passes its acceptance, the
// operator opens it by choice ([ui] in nhm's config, or --go); then the
// default becomes "go" and the fzf and gum code goes (plan-v4 sections 0
// and 10).
const (
	engineFzf = "fzf"
	engineGo  = "go"
)

// uiSurfaces are the surfaces [ui] switches.
var uiSurfaces = []string{"menu", "palette", "board"}

// surfaceEngine is the engine nhm's config sets for a surface:
//
//	[ui]
//	menu = "go"      # default "fzf"
//	palette = "go"
//	board = "go"
//
// A missing file or key is the default. An unknown key or value is an
// error, so that a typo shows in the popup instead of opening the other
// engine.
func surfaceEngine(surface string) (string, error) {
	if !slices.Contains(uiSurfaces, surface) {
		return "", fmt.Errorf("[ui] has no surface %q", surface)
	}
	path, err := enrich.ConfigPath()
	if err != nil {
		return engineFzf, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return engineFzf, nil
	}
	if err != nil {
		return "", err
	}
	var f struct {
		UI map[string]any `toml:"ui"`
	}
	if _, err := toml.Decode(string(data), &f); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	engine := engineFzf
	for key, v := range f.UI {
		s, ok := v.(string)
		switch {
		case !slices.Contains(uiSurfaces, key):
			return "", fmt.Errorf("%s: [ui] takes %v, not %s", path, uiSurfaces, key)
		case !ok || s != engineFzf && s != engineGo:
			return "", fmt.Errorf("%s: ui.%s is %q or %q", path, key, engineGo, engineFzf)
		case key == surface:
			engine = s
		}
	}
	return engine, nil
}
