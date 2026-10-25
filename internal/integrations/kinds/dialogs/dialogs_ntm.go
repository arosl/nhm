// Ported from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs.go (the dialog classes, DialogOption,
// DialogState, dialogSignatures, destructiveEvidence, acceptSideLabel,
// declineSideLabel, extractDialogOptions, classifyOmpDialog,
// classifyDialog, firstDialogEvidenceLine and describeOptions) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the package; the names that callers use are exported
// (Classify, Option, State, DescribeOptions) and the others lose their
// "dialog" prefix, the class table being classRows (ntm's
// dialogSignatures); the omp composer is this package's (omp_ntm.go); the
// screen is herdr's visible pane read, not tmux's capture.

// Package dialogs classifies the dialog on an agent's screen as ntm's
// --robot-dialogs does: its class, the options it shows and how they are
// chosen (ntm-xcji). The class table is the providers' dialog vocabulary.
package dialogs

import (
	"fmt"
	"regexp"
	"strings"
)

// Dialog classes reported by the classifier.
const (
	None               = "none"
	TrustPrompt        = "trust_prompt"
	RateLimitOptions   = "rate_limit_options"
	UsageOverlay       = "usage_overlay"
	PasteLimbo         = "paste_limbo"
	DestructiveConfirm = "destructive_confirm"
	Unknown            = "unknown_dialog"
)

