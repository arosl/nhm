package cli

import (
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/panediff"
)

// codeBlocks is ntm's --code-only text: each fenced block as
// "// <language>", its content and a blank line.
func codeBlocks(text string) string {
	var b strings.Builder
	for _, blk := range panediff.ExtractFromText(text) {
		fmt.Fprintf(&b, "// %s\n%s\n\n", blk.Language, blk.Content)
	}
	return b.String()
}
