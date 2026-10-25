// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/util/text.go (Truncate, SanitizeFilename) at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md. Local changes: the
// package (nhm's cli); Truncate is truncateText, SanitizeFilename
// sanitizeFilename.
package cli

import (
	"strings"
	"unicode/utf8"
)

// sanitizeFilename makes a string safe for use as a filename.
func sanitizeFilename(name string) string {
	// Replace unsafe characters
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
		"%", "_",
		" ", "_",
		".", "_", // Prevent dotfiles and directory traversal
	)
	safe := replacer.Replace(strings.TrimSpace(name))

	// Limit length while respecting UTF-8 boundaries
	if len(safe) > 50 {
		for i := 50; i >= 0; i-- {
			if utf8.RuneStart(safe[i]) {
				return safe[:i]
			}
		}
		return safe[:50]
	}
	return safe
}

// truncateText shortens a string to maxLen with ellipsis.
// Uses three ASCII periods "..." to indicate truncation.
func truncateText(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	// When n too small for content + ellipsis, just return first n chars
	if n <= 3 {
		// Find last rune boundary at or before n bytes
		lastValid := 0
		for i := range s {
			if i > n {
				break
			}
			lastValid = i
		}
		if lastValid == 0 && len(s) > 0 {
			return ""
		}
		return s[:lastValid]
	}
	// Find the last rune boundary that allows for "..." suffix within n bytes.
	targetLen := n - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts are <= targetLen, but string is > n bytes.
	return s[:prevI] + "..."
}
