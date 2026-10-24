package robot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func paletteSource() PaletteSource {
	return PaletteSource{
		Commands: []PaletteCmd{
			{Key: "fresh_review", Label: "Fresh Review", Category: "Code Review", Prompt: "Read it again."},
			{Key: "git_commit", Label: "Git Commit", Category: "Git", Prompt: "Commit."},
			{Key: "check_mail", Label: "Check Mail", Category: "Agent Coordination", Prompt: "Read mail."},
			{Key: "deep_review", Label: "Deep Review", Category: "Code Review", Prompt: "Look deeper."},
		},
		// The state's order, with a key no catalog has and one with spaces.
		Favorites: []string{"git_commit", "gone", " fresh_review "},
		Pinned:    []string{"check_mail"},
		UseCounts: map[string]int{"git_commit": 3},
	}
}

// ntm's answer: every command with its marks, the favorites and pins in
// the state's order but only the shown ones, the categories in first-seen
// order, and never-null lists.
func TestGetPalette(t *testing.T) {
	out := GetPalette(paletteSource(), PaletteOptions{Session: "s"}, time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC))
	if !out.Success || out.Session != "s" || len(out.Commands) != 4 || fmt.Sprint(out.Favorites) != "[git_commit fresh_review]" ||
		fmt.Sprint(out.Pinned) != "[check_mail]" || fmt.Sprint(out.Categories) != "[Code Review Git Agent Coordination]" {
		t.Fatalf("out = %+v", out)
	}
	if c := out.Commands[1]; !c.IsFavorite || c.IsPinned || c.UseCount != 3 {
		t.Fatalf("git_commit = %+v", c)
	}
	if c := out.Commands[2]; c.IsFavorite || !c.IsPinned {
		t.Fatalf("check_mail = %+v", c)
	}
	if out.AgentHints == nil || out.AgentHints.Summary != "4 commands available across 3 categories" ||
		fmt.Sprint(out.AgentHints.Notes) != "[Use --robot-send with a prompt to send commands to agents]" {
		t.Fatalf("hints = %+v", out.AgentHints)
	}
	data, _ := json.Marshal(out)
	for _, want := range []string{`"recent":[]`, `"timestamp":"2026-10-24T06:00:00Z"`, `"_agent_hints":{`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("json has no %s: %s", want, data)
		}
	}
}

// --category keeps one category; --search matches the label or the key,
// ignoring case; the favorites and categories follow the shown commands.
func TestGetPaletteFilters(t *testing.T) {
	now := time.Now()
	out := GetPalette(paletteSource(), PaletteOptions{Category: "Code Review"}, now)
	if len(out.Commands) != 2 || fmt.Sprint(out.Favorites) != "[fresh_review]" || len(out.Pinned) != 0 || fmt.Sprint(out.Categories) != "[Code Review]" {
		t.Fatalf("category = %+v", out)
	}
	out = GetPalette(paletteSource(), PaletteOptions{SearchQuery: "COMMIT"}, now)
	if len(out.Commands) != 1 || out.Commands[0].Key != "git_commit" {
		t.Fatalf("search label = %+v", out.Commands)
	}
	out = GetPalette(paletteSource(), PaletteOptions{SearchQuery: "check_"}, now)
	if len(out.Commands) != 1 || out.Commands[0].Key != "check_mail" {
		t.Fatalf("search key = %+v", out.Commands)
	}
	if out := GetPalette(paletteSource(), PaletteOptions{Category: "None"}, now); len(out.Commands) != 0 || out.Commands == nil {
		t.Fatalf("no match = %+v", out.Commands)
	}
}

// At most 8 recents, each key once, of shown commands and of the session
// when one is given; a recent warning replaces them.
func TestGetPaletteRecent(t *testing.T) {
	src := paletteSource()
	for i := 0; i < 12; i++ {
		key := []string{"git_commit", "fresh_review", "check_mail", "deep_review", "gone"}[i%5]
		src.Recent = append(src.Recent, PaletteRecent{Key: key, Session: []string{"a", "b"}[i%2], Success: true})
	}
	out := GetPalette(src, PaletteOptions{}, time.Now())
	if len(out.Recent) != 4 {
		t.Fatalf("recent = %+v", out.Recent)
	}
	out = GetPalette(src, PaletteOptions{Session: "a"}, time.Now())
	for _, r := range out.Recent {
		if r.Session != "a" {
			t.Fatalf("session filter let %+v through", r)
		}
	}
	src.RecentWarning = "palette history unavailable: x"
	out = GetPalette(src, PaletteOptions{}, time.Now())
	if len(out.Recent) != 0 || out.AgentHints == nil || fmt.Sprint(out.AgentHints.Warnings) != "[palette history unavailable: x]" {
		t.Fatalf("warning = %+v %+v", out.Recent, out.AgentHints)
	}
}
