// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/agent/patterns.go (OmpComposer, ParseOmpComposer,
// parseOmpComposerAt, the omp composer patterns, ansiPattern and
// stripANSICodes) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: the package; only what the dialog
// classifier reads is kept (the box, its draft, the paste token and the
// signs of a running turn), so the provider error, the context gauge, the
// status line and the line indexes are left out, and the names are not
// exported.

package dialogs

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// ompComposerTopRe matches the composer's top border: exactly two rule
	// glyphs after the corner, whitespace, then the status line. Tool-output
	// boxes ("╭────╮"), the banner ("╭─── omp v…") and the model selector
	// ("╭─ Models ─") never have that shape.
	ompComposerTopRe = regexp.MustCompile(`^\s*(?:╭──|\+--)\s+(\S.*)$`)
	// ompComposerBottomRe matches the composer's bottom border, which is also
	// the draft's last input row.
	ompComposerBottomRe = regexp.MustCompile(`^\s*(?:╰─|\+-)(.*?)(?:─╯|-\+)\s*$`)
	// ompComposerRowRe matches a bordered continuation row of the draft.
	ompComposerRowRe = regexp.MustCompile(`^\s*(?:│|\|)(.*?)(?:│|\|)\s*$`)
	// ompBusyStatusRe matches the in-flight status segment: one spinner frame
	// then the elapsed timer ("⠧ 1s", "\ 5s", "⠋ 1m").
	ompBusyStatusRe = regexp.MustCompile(`^\S\s+\d+[smh]\b`)
	// ompEscHintRe matches the activity line's interrupt hint in every symbol
	// preset, plus the trailing "⟨esc⟩" form earlier omp v18 builds rendered.
	ompEscHintRe = regexp.MustCompile(`^\s*(?:󱊷|⎋|esc)\s+\S|(?:⟨esc⟩|\[esc\])\s*$|^\s*[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*Working…`)
	// ompPasteTokenRe matches a collapsed bracketed-paste token in the draft.
	ompPasteTokenRe = regexp.MustCompile(`(?:^|\s)#\d+(?:\s|$)`)
	// ompPastePreviewFooterRe matches the paste preview box's bottom edge.
	ompPastePreviewFooterRe = regexp.MustCompile(`^\s*(?:╰|\+) \+\d+ lines [─\-]`)
	// ompSteeringHeaderRe matches the pending-steering block header omp draws
	// above the activity line when a message is submitted mid-turn
	// (" Steering · 1", then "   1. <message>" rows and an edit hint).
	ompSteeringHeaderRe = regexp.MustCompile(`^\s*Steering\s+·\s+(\d+)\s*$`)
	// ompSubagentsHeaderRe matches the running-subagents panel header omp
	// draws in the HUD above the activity line while subagents run
	// (" Subagents", then "  └─ • <Name> ⟨<role>⟩ <task>…" rows).
	ompSubagentsHeaderRe = regexp.MustCompile(`^\s*Subagents\s*$`)
	// ompSubagentRowRe matches one running-subagent row under that header.
	ompSubagentRowRe = regexp.MustCompile(`^\s*(?:[├└]─|[|\\+` + "`" + `]-)\s+[•*]\s+\S`)
)

const (
	// ompComposerScanLines bounds how many trailing non-blank rows are searched
	// for the composer's bottom border. The box is bottom-pinned, but the
	// slash-command autocomplete list renders below it.
	ompComposerScanLines = 40
	// ompComposerMaxRows bounds the continuation rows walked between the
	// bottom and top borders (omp caps the editor at 18 rows).
	ompComposerMaxRows = 24
	// ompSteeringScanLines bounds how far above the composer the pending-
	// steering header is searched for (header + queued-message previews +
	// edit hint + activity line).
	ompSteeringScanLines = 16
	// ompSubagentsScanLines bounds how far above the composer the subagents
	// panel header is searched for (header + one row per running subagent +
	// steering block + activity line).
	ompSubagentsScanLines = 24
)

// ompComposer is the parsed live composer box of an omp pane.
type ompComposer struct {
	// found: a structurally complete composer box (bottom border, bordered
	// rows, status-line top border) is visible near the bottom of the capture.
	found bool
	// busy: the top border carries the in-flight spinner + elapsed timer.
	busy bool
	// escHint: the activity line directly above the box carries the Esc
	// interrupt hint.
	escHint bool
	// draft is the unsubmitted composer text (rows joined by "\n"); "" when
	// the composer is empty.
	draft string
	// pasteToken: the draft holds a collapsed bracketed-paste token ("#1")
	// with its preview box drawn above the composer.
	pasteToken bool
	// steering is the count from the pending-steering block ("Steering · N")
	// omp draws above the activity line after a message is submitted while a
	// turn runs; 0 when no steering is pending.
	steering int
	// subagents counts the rows of the running-subagents panel in the HUD
	// above the composer; 0 when no subagent is listed.
	subagents int
}

