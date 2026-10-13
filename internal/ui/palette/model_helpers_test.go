// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/palette/model_helpers_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; config.PaletteCmd is this package's.

package palette

// New creates a palette model with default options (test-only convenience
// constructor; production uses NewWithOptions).
func New(session string, commands []PaletteCmd) Model {
	return NewWithOptions(session, commands, Options{})
}
