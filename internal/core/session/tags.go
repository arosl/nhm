// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/tmux/session.go (the tags group of paneNameRegex, parseTags)
// and internal/cli/util.go (HasAnyTag); see LICENSE in this directory and
// PROVENANCE.md. Local changes: the tags end herdr's pane label, where
// ntm reads them from the end of the tmux pane title.
package session

import (
	"regexp"
	"strings"
)

// ntm's pane tags, on herdr (nhm-2p6.5.1): ntm reads them from the end of
// the tmux pane title, "proj__cc_1[frontend,api]"; nhm reads them from
// the end of the herdr pane label, "IcyKnoll opus[frontend,api]", which
// `herdr pane rename` sets. nhm writes a label only where a pane has
// none, so tags that a person adds stay.

var paneTagsPattern = regexp.MustCompile(`\[([^\]]*)\]$`)

// PaneTags are the tags at the end of a pane label, or nil.
func PaneTags(label string) []string {
	m := paneTagsPattern.FindStringSubmatch(strings.TrimRight(label, " \t"))
	if m == nil {
		return nil
	}
	return parseTags(m[1])
}

// parseTags is ntm's: a comma-separated list, each tag trimmed, the empty
// ones dropped; nil for none.
func parseTags(tagStr string) []string {
	if tagStr == "" {
		return nil
	}
	var tags []string
	for _, p := range strings.Split(tagStr, ",") {
		if p = strings.TrimSpace(p); p != "" {
			tags = append(tags, p)
		}
	}
	return tags
}

// HasAnyTag is ntm's tag filter: true when one of the filter tags is one
// of the pane's tags, case-insensitively.
func HasAnyTag(paneTags, filterTags []string) bool {
	for _, ft := range filterTags {
		for _, pt := range paneTags {
			if strings.EqualFold(pt, ft) {
				return true
			}
		}
	}
	return false
}
