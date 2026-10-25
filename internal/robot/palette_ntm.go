// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/tui_parity.go (PaletteOutput, PaletteCmd, PaletteRecent,
// PaletteOptions, GetPalette, hasPaletteKey) and internal/robot/types.go
// (AgentHints, with its paging fields for --robot-status and
// --robot-snapshot, nhm-2p6.2.4, and without its action fields, which
// nhm never sets) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: GetPalette takes nhm's palette (its
// catalog commands, pins and favorites) as PaletteSource, not ntm's
// config; ntm's kernel command registry and its xf entry have no nhm
// source; recent and use_count come from PaletteSource, which nhm's
// palette cannot fill yet (its history entries do not name the key).
package robot

import (
	"fmt"
	"strings"
	"time"
)

// PaletteOutput is ntm's palette state and commands (--robot-palette,
// palette --json).
type PaletteOutput struct {
	RobotResponse
	Session    string          `json:"session,omitempty"`
	Commands   []PaletteCmd    `json:"commands"`
	Favorites  []string        `json:"favorites"`
	Pinned     []string        `json:"pinned"`
	Recent     []PaletteRecent `json:"recent"`
	Categories []string        `json:"categories"`
	AgentHints *AgentHints     `json:"_agent_hints,omitempty"`
}

// PaletteCmd is one palette command.
type PaletteCmd struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Category   string   `json:"category"`
	Prompt     string   `json:"prompt"`
	Targets    string   `json:"targets,omitempty"`
	IsFavorite bool     `json:"is_favorite"`
	IsPinned   bool     `json:"is_pinned"`
	UseCount   int      `json:"use_count"`
	Tags       []string `json:"tags,omitempty"`
}

// PaletteRecent is a recently used command.
type PaletteRecent struct {
	Key     string `json:"key"`
	UsedAt  string `json:"used_at"` // RFC3339
	Session string `json:"session"`
	Success bool   `json:"success"`
}

// AgentHints are ntm's notes for an AI reader of a robot answer.
type AgentHints struct {
	Summary        string   `json:"summary,omitempty"`
	NextOffset     *int     `json:"next_offset,omitempty"`
	PagesRemaining *int     `json:"pages_remaining,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	Notes          []string `json:"notes,omitempty"`
}

// PaletteOptions filter the palette: the recents of one session, the
// commands of a category or that match a search.
type PaletteOptions struct {
	Session     string
	Category    string
	SearchQuery string
}

// PaletteSource is the palette as nhm keeps it: the catalogs' commands in
// order, the pins and favorites, and the recents (newest first) and use
// counts when there are any. RecentWarning says why the recents are
// missing.
type PaletteSource struct {
	Commands      []PaletteCmd
	Favorites     []string
	Pinned        []string
	Recent        []PaletteRecent
	UseCounts     map[string]int
	RecentWarning string
}

// GetPalette is ntm's palette answer: the commands that pass the filters,
// each with its favorite and pin marks; the favorites and pins among
// them, in the state's order; their categories in first-seen order; at
// most 8 recents of the shown commands (of the session, when one is
// given).
func GetPalette(src PaletteSource, opts PaletteOptions, now time.Time) *PaletteOutput {
	favorites := map[string]struct{}{}
	for _, k := range src.Favorites {
		if k = strings.TrimSpace(k); k != "" {
			favorites[k] = struct{}{}
		}
	}
	pinned := map[string]struct{}{}
	for _, k := range src.Pinned {
		if k = strings.TrimSpace(k); k != "" {
			pinned[k] = struct{}{}
		}
	}
	out := &PaletteOutput{
		RobotResponse: NewResponse(true, "json", now),
		Session:       opts.Session,
		Commands:      []PaletteCmd{},
		Favorites:     []string{},
		Pinned:        []string{},
		Recent:        []PaletteRecent{},
		Categories:    []string{},
	}
	visible := map[string]struct{}{}
	categories := map[string]struct{}{}
	query := strings.ToLower(opts.SearchQuery)
	matches := func(key, label, category string) bool {
		if opts.Category != "" && category != opts.Category {
			return false
		}
		if opts.SearchQuery == "" {
			return true
		}
		return strings.Contains(strings.ToLower(label), query) || strings.Contains(strings.ToLower(key), query)
	}
	for _, c := range src.Commands {
		if !matches(c.Key, c.Label, c.Category) {
			continue
		}
		_, c.IsFavorite = favorites[c.Key]
		_, c.IsPinned = pinned[c.Key]
		c.UseCount = src.UseCounts[c.Key]
		out.Commands = append(out.Commands, c)
		if c.Key != "" {
			visible[c.Key] = struct{}{}
		}
		if cat := strings.TrimSpace(c.Category); cat != "" {
			if _, ok := categories[cat]; !ok {
				categories[cat] = struct{}{}
				out.Categories = append(out.Categories, cat)
			}
		}
	}
	for _, k := range src.Favorites {
		if _, ok := visible[strings.TrimSpace(k)]; ok {
			out.Favorites = append(out.Favorites, strings.TrimSpace(k))
		}
	}
	for _, k := range src.Pinned {
		if _, ok := visible[strings.TrimSpace(k)]; ok {
			out.Pinned = append(out.Pinned, strings.TrimSpace(k))
		}
	}
	summary := fmt.Sprintf("%d commands available across %d categories", len(out.Commands), len(out.Categories))
	notes := []string{"Use --robot-send with a prompt to send commands to agents"}
	if src.RecentWarning != "" {
		out.AgentHints = &AgentHints{Summary: summary, Warnings: []string{src.RecentWarning}, Notes: notes}
		return out
	}
	seen := map[string]struct{}{}
	for _, r := range src.Recent {
		if opts.Session != "" && r.Session != opts.Session {
			continue
		}
		if _, ok := visible[r.Key]; !ok {
			continue
		}
		if _, dup := seen[r.Key]; dup {
			continue
		}
		seen[r.Key] = struct{}{}
		out.Recent = append(out.Recent, r)
		if len(out.Recent) >= 8 {
			break
		}
	}
	out.AgentHints = &AgentHints{Summary: summary, Notes: notes}
	return out
}
