// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/get_all_session_text.go (newGetAllSessionTextCmd,
// paneStatus, sessionStatus, runGetAllSessionText, collectSessionStatus,
// paneIsController, analyzePaneOutput, getAgentTypeShort,
// getLastMeaningfulLine, isOnlyWhitespaceOrControl, detectPaneState, the
// rate-limit and error patterns, detectRateLimit, detectErrors,
// renderCompactTable, renderFullTable, shortState, formatIssues,
// escapeMarkdown) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: the sessions are herdr's workspaces, by
// name, and their panes the workspaces' (core/session.Panes), a pane
// without an agent being ntm's user pane; a pane is read with herdr's
// pane.read (recent), which strips the control sequences; a pane's index
// is its slot's M (0 without a slot); the short type comes from nhm's
// ui/tui/agent (ntm's agent.AgentType); the state words are ntm's
// patterns (core/panestate); "tmux" and "NTM" in the texts read "herdr"
// and "NHM"; the output goes to cobra's writer.
package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/panestate"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

const gastUsage = `Usage: nhm get-all-session-text [--lines N] [--compact] [--socket PATH]
       (also gast, swarm-status)

ntm's swarm overview on herdr, as a markdown table: every herdr
workspace (a session), by name, and each of its panes with its type,
state (WAITING, GENERATING, THINKING, ERROR, STALLED or UNKNOWN, from
ntm's text patterns), last line and issues (rate limits, errors). The
first pane without an agent is the session's controller. --lines (10) is
the lines read per pane; --compact prints one row per session. Markdown
only: --json changes nothing.
`

func newGetAllSessionTextCmd() *cobra.Command {
	var (
		lines   int
		compact bool
		socket  string
	)
	cmd := &cobra.Command{
		Use:     "get-all-session-text",
		Aliases: []string{"gast", "swarm-status"},
		Short:   "Get text from all panes across all sessions as markdown table",
		Long: `Captures output from all panes in all herdr sessions and displays as a markdown table.

Each row represents a session, with columns for:
- Session name
- Controller pane (the user/shell pane) last message and status
- Worker panes (agent panes) last messages and status
- Detected errors (rate limits, crashes, etc.)

This is useful for AI agents orchestrating multiple agent swarms to monitor progress.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetAllSessionText(cmd.OutOrStdout(), socket, lines, compact)
		},
	}
	cmd.Flags().IntVar(&lines, "lines", 10, "Number of lines to capture per pane")
	cmd.Flags().BoolVar(&compact, "compact", false, "Ultra-compact output (one line per session)")
	cmd.Flags().StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// paneStatus holds captured status for a single pane
type paneStatus struct {
	Index     int
	Type      string // cc, cod, gmi, user
	LastLine  string
	State     string // WAITING, GENERATING, THINKING, ERROR, UNKNOWN
	Errors    []string
	RateLimit bool
	HasError  bool
}

// sessionStatus holds status for an entire session
type sessionStatus struct {
	Name       string
	Controller *paneStatus
	Workers    []*paneStatus
	ErrorCount int
	RateLimits int
}

func runGetAllSessionText(w io.Writer, socket string, lines int, compact bool) error {
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sessions, err := allSessionPanes(ctx, ie)
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(w, "No active herdr sessions found.")
		return nil
	}
	if lines < 0 {
		lines = -lines
	}
	var statuses []sessionStatus
	for _, s := range sessions {
		statuses = append(statuses, collectSessionStatus(ctx, ie, s, lines))
	}
	if compact {
		renderCompactTable(w, statuses)
	} else {
		renderFullTable(w, statuses)
	}
	return nil
}

func collectSessionStatus(ctx context.Context, ie *instanceEnv, s namedPanes, lines int) sessionStatus {
	status := sessionStatus{Name: s.session, Workers: make([]*paneStatus, 0)}
	for _, p := range s.panes {
		captured, err := readPane(ctx, ie.herdr, p.PaneID, lines)
		if err != nil {
			continue
		}
		ps := analyzePaneOutput(p, captured)
		// The controller is the first pane without an agent: ntm's
		// user pane.
		if status.Controller == nil && p.Type == session.UserType {
			status.Controller = ps
		} else {
			status.Workers = append(status.Workers, ps)
		}
		if ps.HasError {
			status.ErrorCount++
		}
		if ps.RateLimit {
			status.RateLimits++
		}
	}
	return status
}

func analyzePaneOutput(p session.Pane, captured string) *paneStatus {
	ps := &paneStatus{Index: p.Index, Type: getAgentTypeShort(p.Type), Errors: make([]string, 0)}
	ps.LastLine = getLastMeaningfulLine(strings.Split(captured, "\n"))
	ps.State = detectPaneState(captured, ps.Type)
	ps.RateLimit = detectRateLimit(captured)
	ps.Errors = detectErrors(captured)
	ps.HasError = ps.RateLimit || len(ps.Errors) > 0 || ps.State == "ERROR"
	return ps
}

// getAgentTypeShort is ntm's short type of a pane: cc, cod, gmi, agy, oc,
// omp, user, else the type itself, else "?".
func getAgentTypeShort(agentType string) string {
	if s := string(agent.AgentType(agentType).Canonical()); s != "" {
		return s
	}
	return "?"
}

func getLastMeaningfulLine(lines []string) string {
	// Work backwards to find non-empty line
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !isOnlyWhitespaceOrControl(line) {
			// Truncate long lines
			if len(line) > 60 {
				return truncateText(line, 60)
			}
			return line
		}
	}
	return "(empty)"
}

func isOnlyWhitespaceOrControl(s string) bool {
	for _, r := range s {
		if r > 32 && r != 127 { // Not whitespace or DEL
			return false
		}
	}
	return true
}

func detectPaneState(output, agentType string) string {
	if match := panestate.MatchFirstPattern(output, agentType); match != nil {
		return string(match.State)
	}
	return "UNKNOWN"
}

// Rate limit patterns
var rateLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)rate\s*limit`),
	regexp.MustCompile(`(?i)you'?ve?\s+hit\s+(?:your\s+)?limit`),
	regexp.MustCompile(`(?i)usage\s+limit\s+reached`),
	regexp.MustCompile(`(?i)too\s+many\s+requests`),
	regexp.MustCompile(`(?i)quota\s+exceeded`),
	regexp.MustCompile(`\b429\b`),
	regexp.MustCompile(`(?i)please\s+try\s+again\s+(?:in|later)`),
}

