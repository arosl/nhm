// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/extract.go
// (ExtractResponse, newExtractCmd, runExtract, handleExtractApply) at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the session is a herdr workspace, named exactly by its label or
// id (before ntm's name check, as nhm's resolver does); its panes are the
// workspace's (core/session.Panes); the pane argument takes nhm's pane
// forms (ParsePane), an agent's name, or ntm's title match; --last takes
// the focused pane of the workspace's active tab; a pane is read with
// herdr's pane.read (recent); a block's source pane is "<session>:.<slot
// M>", or "<session>:<pane id>" for a pane without a slot; the parser is
// core/panediff's; the clipboard waits on the operator's ruling
// (nhm-2p6.8.1), so --copy refuses; --apply reads its answers from cobra's
// input and prints without colour; the output goes to cobra's writers.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/panediff"
	"github.com/arosl/nhm/internal/core/session"
)

const extractUsage = `Usage: nhm extract <session> [pane] [-l|--lang L] [--last] [-n|--lines N] [-c|--copy] [-a|--apply]
                   [-s|--select N] [--socket PATH] [--json]

ntm's extract on herdr: the fenced code blocks in the recent output of a
session's panes. The session is a herdr workspace, named exactly (its
label or id). Without a pane, every pane; the pane is a pane index (pane
M of the agent tab), a slot N.M, a herdr pane id, an agent's name, or a
part of a pane's title; --last takes the session's active pane. -n (500)
is the lines read per pane; --lang keeps one language; --select keeps
block N (from 1). --apply asks y, n or s for each block with a file path
and writes it relative to the current folder (absolute and .. paths are
refused). --json lists the blocks; it does not go with --copy or --apply.

The clipboard is not in this nhm yet (it waits on a ruling): --copy
refuses.
`

// ExtractResponse is ntm's ExtractResponse.
type ExtractResponse struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Session     string               `json:"session"`
	Source      string               `json:"source"` // Session or session:pane
	Blocks      []panediff.CodeBlock `json:"blocks"`
	TotalFound  int                  `json:"total_found"`
}

// extractOptions are extract's flags.
type extractOptions struct {
	language    string
	last        bool
	lines       int
	copy, apply bool
	selectBlock int
}