// working reports whether the composer shows an in-flight turn.
func (c ompComposer) working() bool {
	return c.found && (c.busy || c.escHint || c.steering > 0 || c.subagents > 0)
}

// parseOmpComposer locates and parses the bottom-most omp composer box.
func parseOmpComposer(output string) ompComposer {
	lines := strings.Split(stripANSICodes(output), "\n")
	nonBlankSeen := 0
	for i := len(lines) - 1; i >= 0 && nonBlankSeen < ompComposerScanLines; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		nonBlankSeen++
		bottom := ompComposerBottomRe.FindStringSubmatch(lines[i])
		if bottom == nil {
			continue
		}
		if composer, ok := parseOmpComposerAt(lines, i, bottom[1]); ok {
			return composer
		}
	}
	return ompComposer{}
}

// parseOmpComposerAt validates a candidate bottom border at lines[bottomIdx]
// by walking up through bordered rows to a status-line top border.
func parseOmpComposerAt(lines []string, bottomIdx int, bottomText string) (ompComposer, bool) {
	draftRows := []string{strings.TrimSpace(bottomText)}
	for j := bottomIdx - 1; j >= 0 && bottomIdx-j <= ompComposerMaxRows+1; j-- {
		line := lines[j]
		if top := ompComposerTopRe.FindStringSubmatch(line); top != nil {
			composer := ompComposer{found: true, busy: ompBusyStatusRe.MatchString(strings.TrimSpace(top[1]))}
			// Rows were collected bottom-up; restore reading order and drop
			// blank leading/trailing rows so an empty box reads as "".
			for l, r := 0, len(draftRows)-1; l < r; l, r = l+1, r-1 {
				draftRows[l], draftRows[r] = draftRows[r], draftRows[l]
			}
			composer.draft = strings.TrimSpace(strings.Join(draftRows, "\n"))
			for k := j - 1; k >= 0 && j-k <= 2; k-- {
				above := lines[k]
				if strings.TrimSpace(above) == "" {
					continue
				}
				if ompEscHintRe.MatchString(above) {
					composer.escHint = true
				}
				break
			}
			for k := j - 1; k >= 0 && j-k <= ompSteeringScanLines; k-- {
				if m := ompSteeringHeaderRe.FindStringSubmatch(lines[k]); m != nil {
					if n, err := strconv.Atoi(m[1]); err == nil {
						composer.steering = n
					}
					break
				}
			}
			for k := j - 1; k >= 0 && j-k <= ompSubagentsScanLines; k-- {
				if !ompSubagentsHeaderRe.MatchString(lines[k]) {
					continue
				}
				for r := k + 1; r < j && ompSubagentRowRe.MatchString(lines[r]); r++ {
					composer.subagents++
				}
				break
			}
			if composer.draft != "" && ompPasteTokenRe.MatchString(composer.draft) {
				for k := j - 1; k >= 0 && j-k <= 2; k-- {
					if ompPastePreviewFooterRe.MatchString(lines[k]) {
						composer.pasteToken = true
						break
					}
				}
			}
			return composer, true
		}
		row := ompComposerRowRe.FindStringSubmatch(line)
		if row == nil {
			return ompComposer{}, false
		}
		draftRows = append(draftRows, strings.TrimSpace(row[1]))
	}
	return ompComposer{}, false
}

// ansiPattern matches ANSI escape sequences.
// Matches CSI sequences (with private mode ?) and OSC sequences (title setting etc)
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\a\x1b]*(\a|\x1b\\)`)

// stripANSICodes removes ANSI escape sequences, and keeps only the text
// after a line's last carriage return, as a terminal overwrites the line.
func stripANSICodes(text string) string {
	clean := ansiPattern.ReplaceAllString(text, "")
	if !strings.Contains(clean, "\r") {
		return clean
	}
	lines := strings.Split(clean, "\n")
	for i, line := range lines {
		if lastCR := strings.LastIndex(line, "\r"); lastCR != -1 {
			lines[i] = line[lastCR+1:]
		}
	}
	return strings.Join(lines, "\n")
}
