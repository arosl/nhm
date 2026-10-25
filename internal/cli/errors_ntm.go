// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/errors.go
// (ErrorEntry, ErrorsResult and its Text, ErrorsOptions, newErrorsCmd,
// runErrors) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: a session is a herdr workspace (nhm's
// resolveSession) and its panes are the workspace's (core/session.Panes),
// a pane without an agent being ntm's user pane; herdr has no service
// panes to skip; a --panes index is a pane index under the operator's
// ruling of 2026-10-24 (pane M of the session's agent tab); a pane is
// read with herdr's pane.read (recent); the agent type is the pane's ntm
// type; the times are in UTC; the pattern table is in core/panediff; the
// text is without ntm's colours; the output goes to cobra's writers.
package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/panediff"
	"github.com/arosl/nhm/internal/core/session"
)

const errorsUsage = `Usage: nhm errors [session] [--panes N,…] [-C N] [-n N] [--cc] [--cod] [--gmi] [--agy]
                 [--socket PATH] [--json]

ntm's errors on herdr: the error lines in the recent output of a
session's agent panes (tracebacks, panics, error, fatal and critical
lines, failed builds and tests, non-zero exits, rate and context limits),
each with -C (2) lines of context. The session is a herdr workspace (its
label or id); without one, nhm infers it as ntm does. Every pane but the
shells is searched, unless the type flags name the agents; --panes keeps
the pane indexes given (pane M of the session's agent tab). -n (1000) is
the lines read per pane. Errors found are not a failure: exit 0.
`

// ErrorEntry is ntm's ErrorEntry: one error line of a pane.
type ErrorEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session"`
	Pane      string    `json:"pane"`
	PaneIndex int       `json:"pane_index"`
	Line      int       `json:"line"`
	Content   string    `json:"content"`
	MatchType string    `json:"match_type"`
	Context   []string  `json:"context,omitempty"`
	AgentType string    `json:"agent_type,omitempty"`
}

// ErrorsResult is ntm's ErrorsResult.
type ErrorsResult struct {
	Session     string       `json:"session"`
	Errors      []ErrorEntry `json:"errors"`
	TotalErrors int          `json:"total_errors"`
	TotalLines  int          `json:"total_lines_searched"`
	PaneCount   int          `json:"panes_searched"`
	Timestamp   time.Time    `json:"timestamp"`
}

// Text prints the result as ntm does, without its colours.
func (r *ErrorsResult) Text(w io.Writer) {
	if len(r.Errors) == 0 {
		fmt.Fprintf(w, "✓ No errors found in session '%s'\n", r.Session)
		return
	}
	fmt.Fprintf(w, "\n%d error(s) found in session '%s'\n\n", r.TotalErrors, r.Session)
	lastPane := ""
	for i, e := range r.Errors {
		if e.Pane != lastPane {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "─── %s (%s) ───\n", e.Pane, e.AgentType)
			lastPane = e.Pane
		}
		before := len(e.Context) / 2
		for j := 0; j < before && j < len(e.Context); j++ {
			if n := e.Line - before + j; n > 0 {
				fmt.Fprintf(w, "%d- %s\n", n, e.Context[j])
			}
		}
		fmt.Fprintf(w, "%d: %s [%s]\n", e.Line, e.Content, e.MatchType)
		for j := before; j < len(e.Context); j++ {
			fmt.Fprintf(w, "%d- %s\n", e.Line+j-before+1, e.Context[j])
		}
	}
	fmt.Fprintf(w, "\nSearched %d pane(s), %d line(s)\n", r.PaneCount, r.TotalLines)
}

// errorsOptions are errors' flags (ntm's ErrorsOptions).
type errorsOptions struct {
	Panes        []int
	ContextLines int
	MaxLines     int
	Filter       session.TypeFilter
}