// Option is one selectable entry extracted from a dialog. Number is the
// one-based DISPLAY ORDER, including on unnumbered menus. It identifies
// option-K; it is not necessarily a key the provider accepts.
type Option struct {
	Number   int    `json:"number"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	Key      string `json:"key,omitempty"` // explicitly displayed hotkey, if any
}

// State is the classification result for one pane.
type State struct {
	Class     string   `json:"class"`
	Options   []Option `json:"options,omitempty"`
	Evidence  string   `json:"evidence,omitempty"`
	InputMode string   `json:"input_mode,omitempty"` // numbered | cursor | hotkey
}

// classRow declares the substring patterns (case-insensitive, ALL
// required) that identify one dialog class for one agent family. The single
// table IS the provider dialog vocabulary — fixtures pin each row.
type classRow struct {
	class       string
	agent       string // long agent name ("claude", "codex") or "*"
	patterns    []string
	needOptions bool // class requires extracted selectable options
}

var classRows = []classRow{
	// Claude and Antigravity folder trust gates, numbered or cursor-driven.
	{class: TrustPrompt, agent: "*", patterns: []string{"trust"}, needOptions: true},
	// Grok's workspace gate does not contain the word "trust" (GH#325).
	{class: TrustPrompt, agent: "grok", patterns: []string{"may run or modify contents", "security risks"}, needOptions: true},
	// Claude rate-limit choice dialog (/rate-limit-options or auto-shown).
	{class: RateLimitOptions, agent: "*", patterns: []string{"usage limit"}, needOptions: true},
	{class: RateLimitOptions, agent: "*", patterns: []string{"rate limit"}, needOptions: true},
	// Usage overlay (claude /usage): a panel, not an option dialog.
	{class: UsageOverlay, agent: "claude", patterns: []string{"current session", "usage"}},
	{class: UsageOverlay, agent: "claude", patterns: []string{"esc to close", "usage"}},
}

// destructiveEvidence flags dialog bodies whose pending action is
// destructive; accept-side answers are refused for these.
var destructiveEvidence = regexp.MustCompile(`(?i)(rm -|rm\b.*-rf|force.?push|git push --force|delete|remove|reset --hard|drop table|overwrite)`)

// acceptSideLabel reports whether an option label accepts/proceeds.
func acceptSideLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, prefix := range []string{"yes", "allow", "proceed", "accept", "trust", "continue", "confirm", "run "} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// declineSideLabel reports whether an option label declines/cancels.
func declineSideLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, prefix := range []string{"no", "don't", "dont", "cancel", "decline", "stop", "exit", "skip"} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

var (
	optionPattern = regexp.MustCompile(`^\s*([❯>]\s*)?(\d+)\.\s+(.+?)\s*$`)
	// A hotkey must be a separate column, not the last letter of a label.
	hotkeyPattern = regexp.MustCompile(`^(.+?\S)[\t ]{2,}([a-zA-Z])\s*$`)
	plainLabel    = regexp.MustCompile(`(?i)^(yes\b|no\b|allow\b|proceed\b|accept\b|trust\b|continue\b|confirm\b|run\b|don't\b|dont\b|cancel\b|decline\b|stop\b|exit\b|skip\b|wait for\b|use extra\b|upgrade\b)`)
)

// extractOptions accepts only the LAST compact option block. Requiring a
// live footer (or a displayed hotkey pair) for unnumbered menus prevents
// arbitrary yes/no prose, quoted dialogs, and old menus above a live composer
// from being treated as actionable. A numbered block must still run 1..n.
func extractOptions(capture string) ([]Option, string) {
	lines := strings.Split(strings.TrimRight(capture, "\r\n \t"), "\n")
	var options, candidate []Option
	mode, candidateMode := "", ""
	end := -1
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		var option Option
		kind := ""
		if match := optionPattern.FindStringSubmatch(line); match != nil {
			fmt.Sscanf(match[2], "%d", &option.Number)
			option.Label = strings.TrimSpace(match[3])
			option.Selected = strings.TrimSpace(match[1]) != ""
			kind = "numbered"
		} else {
			for _, marker := range []string{"❯", ">"} {
				if strings.HasPrefix(text, marker+" ") || strings.HasPrefix(text, marker+"\t") {
					option.Selected = true
					text = strings.TrimSpace(strings.TrimPrefix(text, marker))
					break
				}
			}
			option.Label = text
			kind = "cursor"
			if match := hotkeyPattern.FindStringSubmatch(text); match != nil {
				option.Label = strings.TrimSpace(match[1])
				option.Key = match[2]
				kind = "hotkey"
			}
			if !plainLabel.MatchString(option.Label) {
				kind = ""
			}
		}
		if kind == "" {
			candidate = nil
			candidateMode = ""
			continue
		}
		if candidateMode != kind || (kind == "numbered" && option.Number == 1) {
			candidate = nil
		}
		candidateMode = kind
		if kind != "numbered" {
			option.Number = len(candidate) + 1
		}
		candidate = append(candidate, option)
		options, mode, end = candidate, kind, i
	}
	if len(options) == 0 || len(options) > 9 {
		return nil, ""
	}
	for i, option := range options {
		if option.Number != i+1 {
			return nil, ""
		}
	}
	// Only known dialog chrome may follow a menu. In particular, a new
	// composer, agent response, or closing Markdown fence invalidates it.
	footer := false
	for _, line := range lines[end+1:] {
		text := strings.ToLower(strings.TrimSpace(line))
		if text == "" {
			continue
		}
		if (strings.Contains(text, "enter") && strings.Contains(text, "confirm")) ||
			(strings.Contains(text, "esc") && strings.Contains(text, "cancel")) ||
			(strings.Contains(text, "↑/↓") && strings.Contains(text, "navigate")) {
			footer = true
			continue
		}
		return nil, ""
	}
	if mode == "numbered" {
		return options, mode
	}
	if len(options) < 2 || (mode == "cursor" && !footer) {
		return nil, ""
	}
	if mode == "hotkey" {
		seen := make(map[string]bool, len(options))
		for _, option := range options {
			key := strings.ToLower(option.Key)
			if seen[key] || option.Selected {
				return nil, ""
			}
			seen[key] = true
		}
	}
	return options, mode
}

// ompSelectorFooterRe matches the key-hint footer of an omp selector overlay
// (the /model picker renders "Enter assign roles · ↑/↓ providers · … · Esc
// close" as its last bordered row, and the overlay replaces the composer).
var ompSelectorFooterRe = regexp.MustCompile(`(?i)\besc\s+(?:close|cancel|back)\b`)

