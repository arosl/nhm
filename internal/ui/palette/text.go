package palette

import (
	"regexp"
	"strings"
)

// ansiPattern matches CSI and OSC sequences and lone escapes (as
// internal/cli's read does).
var ansiPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// clean removes terminal controls from text the palette shows: catalog,
// pane, xf and core text (plan-v4 section 4). Newlines and tabs stay.
func clean(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f) {
			return r
		}
		return -1
	}, s)
}