func detectRateLimit(output string) bool {
	for _, pattern := range rateLimitPatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

// Error detection patterns
var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)error:\s*(.{10,50})`),
	regexp.MustCompile(`(?i)exception:\s*(.{10,50})`),
	regexp.MustCompile(`(?i)panic:\s*(.{10,50})`),
	regexp.MustCompile(`(?i)failed\s+to\s+(.{10,40})`),
	regexp.MustCompile(`SIGSEGV`),
	regexp.MustCompile(`(?i)connection\s+refused`),
	regexp.MustCompile(`(?i)unauthorized`),
	regexp.MustCompile(`(?i)authentication\s+failed`),
}

func detectErrors(output string) []string {
	var errors []string
	seen := make(map[string]bool)

	for _, pattern := range errorPatterns {
		matches := pattern.FindAllStringSubmatch(output, 2) // Max 2 matches per pattern
		for _, match := range matches {
			errMsg := strings.TrimSpace(match[0])
			if len(errMsg) > 50 {
				errMsg = errMsg[:47] + "..."
			}
			if !seen[errMsg] {
				seen[errMsg] = true
				errors = append(errors, errMsg)
			}
		}
	}

	// Limit to 3 errors max
	if len(errors) > 3 {
		errors = errors[:3]
	}

	return errors
}

func renderCompactTable(w io.Writer, statuses []sessionStatus) {
	fmt.Fprintln(w, "## NHM Swarm Status (Compact)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "| Session | Ctrl | Workers | Errors |")
	fmt.Fprintln(w, "|---------|------|---------|--------|")

	for _, s := range statuses {
		ctrlState := "?"
		if s.Controller != nil {
			ctrlState = shortState(s.Controller.State)
			if s.Controller.RateLimit {
				ctrlState = "RATE"
			}
		}

		workerStates := []string{}
		for _, wrk := range s.Workers {
			ws := shortState(wrk.State)
			if wrk.RateLimit {
				ws = "RATE"
			}
			workerStates = append(workerStates, fmt.Sprintf("%s:%s", wrk.Type, ws))
		}
		workerStr := strings.Join(workerStates, " ")
		if workerStr == "" {
			workerStr = "-"
		}

		errStr := fmt.Sprintf("%d", s.ErrorCount)
		if s.RateLimits > 0 {
			errStr = fmt.Sprintf("%d (%d rate)", s.ErrorCount, s.RateLimits)
		}

		fmt.Fprintf(w, "| %s | %s | %s | %s |\n", s.Name, ctrlState, workerStr, errStr)
	}
}

func renderFullTable(w io.Writer, statuses []sessionStatus) {
	fmt.Fprintln(w, "## NHM Swarm Status")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "| Session | Pane | Type | State | Last Message | Issues |")
	fmt.Fprintln(w, "|---------|------|------|-------|--------------|--------|")

	for _, s := range statuses {
		// Controller row
		if s.Controller != nil {
			issues := formatIssues(s.Controller)
			lastMsg := escapeMarkdown(s.Controller.LastLine)
			fmt.Fprintf(w, "| %s | 1 (ctrl) | %s | %s | %s | %s |\n",
				s.Name, s.Controller.Type, s.Controller.State, lastMsg, issues)
		}

		// Worker rows
		for _, wrk := range s.Workers {
			issues := formatIssues(wrk)
			lastMsg := escapeMarkdown(wrk.LastLine)
			fmt.Fprintf(w, "| %s | %d | %s | %s | %s | %s |\n",
				s.Name, wrk.Index, wrk.Type, wrk.State, lastMsg, issues)
		}
	}

	// Summary
	totalSessions := len(statuses)
	totalErrors := 0
	totalRateLimits := 0
	for _, s := range statuses {
		totalErrors += s.ErrorCount
		totalRateLimits += s.RateLimits
	}

	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "**Summary:** %d sessions, %d errors, %d rate-limited\n", totalSessions, totalErrors, totalRateLimits)
}

func shortState(state string) string {
	switch state {
	case "WAITING":
		return "WAIT"
	case "GENERATING":
		return "GEN"
	case "THINKING":
		return "THINK"
	case "ERROR":
		return "ERR"
	case "STALLED":
		return "STAL"
	default:
		return "?"
	}
}

func formatIssues(ps *paneStatus) string {
	issues := []string{}
	if ps.RateLimit {
		issues = append(issues, "RATE_LIMIT")
	}
	issues = append(issues, ps.Errors...)
	if len(issues) == 0 {
		return "-"
	}
	return strings.Join(issues, "; ")
}

func escapeMarkdown(s string) string {
	// Escape pipe characters for markdown tables
	s = strings.ReplaceAll(s, "|", "\\|")
	// Remove newlines
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
