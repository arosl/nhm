package kinds

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// claudeWaitingLine is Claude Code's line above its prompt box while
	// the main agent's turn waits for its background agents: a status
	// glyph in the first column (2.1.316 draws ✻), then "Waiting for N
	// background agent(s)", which may go on with " and M dynamic
	// workflow(s)", " to finish" and " · " items (a budget, "N messages
	// hidden"). Output that quotes it is indented.
	claudeWaitingLine = regexp.MustCompile(`^[*·✢✶✻✽]\s+Waiting for ([1-9][0-9]*) background agents?\b`)
	// claudeAgentsFooter is Claude Code's footer item for the background
	// agents of a main agent at work: "← 1 agent".
	claudeAgentsFooter = regexp.MustCompile(`←\s*([1-9][0-9]*)\s+agents?\b`)
)

// BackgroundAgents counts the background agents (subagents that run in
// the background) that the last lines of a kind's screen show. waiting
// is true when the main agent's turn waits only for them. Only Claude
// Code shows them; other kinds give 0 (nhm-nng3).
func BackgroundAgents(kind, screen string) (n int, waiting bool) {
	if !ShowsBackgroundAgents(kind) {
		return 0, false
	}
	return claudeBackgroundAgents(screen)
}

// ShowsBackgroundAgents reports whether a kind's screen can show
// background agents (BackgroundAgents), so that a caller reads the
// screen only when it can.
func ShowsBackgroundAgents(kind string) bool {
	return kind == "claude"
}

// claudeBackgroundAgents reads Claude Code 2.1.316's screen (the sandbox
// and the dive panes, 2026-11-02). A main agent whose own work is done
// keeps its turn open while its background agents run, and prints
// "✻ Waiting for N background agent(s) to finish" above its prompt box:
// herdr reads it as working, and the empty box looks idle to a person.
// A main agent at work shows "← N agent(s)" in the footer below its box.
// The box is the lines between the screen's last two rules of ─. A
// waiting line counts only when no newer transcript entry (a line that
// starts with ●, such as "● Agent … finished") is below it.
func claudeBackgroundAgents(screen string) (int, bool) {
	lines := screenLines(screen)
	var rules []int
	for i := len(lines) - 1; i >= 0 && len(rules) < 2; i-- {
		if rule := strings.TrimSpace(lines[i]); rule != "" && strings.Trim(rule, "─") == "" {
			rules = append(rules, i)
		}
	}
	if len(rules) < 2 {
		return 0, false
	}
	top, bottom := rules[1], rules[0]
	for i := top - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "●") {
			break
		}
		if m := claudeWaitingLine.FindStringSubmatch(lines[i]); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, true
		}
	}
	for _, line := range lines[bottom+1:] {
		if m := claudeAgentsFooter.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, false
		}
	}
	return 0, false
}