func newErrorsCmd() *cobra.Command {
	var (
		opts   errorsOptions
		panes  string
		socket string
	)
	cmd := &cobra.Command{
		Use:   "errors [session-name]",
		Short: "Show only error output from agents",
		Long: `Filter and display only error-related output from agent panes.

Searches pane output for error patterns including:
- Exception tracebacks (Python, JavaScript, Go, Rust)
- Error/FATAL/CRITICAL messages
- Build and test failures
- Non-zero exit codes
- Rate limit and context errors`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				name = args[0]
			}
			opts.Panes = nil
			if panes != "" {
				for _, p := range strings.Split(panes, ",") {
					p = strings.TrimSpace(p)
					if p == "" {
						continue
					}
					idx, err := strconv.Atoi(p)
					if err != nil {
						return fmt.Errorf("invalid pane index '%s': %w", p, err)
					}
					opts.Panes = append(opts.Panes, idx)
				}
			}
			return runErrors(cmd, socket, name, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&panes, "panes", "", "Comma-separated pane indices to filter")
	fl.IntVarP(&opts.ContextLines, "context", "C", 2, "Lines of context before and after each error")
	fl.IntVarP(&opts.MaxLines, "max-lines", "n", 1000, "Search last N lines per pane")
	fl.BoolVar(&opts.Filter.Claude, "cc", false, "Show Claude pane errors only")
	fl.BoolVar(&opts.Filter.Codex, "cod", false, "Show Codex pane errors only")
	fl.BoolVar(&opts.Filter.Gemini, "gmi", false, "Show Gemini pane errors only")
	fl.BoolVar(&opts.Filter.Antigravity, "agy", false, "Show Antigravity pane errors only")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

func runErrors(cmd *cobra.Command, socket, name string, opts errorsOptions) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sp, err := loadPaneSet(ctx, ie, name, wantJSON(cmd), stdout, stderr)
	if err != nil || sp.res.Session == "" {
		return err
	}
	sessionName := sp.res.Session
	// A pane index is the slot M=K of the agent tab (the 2026-10-24 ruling).
	var slots map[string]bool
	if len(opts.Panes) > 0 {
		slots = map[string]bool{}
		for _, idx := range opts.Panes {
			t, err := sp.paneSpec(strconv.Itoa(idx))
			if err != nil {
				return err
			}
			slots[t.Slot] = true
		}
	}

	var entries []ErrorEntry
	totalLines, panesSearched := 0, 0
	for _, p := range sp.panes {
		if slots != nil && (p.Slot == "" || !slots[p.Slot]) {
			continue
		}
		if opts.Filter.IsEmpty() {
			if p.Type == session.UserType {
				continue
			}
		} else if !opts.Filter.Matches(p.Type) {
			continue
		}
		output, err := readPane(ctx, ie.herdr, p.PaneID, opts.MaxLines)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: failed to capture pane %s: %v\n", p.Title, err)
			continue
		}
		lines := strings.Split(output, "\n")
		totalLines += len(lines)
		panesSearched++
		for i, line := range lines {
			isError, matchType := panediff.IsErrorLine(line)
			if !isError {
				continue
			}
			var ctxLines []string
			for j := i - opts.ContextLines; j < i && j >= 0; j++ {
				ctxLines = append(ctxLines, lines[j])
			}
			for j := i + 1; j <= i+opts.ContextLines && j < len(lines); j++ {
				ctxLines = append(ctxLines, lines[j])
			}
			entries = append(entries, ErrorEntry{Timestamp: time.Now().UTC(), Session: sessionName, Pane: p.Title,
				PaneIndex: p.Index, Line: i + 1, Content: line, MatchType: matchType, Context: ctxLines, AgentType: p.Type})
		}
	}

	result := &ErrorsResult{Session: sessionName, Errors: entries, TotalErrors: len(entries), TotalLines: totalLines,
		PaneCount: panesSearched, Timestamp: time.Now().UTC()}
	if wantJSON(cmd) {
		return encodeJSONIndent(stdout, result)
	}
	result.Text(stdout)
	return nil
}