// ompSelectorScanLines bounds the footer search to the overlay's bottom rows.
const ompSelectorScanLines = 6

// classifyOmp recognises omp-specific blocking states from a live capture
// (omp v18.2.3): a collapsed bracketed paste ("#N" token with its "+N
// lines" preview box) sitting in an idle composer, and a selector overlay
// that has replaced the composer. It reports ok=false when neither applies
// so the shared classifier runs.
func classifyOmp(capture string) (State, bool) {
	composer := parseOmpComposer(capture)
	if composer.found {
		if composer.pasteToken && !composer.working() {
			return State{Class: PasteLimbo, Evidence: composer.draft}, true
		}
		return State{}, false
	}
	lines := strings.Split(capture, "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < ompSelectorScanLines; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if ompSelectorFooterRe.MatchString(line) {
			return State{Class: Unknown, Evidence: strings.Trim(line, " │┃|")}, true
		}
	}
	return State{}, false
}

// Classify inspects one pane's screen and returns its dialog state.
// agentType is ntm's agent type of the pane ("claude", "codex", "omp").
func Classify(capture string, agentType string) State {
	if agentType == "omp" {
		if state, ok := classifyOmp(capture); ok {
			return state
		}
	}
	lower := strings.ToLower(capture)
	options, inputMode := extractOptions(capture)

	// codex paste limbo: staged "[Pasted text ...]" sitting in the live
	// composer with no working footer — Enter was consumed by the paste.
	if agentType == "codex" && !strings.Contains(lower, "esc to interrupt") {
		if lines := strings.Split(capture, "\n"); len(lines) > 0 {
			for i := len(lines) - 1; i >= 0; i-- {
				idx := strings.Index(lines[i], "›")
				if idx < 0 {
					continue
				}
				if strings.Contains(lines[i][idx:], "[Pasted") {
					return State{Class: PasteLimbo, Evidence: strings.TrimSpace(lines[i])}
				}
				break
			}
		}
	}

	for _, row := range classRows {
		if row.agent != "*" && row.agent != agentType {
			continue
		}
		matched := true
		for _, pattern := range row.patterns {
			if !strings.Contains(lower, pattern) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if row.needOptions && len(options) == 0 {
			continue
		}
		state := State{Class: row.class, Options: options, InputMode: inputMode, Evidence: firstEvidenceLine(capture, row.patterns)}
		// A trust/rate-limit style dialog whose body shows destructive
		// evidence is reclassified: the pending action dominates.
		if destructiveEvidence.MatchString(capture) && len(options) > 0 && row.class != UsageOverlay {
			state.Class = DestructiveConfirm
		}
		return state
	}

	// Generic confirm dialog: options with an accept/decline shape.
	if len(options) >= 2 {
		hasAccept, hasDecline := false, false
		for _, opt := range options {
			if acceptSideLabel(opt.Label) {
				hasAccept = true
			}
			if declineSideLabel(opt.Label) {
				hasDecline = true
			}
		}
		if hasAccept && hasDecline {
			if destructiveEvidence.MatchString(capture) {
				return State{Class: DestructiveConfirm, Options: options, InputMode: inputMode}
			}
			return State{Class: Unknown, Options: options, InputMode: inputMode}
		}
	}
	return State{Class: None}
}

func firstEvidenceLine(capture string, patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	for _, line := range strings.Split(capture, "\n") {
		if strings.Contains(strings.ToLower(line), patterns[0]) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// DescribeOptions lists the options as ntm's messages name them:
// 1="Yes", 2="No"; "none extracted" when there are none.
func DescribeOptions(options []Option) string {
	if len(options) == 0 {
		return "none extracted"
	}
	parts := make([]string, 0, len(options))
	for _, opt := range options {
		parts = append(parts, fmt.Sprintf("%d=%q", opt.Number, opt.Label))
	}
	return strings.Join(parts, ", ")
}
