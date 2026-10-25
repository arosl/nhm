// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/colors.go (colorize, colorToRGB, hexToRGB); see LICENSE in
// this directory and PROVENANCE.md. Local changes: the colours are nhm's
// theme (internal/ui/tui/theme, ported from ntm's); formatBytes and
// formatCleanupBytes left out.
package cli

import "fmt"

// colorize is ntm's ANSI escape for a theme colour: a 24-bit foreground,
// written whether or not stdout is a terminal, as ntm writes it.
func colorize(c interface{}) string {
	return fmt.Sprintf("\033[38;2;%s", colorToRGB(c))
}

func colorToRGB(c interface{}) string {
	s := fmt.Sprintf("%v", c)
	if len(s) == 7 && s[0] == '#' {
		r, g, b := hexToRGB(s)
		return fmt.Sprintf("%d;%d;%dm", r, g, b)
	}
	return "255;255;255m" // white, as ntm's default
}

func hexToRGB(hex string) (int, int, int) {
	var r, g, b int
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return r, g, b
}