func newExtractCmd() *cobra.Command {
	var (
		opts   extractOptions
		socket string
	)
	cmd := &cobra.Command{
		Use:   "extract <session> [pane]",
		Short: "Extract code blocks from agent output",
		Long:  `Extract markdown code blocks from agent pane output.`,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var pane string
			if len(args) > 1 {
				pane = args[1]
			}
			return runExtract(cmd, socket, args[0], pane, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&opts.language, "lang", "l", "", "Filter by language (e.g., python, go, bash)")
	fl.BoolVar(&opts.last, "last", false, "Extract from most recent active pane")
	fl.IntVarP(&opts.lines, "lines", "n", 500, "Number of lines to capture from pane")
	fl.BoolVarP(&opts.copy, "copy", "c", false, "Copy extracted code to clipboard")
	fl.BoolVarP(&opts.apply, "apply", "a", false, "Apply code blocks to detected files")
	fl.IntVarP(&opts.selectBlock, "select", "s", 0, "Select specific block by number (1-indexed)")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// extractFailure writes ntm's ErrorResponse under --json and exits 1, or
// returns err for the root to print.
func extractFailure(cmd *cobra.Command, resp ntmErrorResponse, err error) error {
	if !wantJSON(cmd) {
		return err
	}
	if encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(resp); encErr != nil {
		return encErr
	}
	return verbExit{code: exitError}
}

func runExtract(cmd *cobra.Command, socket, sessionName, paneArg string, opts extractOptions) error {
	// --copy and --apply are side effects; under --json the JSON is the
	// whole contract, so the pair is refused.
	if wantJSON(cmd) && (opts.copy || opts.apply) {
		combo := "--copy"
		if opts.apply && opts.copy {
			combo = "--copy/--apply"
		} else if opts.apply {
			combo = "--apply"
		}
		err := fmt.Errorf("%s is not supported with --json output", combo)
		return extractFailure(cmd, ntmErrorResponse{Code: "UNSUPPORTED_COMBINATION", Error: err.Error(),
			Hint: "Run without --json to use " + combo + ", or parse blocks[] from the JSON output and act on it yourself"}, err)
	}
	sessionName = strings.TrimSpace(sessionName)
	ie, err := openInstance(socket)
	if err != nil {
		return err
	}
	defer ie.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		err = fmt.Errorf("failed to get panes: %w", err)
		return extractFailure(cmd, ntmErrorResponse{Error: "failed to get panes", Details: err.Error()}, err)
	}
	workspace, activeTab := "", ""
	for _, w := range snap.Workspaces {
		if w.Label == sessionName || w.WorkspaceID == sessionName {
			workspace, activeTab = w.WorkspaceID, w.ActiveTabID
			break
		}
	}
	if workspace == "" {
		if verr := session.ValidateName(sessionName); verr != nil {
			err := fmt.Errorf("invalid session name: %w", verr)
			return extractFailure(cmd, ntmErrorResponse{Error: err.Error()}, err)
		}
		err := fmt.Errorf("session '%s' does not exist", sessionName)
		return extractFailure(cmd, ntmErrorResponse{Code: "SESSION_NOT_FOUND", Error: err.Error()}, err)
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: workspace, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return err
	}
	panes := session.Panes(snap, workspace, bound)

	var targets []session.Pane
	switch {
	case opts.last:
		for _, p := range panes {
			if p.Active {
				targets = []session.Pane{p}
				break
			}
		}
		if len(targets) == 0 && len(panes) > 0 {
			targets = panes[len(panes)-1:]
		}
	case paneArg != "":
		t, perr := session.ParsePane(paneArg, session.AgentTabs(bound), activeTab)
		for _, p := range panes {
			if (perr == nil && session.MatchPane(p, t)) || p.Title == paneArg || strings.Contains(p.Title, paneArg) {
				targets = []session.Pane{p}
				break
			}
		}
		if len(targets) == 0 {
			err := fmt.Errorf("pane '%s' not found in session '%s'", paneArg, sessionName)
			return extractFailure(cmd, ntmErrorResponse{Code: "PANE_NOT_FOUND", Error: fmt.Sprintf("pane '%s' not found", paneArg)}, err)
		}
	default:
		targets = panes
	}

	parser := panediff.NewParser()
	if opts.language != "" {
		parser = parser.WithLanguageFilter([]string{opts.language})
	}
	source := sessionName
	if paneArg != "" {
		source = sessionName + ":" + paneArg
	}
	var blocks []panediff.CodeBlock
	for _, p := range targets {
		captured, err := readPane(ctx, ie.herdr, p.PaneID, opts.lines)
		if err != nil {
			continue // a pane that fails is skipped, as in ntm
		}
		found := parser.Parse(captured)
		for i := range found {
			found[i].SourcePane = paneLabel(sessionName, p)
		}
		blocks = append(blocks, found...)
	}

	if opts.selectBlock > 0 {
		if opts.selectBlock > len(blocks) {
			err := fmt.Errorf("block %d does not exist (found %d blocks)", opts.selectBlock, len(blocks))
			return extractFailure(cmd, ntmErrorResponse{Code: "INVALID_SELECTION", Error: err.Error()}, err)
		}
		blocks = []panediff.CodeBlock{blocks[opts.selectBlock-1]}
	}

	stdout := cmd.OutOrStdout()
	if wantJSON(cmd) {
		return encodeJSONIndent(stdout, ExtractResponse{GeneratedAt: time.Now().UTC(), Session: sessionName, Source: source,
			Blocks: blocks, TotalFound: len(blocks)})
	}
	if len(blocks) == 0 {
		fmt.Fprintf(stdout, "No code blocks found in %s\n", source)
		return nil
	}
	if opts.copy {
		return fmt.Errorf("the clipboard is not in this nhm yet (deferred: nhm-2p6.8.1, a ruling); list the blocks without --copy")
	}
	if opts.apply {
		return handleExtractApply(cmd.InOrStdin(), stdout, blocks)
	}

	fmt.Fprintf(stdout, "Found %d code block(s) in %s:\n\n", len(blocks), source)
	for i, block := range blocks {
		langDisplay := block.Language
		if langDisplay == "" {
			langDisplay = "text"
		}
		fileInfo := ""
		if block.FilePath != "" {
			if block.IsNew {
				fileInfo = fmt.Sprintf(" (new file: %s)", block.FilePath)
			} else {
				fileInfo = fmt.Sprintf(" (%s)", block.FilePath)
			}
		}
		fmt.Fprintf(stdout, "[%d] %s%s (from %s)\n", i+1, langDisplay, fileInfo, block.SourcePane)
		// A box around the content.
		fmt.Fprintln(stdout, "    ┌"+strings.Repeat("─", 60))
		for _, line := range strings.Split(block.Content, "\n") {
			if len(line) > 58 {
				line = line[:55] + "..."
			}
			fmt.Fprintf(stdout, "    │ %s\n", line)
		}
		fmt.Fprintln(stdout, "    └"+strings.Repeat("─", 60))
		fmt.Fprintln(stdout)
	}
	return nil
}

// handleExtractApply writes each block with a file path, after a yes on
// in: y writes it, n skips it, s skips the rest.
func handleExtractApply(in io.Reader, w io.Writer, blocks []panediff.CodeBlock) error {
	if len(blocks) == 0 {
		return fmt.Errorf("no code blocks to apply")
	}
	applied, skipped := 0, 0

blockLoop:
	for i, block := range blocks {
		if block.FilePath == "" {
			fmt.Fprintf(w, "[%d] Skipped: no file path detected\n", i+1)
			skipped++
			continue
		}
		langDisplay := block.Language
		if langDisplay == "" {
			langDisplay = "text"
		}
		action := "update"
		if block.IsNew {
			action = "create"
		}
		// Risky paths; cleanPath is used from here on.
		cleanPath := filepath.Clean(block.FilePath)
		isRisky := filepath.IsAbs(cleanPath) || strings.HasPrefix(cleanPath, "..") || strings.Contains(cleanPath, "/..")
		fmt.Fprintf(w, "[%d] %s %s (%s)\n", i+1, action, cleanPath, langDisplay)
		if isRisky {
			fmt.Fprintln(w, "    ✗ Blocked: path escapes current directory")
			skipped++
			continue
		}
		if _, err := os.Stat(cleanPath); err == nil && !block.IsNew {
			fmt.Fprintf(w, "    Preview: %d lines of code\n", strings.Count(block.Content, "\n")+1)
		}
		fmt.Fprint(w, "    Apply? [y]es / [n]o / [s]kip all: ")
		var response string
		_, _ = fmt.Fscanln(in, &response)
		switch strings.ToLower(strings.TrimSpace(response)) {
		case "y", "yes":
			if dir := filepath.Dir(cleanPath); dir != "." && dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					fmt.Fprintf(w, "    ✗ Failed to create directory: %v\n", err)
					continue
				}
			}
			if err := os.WriteFile(cleanPath, []byte(block.Content), 0o644); err != nil {
				fmt.Fprintf(w, "    ✗ Failed to write: %v\n", err)
				continue
			}
			fmt.Fprintf(w, "    ✓ Applied to %s\n", cleanPath)
			applied++
		case "s", "skip":
			fmt.Fprintln(w, "Skipping remaining blocks")
			break blockLoop
		default:
			fmt.Fprintln(w, "    Skipped")
			skipped++
		}
	}
	fmt.Fprintf(w, "\nApplied: %d, Skipped: %d\n", applied, skipped)
	return nil
}
