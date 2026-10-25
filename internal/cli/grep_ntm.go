// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/grep.go
// (GrepMatch, GrepResult and its Text, newGrepCmd, GrepOptions, runGrep,
// outputMatchingPanes) at commit 9d5ed977; see LICENSE in this directory
// and PROVENANCE.md. Local changes: a session is a herdr workspace (nhm's
// resolveSession) and its panes are the workspace's (core/session.Panes),
// a pane without an agent being ntm's user pane; --all searches every
// herdr workspace, by name; a pane is read with herdr's pane.read
// (recent); "tmux" in the texts reads "herdr"; the text is without ntm's
// colours, so highlightMatch is left out; the output goes to cobra's
// writers.
package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
)

const grepUsage = `Usage: nhm grep <pattern> [session] [-i] [-v] [-C N] [-A N] [-B N] [-n N] [-l] [--all]
                [--cc] [--cod] [--gmi] [--agy] [--socket PATH] [--json]

ntm's grep on herdr: search the recent output of a session's panes with
a Go regular expression. The session is a herdr workspace (its label or
id); without one, nhm infers it as ntm does; --all searches every
workspace. Every pane but the shells is searched, unless the type flags
name the agents to search.

-n (1000) is the lines read per pane; -i ignores case; -v keeps the
lines that do not match; -C gives N lines of context on each side, -A
and -B after and before (either one sets -C to 0); -l lists the panes
that match. Each match prints as <session>/<pane title>:<line>: <text>,
its context as <session>/<pane title>:<line>- <text>, with "--" between
panes. No match is not an error: exit 0.
`

// GrepMatch is ntm's GrepMatch: one matching line of a pane.
type GrepMatch struct {
	Session string   `json:"session"`
	Pane    string   `json:"pane"`
	PaneID  string   `json:"pane_id"`
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Context []string `json:"context,omitempty"`
	// ContextBefore and ContextAfter count the lines of Context before
	// and after the match, so that -A or -B alone prints right.
	ContextBefore int `json:"context_before,omitempty"`
	ContextAfter  int `json:"context_after,omitempty"`
}

// GrepResult is ntm's GrepResult.
type GrepResult struct {
	Pattern         string      `json:"pattern"`
	Session         string      `json:"session"`
	Matches         []GrepMatch `json:"matches"`
	TotalLines      int         `json:"total_lines_searched"`
	MatchCount      int         `json:"match_count"`
	PaneCount       int         `json:"panes_searched"`
	CaseInsensitive bool        `json:"case_insensitive,omitempty"`
}

// Text prints the result as ntm does, without its colours.
func (r *GrepResult) Text(w io.Writer) {
	if len(r.Matches) == 0 {
		fmt.Fprintf(w, "No matches found for pattern '%s'\n", r.Pattern)
		return
	}
	lastKey := ""
	for i, m := range r.Matches {
		// "--" between panes.
		key := m.Session + "/" + m.Pane
		if key != lastKey {
			if i > 0 {
				fmt.Fprintln(w, "--")
			}
			lastKey = key
		}
		before := m.ContextBefore
		if before < 0 || before > len(m.Context) || (before == 0 && m.ContextAfter == 0) {
			before = len(m.Context) / 2
		}
		for j := 0; j < before && j < len(m.Context); j++ {
			if n := m.Line - before + j; n > 0 {
				fmt.Fprintf(w, "%s/%s:%d- %s\n", m.Session, m.Pane, n, m.Context[j])
			}
		}
		fmt.Fprintf(w, "%s/%s:%d: %s\n", m.Session, m.Pane, m.Line, m.Content)
		for j := before; j < len(m.Context); j++ {
			fmt.Fprintf(w, "%s/%s:%d- %s\n", m.Session, m.Pane, m.Line+j-before+1, m.Context[j])
		}
	}
	fmt.Fprintf(w, "\n%d matches in %d pane(s)\n", r.MatchCount, r.PaneCount)
}

// grepOptions are grep's flags (ntm's GrepOptions).
type grepOptions struct {
	AllSessions     bool
	CaseInsensitive bool
	InvertMatch     bool
	ContextLines    int
	AfterLines      int
	BeforeLines     int
	MaxLines        int
	ListOnly        bool
	Filter          session.TypeFilter
}

