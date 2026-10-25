// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/copy.go
// (newCopyCmd, CopyOptions, copyResult, runCopy, filterOutput) and
// internal/cli/redaction_io.go (the warn branch of applyOutputRedaction)
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: a session is a herdr workspace (nhm's resolveSession) and
// its panes are the workspace's (core/session.Panes); the pane after
// "session:" takes nhm's pane forms (ParsePane), an agent's name, or the
// end of a herdr pane id; a pane is read with herdr's pane.read (recent);
// a pane's index in the texts is its slot's M, or its herdr pane id when
// it has no slot; the redaction scan is nhm's (internal/redaction) in
// ntm's default mode, warn, since nhm has no [redaction] config yet
// (nhm-2p6.20); the clipboard is internal/integrations/clipboard: ntm's
// tools, with herdr's OSC 52 where ntm has the tmux buffer, written to the
// calling pane's terminal (herdrTerminal); the output goes to cobra's
// writers, the check mark without colour.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/panediff"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/clipboard"
	"github.com/arosl/nhm/internal/redaction"
)

const copyUsage = `Usage: nhm copy [session[:pane]] [-l|--last N] [-p|--pattern RE] [--code] [--headers]
                [--all|--cc|--cod|--gmi|--agy] [-o|--output FILE] [--quiet] [--socket PATH] [--json]
       (also cp, yank)

ntm's copy on herdr: a pane's recent output, to the clipboard or to a
file (--output). The session is a herdr workspace (its label or id); without one, nhm infers
it as ntm does. The pane after ":" is a pane index (pane M of the
session's agent tab), a slot N.M, a herdr pane id, an agent's name, or
the end of a pane id (pY for wQ:pY). Without a pane, the type flags pick
the panes (--all: every pane, shells too); without those, the session's
active pane, else its first.

--last (1000) is the lines read per pane; --pattern keeps the lines that
match a regular expression; --code keeps the fenced code blocks only,
without pane headers unless --headers. Each pane's text has a header
"═══ <title> (pane N) ═══". A secret in the text is reported on stderr,
and the text is not changed (ntm's default mode, warn).

The clipboard is ntm's first tool found (pbcopy, clip.exe, wl-copy, xclip
or xsel with DISPLAY); inside a herdr pane with none of them, an OSC 52
write that herdr passes to its attached clients' terminals (at most
100000 bytes; with no client attached, herdr drops it).
`

// copyOptions are copy's flags (ntm's CopyOptions).
type copyOptions struct {
	Last     int
	Pattern  string
	Code     bool
	Headers  bool
	Output   string
	Quiet    bool
	PaneSpec string
}

// copyResult is ntm's copyResult.
type copyResult struct {
	Source      string            `json:"source"`
	Panes       []string          `json:"panes"`
	Lines       int               `json:"lines"`
	Bytes       int               `json:"bytes"`
	Destination string            `json:"destination"`
	Pattern     string            `json:"pattern,omitempty"`
	Code        bool              `json:"code,omitempty"`
	OutputPath  string            `json:"output_path,omitempty"`
	Redaction   *RedactionSummary `json:"redaction,omitempty"`
}

