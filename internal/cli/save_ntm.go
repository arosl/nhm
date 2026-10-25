// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/save.go
// (newSaveCmd, runSave) and internal/cli/redaction_io.go (the warn branch
// of applyOutputRedaction) at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: a session is a herdr
// workspace (nhm's resolveSession) and its panes are the workspace's
// (core/session.Panes); a pane is read with herdr's pane.read (recent);
// its command is herdr's first foreground process, else its kind; a
// pane's index is its slot's M, or its herdr pane id; the time in the
// file name and header is UTC (the operator's ruling, 2026-10-25: rule
// 8), in ntm's formats; --output's default is nhm's own, .nhm/outputs
// (rule 4: never ntm's folders); the session part of the file name is
// sanitized, since a herdr label may hold any character; the redaction
// scan is nhm's in ntm's default mode, warn (nhm-2p6.20); the output goes
// to cobra's writers, the check marks without colour.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/session"
)

const saveUsage = `Usage: nhm save [session] [-o|--output DIR] [-l|--lines N] [--all|--cc|--cod|--gmi|--agy]
                [--socket PATH]
       (also dump, export)

ntm's save on herdr: each pane's recent output to a text file in DIR
(default .nhm/outputs), named <session>_<pane title>_<YYYYMMDD_HHMMSS>.txt
(UTC), under a header (session, pane, command, capture time, lines). The
session is a herdr workspace (its label or id); without one, nhm infers
it as ntm does. --lines (2000) is the lines read per pane. Without a type
flag, every pane, shells too. A secret in the text is reported on
stderr, and the files are not changed (ntm's default mode, warn). Text
only: --json changes nothing.
`

// saveDefaultDir is nhm's default --output: ntm's is ./outputs, and rule
// 4 keeps ntm's folders out of nhm.
const saveDefaultDir = ".nhm/outputs"

// saveSeparator is the rule above and below a saved file's header.
const saveSeparator = "═══════════════════════════════════════════════════════════════\n"

func newSaveCmd() *cobra.Command {
	var (
		outputDir string
		lines     int
		filter    session.TypeFilter
		socket    string
	)
	cmd := &cobra.Command{
		Use:     "save [session-name]",
		Aliases: []string{"dump", "export"},
		Short:   "Save pane outputs to files",
		Long: `Save the output from session panes to individual files.

Creates timestamped files for each pane in the output directory.
Files are named: {session}_{pane-title}_{timestamp}.txt`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				name = args[0]
			}
			// Default to all if no filter specified
			if filter.IsEmpty() {
				filter.All = true
			}
			return runSave(cmd, socket, name, outputDir, lines, filter)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&outputDir, "output", "o", saveDefaultDir, "Output directory")
	fl.IntVarP(&lines, "lines", "l", 2000, "Number of lines to capture")
	fl.BoolVar(&filter.All, "all", false, "Save from all panes")
	fl.BoolVar(&filter.Claude, "cc", false, "Save from Claude panes")
	fl.BoolVar(&filter.Codex, "cod", false, "Save from Codex panes")
	fl.BoolVar(&filter.Gemini, "gmi", false, "Save from Gemini panes")
	fl.BoolVar(&filter.Antigravity, "agy", false, "Save from Antigravity panes")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// paneCommand is a pane's command, tmux's pane_current_command on herdr:
// its first foreground process, else its agent's kind.
func paneCommand(ctx context.Context, ie *instanceEnv, p session.Pane) string {
	if info, err := ie.herdr.PaneProcessInfo(ctx, p.PaneID); err == nil && len(info.ForegroundProcesses) > 0 {
		return info.ForegroundProcesses[0].Name
	}
	return p.Kind
}

func runSave(cmd *cobra.Command, socket, name, outputDir string, lines int, filter session.TypeFilter) error {
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

	var targets []session.Pane
	for _, p := range sp.panes {
		if filter.Matches(p.Type) {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no matching panes found")
	}

	timestamp := time.Now().UTC().Format("20060102_150405")
	type capturedOutput struct {
		path, content string
		redaction     *RedactionSummary
	}
	var captured []capturedOutput
	for _, p := range targets {
		output, err := readPane(ctx, ie.herdr, p.PaneID, lines)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: failed to capture pane %s: %v\n", p.Ref(), err)
			continue
		}
		// The title, safe for a file name.
		title := sanitizeFilename(p.Title)
		if title == "" {
			title = "pane_" + sanitizeFilename(p.Ref())
		}
		filename := fmt.Sprintf("%s_%s_%s.txt", sanitizeFilename(sessionName), title, timestamp)
		var header strings.Builder
		header.WriteString(saveSeparator)
		fmt.Fprintf(&header, "Session: %s\n", sessionName)
		fmt.Fprintf(&header, "Pane: %s (index %s)\n", p.Title, p.Ref())
		fmt.Fprintf(&header, "Command: %s\n", paneCommand(ctx, ie, p))
		fmt.Fprintf(&header, "Captured: %s\n", time.Now().UTC().Format(time.RFC3339))
		fmt.Fprintf(&header, "Lines: %d\n", lines)
		header.WriteString(saveSeparator + "\n")
		content := header.String() + output
		captured = append(captured, capturedOutput{path: filepath.Join(outputDir, filename), content: content,
			redaction: warnOutputRedaction(content)})
	}
	if len(captured) == 0 {
		return fmt.Errorf("no content captured")
	}

	// The folder is made after the redaction check, as in ntm.
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	savedFiles, totalFindings := 0, 0
	totalCategories := map[string]int{}
	for _, c := range captured {
		if c.redaction != nil {
			totalFindings += c.redaction.Findings
			for cat, count := range c.redaction.Categories {
				totalCategories[cat] += count
			}
		}
		if err := os.WriteFile(c.path, []byte(c.content), 0o644); err != nil {
			fmt.Fprintf(stderr, "Warning: failed to write %s: %v\n", c.path, err)
			continue
		}
		savedFiles++
		fmt.Fprintf(stdout, "  ✓ %s\n", c.path)
	}
	if savedFiles == 0 {
		return fmt.Errorf("no files saved")
	}
	if totalFindings > 0 {
		fmt.Fprintf(stderr, "Warning: detected %d potential secret(s) across saved outputs (%s); files are NOT modified (mode=warn)\n",
			totalFindings, formatRedactionCategoryCounts(totalCategories))
	}
	fmt.Fprintf(stdout, "\n✓ Saved %d file(s) to %s\n", savedFiles, outputDir)
	return nil
}