func newGrepCmd() *cobra.Command {
	var (
		opts    grepOptions
		context int
		socket  string
	)
	cmd := &cobra.Command{
		Use:   "grep <pattern> [session-name]",
		Short: "Search pane output with regex",
		Long: `Search across all pane output buffers with regex support.

Searches the captured output from herdr panes for lines matching
the given pattern. Supports standard regex syntax.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 1 {
				name = args[1]
			}
			// -A or -B set the context on their own sides only.
			opts.ContextLines = context
			if opts.AfterLines > 0 || opts.BeforeLines > 0 {
				opts.ContextLines = 0
			}
			return runGrep(cmd, socket, args[0], name, opts)
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&opts.CaseInsensitive, "ignore-case", "i", false, "Case insensitive search")
	fl.BoolVarP(&opts.InvertMatch, "invert-match", "v", false, "Invert match (show non-matching lines)")
	fl.IntVarP(&context, "context", "C", 0, "Show N lines of context")
	fl.IntVarP(&opts.AfterLines, "after-context", "A", 0, "Show N lines after match")
	fl.IntVarP(&opts.BeforeLines, "before-context", "B", 0, "Show N lines before match")
	fl.IntVarP(&opts.MaxLines, "max-lines", "n", 1000, "Search last N lines per pane")
	fl.BoolVarP(&opts.ListOnly, "files-with-matches", "l", false, "List matching panes only")
	fl.BoolVar(&opts.AllSessions, "all", false, "Search all sessions")
	fl.BoolVar(&opts.Filter.Claude, "cc", false, "Search Claude panes only")
	fl.BoolVar(&opts.Filter.Codex, "cod", false, "Search Codex panes only")
	fl.BoolVar(&opts.Filter.Gemini, "gmi", false, "Search Gemini panes only")
	fl.BoolVar(&opts.Filter.Antigravity, "agy", false, "Search Antigravity panes only")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// namedPanes are one session's name and panes.
type namedPanes struct {
	session string
	panes   []session.Pane
}

// allSessionPanes lists every herdr workspace's panes, by session name
// (tmux lists its sessions by name), as ntm's --all searches them.
func allSessionPanes(ctx context.Context, ie *instanceEnv) ([]namedPanes, error) {
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	var out []namedPanes
	for _, w := range snap.Workspaces {
		out = append(out, namedPanes{session: firstOf(w.Label, w.WorkspaceID), panes: session.Panes(snap, w.WorkspaceID, bound)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].session < out[j].session })
	return out, nil
}

func runGrep(cmd *cobra.Command, socket, pattern, name string, opts grepOptions) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	rePattern := pattern
	if opts.CaseInsensitive {
		rePattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(rePattern)
	if err != nil {
		return fmt.Errorf("invalid pattern: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var sessions []namedPanes
	if opts.AllSessions {
		if sessions, err = allSessionPanes(ctx, ie); err != nil {
			return err
		}
		if len(sessions) == 0 {
			return fmt.Errorf("no herdr sessions found")
		}
	} else {
		sp, err := loadPaneSet(ctx, ie, name, wantJSON(cmd), stdout, stderr)
		if err != nil || sp.res.Session == "" {
			return err
		}
		name = sp.res.Session
		sessions = []namedPanes{{session: name, panes: sp.panes}}
	}

	var matches []GrepMatch
	totalLines, panesSearched := 0, 0
	matchingPanes := map[string]bool{}
	for _, s := range sessions {
		for _, p := range s.panes {
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
				if re.MatchString(line) == opts.InvertMatch {
					continue
				}
				matchingPanes[s.session+"/"+p.Title] = true
				if opts.ListOnly {
					continue
				}
				before, after := opts.BeforeLines, opts.AfterLines
				if opts.ContextLines > 0 {
					before, after = opts.ContextLines, opts.ContextLines
				}
				var ctxLines []string
				gotBefore, gotAfter := 0, 0
				for j := i - before; j < i && j >= 0; j++ {
					ctxLines = append(ctxLines, lines[j])
					gotBefore++
				}
				for j := i + 1; j <= i+after && j < len(lines); j++ {
					ctxLines = append(ctxLines, lines[j])
					gotAfter++
				}
				matches = append(matches, GrepMatch{Session: s.session, Pane: p.Title, PaneID: p.PaneID, Line: i + 1,
					Content: line, Context: ctxLines, ContextBefore: gotBefore, ContextAfter: gotAfter})
			}
		}
	}

	if opts.ListOnly {
		return printMatchingPanes(stdout, matchingPanes, wantJSON(cmd))
	}
	result := &GrepResult{Pattern: pattern, Session: name, Matches: matches, TotalLines: totalLines,
		MatchCount: len(matches), PaneCount: panesSearched, CaseInsensitive: opts.CaseInsensitive}
	if opts.AllSessions {
		result.Session = "<all>"
	}
	if wantJSON(cmd) {
		return encodeJSONIndent(stdout, result)
	}
	result.Text(stdout)
	return nil
}

// printMatchingPanes is ntm's -l output: the matching panes, sorted.
func printMatchingPanes(w io.Writer, panes map[string]bool, asJSON bool) error {
	list := make([]string, 0, len(panes))
	for p := range panes {
		list = append(list, p)
	}
	sort.Strings(list)
	if asJSON {
		return encodeJSONIndent(w, map[string]any{"matching_panes": list, "count": len(list)})
	}
	if len(list) == 0 {
		fmt.Fprintln(w, "No matching panes found")
		return nil
	}
	for _, p := range list {
		fmt.Fprintln(w, p)
	}
	return nil
}
