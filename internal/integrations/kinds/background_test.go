package kinds

import (
	"strings"
	"testing"
)

// claudeBox is Claude Code 2.1.316's prompt box and footer, as the
// sandbox showed it (nhm-nng3); footer is the line below the box.
func claudeBox(footer string, below ...string) string {
	rule := strings.Repeat("─", 60)
	return strings.Join(append([]string{rule, "❯", rule, footer}, below...), "\n") + "\n"
}

// The screens of Claude Code 2.1.316 with a background agent, from the
// bwrap sandbox and the dive panes (2026-11-02).
func TestClaudeBackgroundAgents(t *testing.T) {
	waiting := "● Agent(bg probe)\n  ⎿  Backgrounded agent (↓ to manage · ctrl+o to expand)\n\n" +
		"● mock: read 1 tool result(s)\n\n✻ Waiting for 1 background agent to finish\n\n\n\n"
	panel := []string{"", "  ● main", "  ◯ general-purpose  bg probe          10s · ↓ 73 tokens"}
	for _, c := range []struct {
		name    string
		screen  string
		n       int
		waiting bool
	}{
		{"the turn waits for its background agent", waiting + claudeBox("  ⏵⏵ bypass permissions on (shift+tab to cycle) · ↓ to manage", panel...), 1, true},
		{"two agents", strings.Replace(waiting, "Waiting for 1 background agent", "Waiting for 2 background agents", 1) +
			claudeBox("  ⏵⏵ bypass permissions on · ↓ to manage"), 2, true},
		// The other forms of the line in Claude Code 2.1.316's renderer
		// (read by a reviewer from the binary).
		{"agents and a dynamic workflow", strings.Replace(waiting, "1 background agent to finish", "1 background agent and 1 dynamic workflow to finish", 1) +
			claudeBox("  ⏵⏵ bypass permissions on · ↓ to manage"), 1, true},
		{"a budget after it", strings.Replace(waiting, "to finish", "to finish · 12.3k / 20k (61%)", 1) +
			claudeBox("  ⏵⏵ bypass permissions on · ↓ to manage"), 1, true},
		{"hidden messages after it", strings.Replace(waiting, "to finish", "to finish · 3 messages hidden (/focus to show)", 1) +
			claudeBox("  ⏵⏵ bypass permissions on · ↓ to manage"), 1, true},
		{"a word that only starts like it", strings.Replace(waiting, "1 background agent to finish", "1 background agentless job", 1) +
			claudeBox("  ⏵⏵ bypass permissions on"), 0, false},
		{"the effort line and a queued /exit below the wait", waiting + "❯ /exit\n\n" + strings.Repeat(" ", 60) + "◉ xhigh · /effort\n" +
			claudeBox("  ⏵⏵ bypass permissions on · ↓ to manage", panel...), 1, true},
		{"the agent finished after the wait", waiting + "● Agent \"bg probe\" finished · 3s\n\n● mock reply 3\n\n✻ Cogitated for 6s\n" +
			claudeBox("  ⏵⏵ bypass permissions on (shift+tab to cycle) · /tasks to see subagents"), 0, false},
		{"a main agent at work with one", "✶ Marinating… (17m 41s · ↓ 71.6k tokens · thinking with xhigh effort)\n" +
			"  ⎿  Tip: Use /btw to ask a quick side question\n\n" + claudeBox("  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 1 agent"), 1, false},
		{"three at work", claudeBox("  ⏵⏵ bypass permissions on · ← 3 agents"), 3, false},
		{"the footer's agents key without a count", claudeBox("  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt · ← for agents"), 0, false},
		{"a quoted wait in the agent's answer", "● The pane said:\n  ✻ Waiting for 1 background agent to finish\n\n" + claudeBox("  ⏵⏵ bypass permissions on"), 0, false},
		{"no prompt box", "✻ Waiting for 1 background agent to finish\n", 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if n, w := BackgroundAgents("claude", c.screen); n != c.n || w != c.waiting {
				t.Fatalf("BackgroundAgents = %d, %v; want %d, %v", n, w, c.n, c.waiting)
			}
		})
	}
	if n, w := BackgroundAgents("codex", waiting+claudeBox("  · ← 1 agent")); n != 0 || w {
		t.Fatalf("codex: %d, %v", n, w)
	}
}