func newCopyCmd() *cobra.Command {
	var (
		opts   copyOptions
		filter session.TypeFilter
		socket string
	)
	cmd := &cobra.Command{
		Use:     "copy [session[:pane]]",
		Aliases: []string{"cp", "yank"},
		Short:   "Copy pane output to clipboard",
		Long: `Copy the output from one or more panes to the system clipboard.

By default, captures the last 1000 lines from the selected pane.
Use filters to target specific agent types.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				// session:pane, as ntm splits it: at the first colon.
				name, opts.PaneSpec, _ = strings.Cut(args[0], ":")
			}
			return runCopy(cmd, socket, name, filter, opts)
		},
	}
	fl := cmd.Flags()
	fl.IntVarP(&opts.Last, "last", "l", 1000, "Number of most recent lines to capture")
	fl.StringVarP(&opts.Pattern, "pattern", "p", "", "Regex pattern to filter lines")
	fl.BoolVar(&opts.Code, "code", false, "Copy only code blocks")
	fl.BoolVar(&opts.Headers, "headers", false, "Include pane headers (defaults off when --code is set)")
	fl.BoolVar(&filter.All, "all", false, "Copy from all panes")
	fl.BoolVar(&filter.Claude, "cc", false, "Copy from Claude panes")
	fl.BoolVar(&filter.Codex, "cod", false, "Copy from Codex panes")
	fl.BoolVar(&filter.Gemini, "gmi", false, "Copy from Gemini panes")
	fl.BoolVar(&filter.Antigravity, "agy", false, "Copy from Antigravity panes")
	fl.StringVarP(&opts.Output, "output", "o", "", "Write output to file instead of clipboard")
	fl.BoolVar(&opts.Quiet, "quiet", false, "Suppress confirmation output")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// paneSet is a resolved session's panes and its active bindings.
type paneSet struct {
	res   session.Resolution
	panes []session.Pane
	bound []bindings.Binding
	// activeTab is herdr's active tab of the workspace, for a pane index.
	activeTab string
}

// loadPaneSet resolves the session as ntm's pane verbs do and lists its
// panes. A cancelled picker returns an empty Session.
func loadPaneSet(ctx context.Context, ie *instanceEnv, name string, asJSON bool, stdout, stderr io.Writer) (paneSet, error) {
	var sp paneSet
	r, err := resolveSession(ctx, ie, name, asJSON, stdout, stderr)
	if err != nil || r.Session == "" {
		return sp, err
	}
	sp.res = r
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return sp, err
	}
	sp.bound, err = bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: r.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return sp, err
	}
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == r.WorkspaceID {
			sp.activeTab = w.ActiveTabID
		}
	}
	sp.panes = session.Panes(snap, r.WorkspaceID, sp.bound)
	return sp, nil
}

// paneSpec parses the pane after "session:", with nhm's pane forms.
func (sp paneSet) paneSpec(text string) (*session.PaneTarget, error) {
	if text == "" {
		return nil, nil
	}
	t, err := session.ParsePane(text, session.AgentTabs(sp.bound), sp.activeTab)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// readPane is a pane's last n lines (ntm's CapturePaneOutput: plain text,
// as tmux capture-pane -p prints it).
func readPane(ctx context.Context, h *herdr.Client, paneID string, n int) (string, error) {
	r, err := h.PaneRead(ctx, herdr.PaneReadParams{PaneID: paneID, Source: herdr.ReadRecent, Lines: &n})
	if err != nil {
		return "", err
	}
	return stripControl(r.Text), nil
}

func runCopy(cmd *cobra.Command, socket, name string, filter session.TypeFilter, opts copyOptions) error {
	if opts.Last <= 0 {
		return fmt.Errorf("--last must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	sp, err := loadPaneSet(ctx, ie, name, wantJSON(cmd), stdout, stderr)
	if err != nil || sp.res.Session == "" {
		return err
	}
	sessionName := sp.res.Session
	spec, err := sp.paneSpec(opts.PaneSpec)
	if err != nil {
		return err
	}
	targets := session.CopyTargets(sp.panes, spec, filter)
	if len(targets) == 0 {
		return fmt.Errorf("no matching panes found")
	}

	var re *regexp.Regexp
	if opts.Pattern != "" {
		if re, err = regexp.Compile(opts.Pattern); err != nil {
			return fmt.Errorf("invalid pattern regex: %w", err)
		}
	}

	var outputs, paneLabels []string
	for _, p := range targets {
		output, err := readPane(ctx, ie.herdr, p.PaneID, opts.Last)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: failed to capture pane %s: %v\n", p.Ref(), err)
			continue
		}
		output = filterOutput(output, re, opts.Code)
		if strings.TrimSpace(output) == "" {
			continue
		}
		// Headerless by default in code mode; --headers opts in.
		if !opts.Code || opts.Headers {
			outputs = append(outputs, fmt.Sprintf("═══ %s (pane %s) ═══", p.Title, p.Ref()), output, "")
		} else {
			outputs = append(outputs, output)
		}
		paneLabels = append(paneLabels, paneLabel(sessionName, p))
	}
	if len(outputs) == 0 {
		return fmt.Errorf("no content captured (check filters)")
	}

	combined := strings.Join(outputs, "\n")
	summary := warnOutputRedaction(combined)
	if summary != nil {
		fmt.Fprintf(stderr, "Warning: detected %d potential secret(s) (%s); output is NOT modified (mode=warn)\n",
			summary.Findings, formatRedactionCategoryCounts(summary.Categories))
	}
	trimmed := strings.TrimRight(combined, "\n")
	lineCount := 0
	if trimmed != "" {
		lineCount = strings.Count(trimmed, "\n") + 1
	}

	destination := "clipboard"
	if opts.Output != "" {
		// The side effects come after the redaction check, as in ntm.
		if err := os.MkdirAll(filepath.Dir(opts.Output), 0o755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
		if err := os.WriteFile(opts.Output, []byte(combined), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		destination = "file"
	} else if err := copyToClipboard(ctx, ie, combined); err != nil {
		return err
	}

	source := sessionName
	if opts.PaneSpec != "" {
		source = sessionName + ":" + opts.PaneSpec
	}
	result := copyResult{Source: source, Panes: paneLabels, Lines: lineCount, Bytes: len(combined), Destination: destination,
		Pattern: opts.Pattern, Code: opts.Code, Redaction: summary, OutputPath: opts.Output}
	if wantJSON(cmd) {
		return encodeJSONIndent(stdout, result)
	}
	if !opts.Quiet {
		target := destination
		if destination == "file" {
			target = fmt.Sprintf("file (%s)", opts.Output)
		}
		fmt.Fprintf(stdout, "✓ Copied %d lines from %d pane(s) to %s\n", lineCount, len(paneLabels), target)
	}
	return nil
}

// newClipboard picks the clipboard (ntm's clipboard.New); tests replace
// it.
var newClipboard = func(ctx context.Context, ie *instanceEnv) (clipboard.Clipboard, error) {
	return clipboard.New(herdrTerminal(ctx, ie))
}

// copyToClipboard puts text on the clipboard, with ntm's errors.
func copyToClipboard(ctx context.Context, ie *instanceEnv, text string) error {
	clip, err := newClipboard(ctx, ie)
	if err != nil {
		return fmt.Errorf("failed to init clipboard: %w", err)
	}
	if !clip.Available() {
		return fmt.Errorf("clipboard backend unavailable")
	}
	if err := clip.Copy(text); err != nil {
		return fmt.Errorf("failed to copy to clipboard via %s: %w", clip.Backend(), err)
	}
	return nil
}

// openTTY opens the process's own terminal; tests replace it.
var openTTY = func() (io.WriteCloser, error) { return os.OpenFile("/dev/tty", os.O_WRONLY, 0) }

// herdrTerminal opens the calling herdr pane's terminal, for the
// clipboard's OSC 52 write: the process's own terminal, else the pane's
// tty that herdr names (an agent's tool runs without a terminal).
func herdrTerminal(ctx context.Context, ie *instanceEnv) func() (io.WriteCloser, error) {
	return func() (io.WriteCloser, error) {
		if w, err := openTTY(); err == nil {
			return w, nil
		}
		pane := ie.callerPane(ctx)
		if pane == "" {
			return nil, fmt.Errorf("no terminal, and no herdr pane to write to")
		}
		info, err := ie.herdr.PaneProcessInfo(ctx, pane)
		if err != nil {
			return nil, err
		}
		tty := info.TTY
		if tty == "" && info.ShellPID != nil {
			tty, _ = os.Readlink(fmt.Sprintf("/proc/%d/fd/0", *info.ShellPID))
		}
		if !strings.HasPrefix(tty, "/dev/") {
			return nil, fmt.Errorf("herdr names no terminal for pane %s", pane)
		}
		return os.OpenFile(tty, os.O_WRONLY, 0)
	}
}

// paneLabel is ntm's "<session>:.<index>" for a pane: the slot's M, or
// the herdr pane id after the colon for a pane without a slot.
func paneLabel(sessionName string, p session.Pane) string {
	if p.Slot == "" {
		return sessionName + ":" + p.PaneID
	}
	return sessionName + ":." + p.Ref()
}

// filterOutput applies the optional code-block extraction, then the
// regex line filter.
func filterOutput(output string, re *regexp.Regexp, codeOnly bool) string {
	if codeOnly {
		var contents []string
		for _, b := range panediff.ExtractFromText(output) {
			if b.Content != "" {
				contents = append(contents, b.Content)
			}
		}
		if len(contents) == 0 {
			return ""
		}
		output = strings.Join(contents, "\n\n")
	}
	if re != nil {
		var kept []string
		for _, line := range strings.Split(output, "\n") {
			if re.MatchString(line) {
				kept = append(kept, line)
			}
		}
		output = strings.Join(kept, "\n")
	}
	return output
}

// warnOutputRedaction is ntm's applyOutputRedaction in its default mode,
// warn: the text stays as it is, and the summary counts the secrets
// found by category (nil when none).
func warnOutputRedaction(text string) *RedactionSummary {
	spans, _ := redaction.Find(text)
	if len(spans) == 0 {
		return nil
	}
	cats := map[string]int{}
	for _, sp := range spans {
		cats[string(sp.Category)]++
	}
	return &RedactionSummary{Mode: "warn", Findings: len(spans), Categories: cats, Action: "warn"}
}
