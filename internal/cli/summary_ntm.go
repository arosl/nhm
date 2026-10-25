// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/summary.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: a session is a herdr workspace (nhm's resolveSession)
// and its panes the workspace's (core/session.Panes), each read with
// herdr's pane.read (500 recent lines); a session's project is its bound
// agents' project (the first), else the cwd's project root (ntm's
// util.ResolveProjectDir, with nhm's .nhm/config.toml); saved summaries
// are in <project>/.nhm/summaries and named in UTC (rule 4, rule 8); the
// session archive is nhm's, <state root>/archive, which nothing writes
// until nhm ports ntm's internal monitor (nhm-2p6.26), so --since trims
// nothing and --regenerate finds no archived output, as ntm does without
// its monitor; without a herdr instance, saved summaries still answer, as
// ntm's do without tmux; the output goes to cobra's writers.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/summary"
	bvutil "github.com/arosl/nhm/internal/integrations/bv/util"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

const summaryUsage = `Usage: nhm summary [session] [--since D] [--format text|json|markdown|detailed|handoff]
                   [--all] [--recent] [--regenerate] [--socket PATH]

ntm's summary on herdr: what each agent did in the session
(accomplishments, changes, files, pending work, errors), from each agent
pane's last 500 lines and git's changed files in the session's project.
A session with a saved summary in <project>/.nhm/summaries shows that
one. --all lists the saved summaries, --recent shows the newest, and
--regenerate builds one again from the session archive and saves it.
nhm has no session archive yet (ntm's internal monitor, nhm-2p6.26), so
--since (30m) trims nothing and --regenerate finds no archived output.
--format json (or --json) prints the summary as JSON.
`

// summaryArchiveDir is where nhm's session archive is: <state root>/archive
// (ntm's is its ~/.ntm/archive).
var summaryArchiveDir = func() (string, error) {
	root, err := store.StateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "archive"), nil
}

func newSummaryCmd() *cobra.Command {
	var (
		since      string
		format     string
		socket     string
		listAll    bool
		recent     bool
		regenerate bool
	)

	cmd := &cobra.Command{
		Use:   "summary [session]",
		Short: "Show activity summary for agents in a session",
		Long: `Display a summary of what each agent accomplished in a session.

Shows per-agent:
  - Active time and output volume
  - Files modified
  - Key actions (created, fixed, added, etc.)
  - Error counts

The summary is useful after parallel agent work to understand
what each agent did and identify potential conflicts.

Examples:
  nhm summary                      # Auto-detect session
  nhm summary myproject            # Specific session
  nhm summary --recent             # Show most recent summary
  nhm summary --since 1h           # Look back 1 hour
  nhm summary --format markdown    # Output as markdown
  nhm summary --json               # Output as JSON
  nhm summary --all                # List all available summaries
  nhm summary --regenerate         # Regenerate from archived output`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listAll {
				return runSummaryList(cmd, format)
			}
			if recent && regenerate {
				return fmt.Errorf("--recent and --regenerate cannot be used together")
			}
			return runSummary(cmd, args, since, format, socket, recent, regenerate, cmd.Flags().Changed("since"))
		},
	}

	cmd.Flags().StringVar(&since, "since", "30m", "Duration to look back (e.g., 30m, 1h)")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, markdown, detailed, or handoff")
	cmd.Flags().BoolVar(&listAll, "all", false, "List all available summaries")
	cmd.Flags().BoolVar(&recent, "recent", false, "Show most recent summary (optionally filtered by session)")
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "Regenerate summary from archived output (if available)")
	cmd.Flags().StringVar(&socket, "socket", "", "herdr API socket (nhm)")

	return cmd
}

type summaryFileInfo struct {
	Session   string    `json:"session"`
	Timestamp time.Time `json:"timestamp"`
	Path      string    `json:"path"`
}

var summaryFilenameRegex = regexp.MustCompile(`^(?P<session>.+)-(?P<ts>\d{8}-\d{6})\.json$`)
var archiveFilenameRegex = regexp.MustCompile(`^(?P<session>.+)_(?P<date>\d{4}-\d{2}-\d{2})\.jsonl$`)

func runSummary(cmd *cobra.Command, args []string, sinceStr, format, socket string, recent, regenerate, sinceSet bool) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	sessionArg := ""
	if len(args) > 0 {
		sessionArg = args[0]
	}

	sumFormat, forceJSON, err := parseSummaryFormat(format)
	if err != nil {
		return err
	}
	machineJSON := forceJSON || wantJSON(cmd)

	since, err := parseNTMDurationWithDefault(sinceStr, 30*time.Minute)
	if err != nil {
		return fmt.Errorf("invalid --since: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	wd, _ := os.Getwd()
	// Saved summaries answer without a herdr instance, as ntm's do
	// without tmux.
	ie, ieErr := openInstance(socket)
	var sessions []session.Session
	if ieErr == nil {
		defer ie.db.Close()
		sessions, _, _ = herdrSessions(ctx, ie, false)
	}
	projectDir := summaryProjectDir(sessions, sessionArg, "", wd)
	summaryFiles, err := listSummaryFiles(projectDir)
	if err != nil {
		return err
	}

	if regenerate {
		// Only an explicit --since narrows regeneration: the default 30m
		// window would silently empty most historical archives.
		cutoff := time.Time{}
		if sinceSet {
			cutoff = time.Now().Add(-since)
		}
		return regenerateSummaryFromArchive(ctx, stdout, sessions, sessionArg, sumFormat, machineJSON, projectDir, wd, cutoff)
	}

	if recent {
		latest, ok := latestSummary(summaryFiles, sessionArg)
		if !ok {
			return fmt.Errorf("no summaries found")
		}
		return outputSummaryFromFile(stdout, latest.Path, sumFormat, machineJSON)
	}

	if sessionArg != "" {
		resolved, ok, err := resolveSummarySessionName(sessionArg, summaryFiles)
		if err != nil {
			return err
		}
		if ok {
			if latest, found := latestSummaryForSession(summaryFiles, resolved); found {
				return outputSummaryFromFile(stdout, latest.Path, sumFormat, machineJSON)
			}
		}
	}

	// Without a live session, the newest saved summary answers a call
	// that named none.
	fallback := func(err error) error {
		if sessionArg == "" && len(summaryFiles) > 0 {
			if latest, ok := latestSummaryForSession(summaryFiles, ""); ok {
				return outputSummaryFromFile(stdout, latest.Path, sumFormat, machineJSON)
			}
		}
		return err
	}
	if ieErr != nil {
		return fallback(ieErr)
	}
	res, err := resolveSession(ctx, ie, sessionArg, machineJSON, stdout, stderr)
	if err != nil {
		return fallback(err)
	}
	if res.Session == "" {
		return nil
	}
	projectDir = summaryProjectDir(sessions, res.Session, res.WorkspaceID, wd)

	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}
	bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: res.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}
	panes := session.Panes(snap, res.WorkspaceID, bound)

	// Build agent outputs. Pane reads carry no timestamps, so the session
	// archive (timestamped JSONL of the same scrollback) is the oracle for
	// the --since window: content the archive proves is older than the
	// cutoff is trimmed from the read.
	outputs := collectSummaryAgentOutputs(ctx, ie, panes)
	if archivePath, _, archiveErr := findArchiveFile(res.Session); archiveErr == nil && archivePath != "" {
		outputs = trimOutputsToSinceWindow(outputs, archivePath, time.Now().Add(-since), archivePaneAliases(panes))
	}

	s, err := summary.SummarizeSession(ctx, summary.Options{
		Session:        res.Session,
		Outputs:        outputs,
		Format:         sumFormat,
		ProjectDir:     projectDir,
		IncludeGitDiff: true,
	})
	if err != nil {
		return err
	}

	if machineJSON {
		return encodeJSONIndent(stdout, s)
	}
	fmt.Fprintln(stdout, s.Text)
	return nil
}

func runSummaryList(cmd *cobra.Command, format string) error {
	stdout := cmd.OutOrStdout()
	sumFormat, forceJSON, err := parseSummaryFormat(format)
	if err != nil {
		return err
	}
	asJSON := forceJSON || wantJSON(cmd)

	wd, _ := os.Getwd()
	files, err := listSummaryFiles(summaryProjectDir(nil, "", "", wd))
	if err != nil {
		return fmt.Errorf("failed to list summaries: %w", err)
	}

	if len(files) == 0 {
		if asJSON {
			return encodeJSONIndent(stdout, []summaryFileInfo{})
		}
		fmt.Fprintln(stdout, "No summaries found.")
		return nil
	}

	if asJSON {
		return encodeJSONIndent(stdout, files)
	}

	fmt.Fprintf(stdout, "Available summaries (%s):\n", sumFormat)
	for _, f := range files {
		fmt.Fprintf(stdout, "  %s - %s (%s)\n", f.Session, f.Timestamp.Format(time.RFC3339), f.Path)
	}
	return nil
}

func parseSummaryFormat(format string) (summary.SummaryFormat, bool, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text", "brief":
		return summary.FormatBrief, false, nil
	case "json":
		return summary.FormatBrief, true, nil
	case "markdown", "md", "detailed":
		return summary.FormatDetailed, false, nil
	case "handoff":
		return summary.FormatHandoff, false, nil
	default:
		return "", false, fmt.Errorf("invalid --format %q (expected text, json, markdown, detailed, or handoff)", format)
	}
}

// summaryProjectDir is ntm's resolveProjectDir on nhm: the project of the
// session's bound agents (the first; the workspace, else the session
// named), else the cwd's project root.
func summaryProjectDir(sessions []session.Session, name, workspaceID, wd string) string {
	name = strings.TrimSpace(name)
	for _, s := range sessions {
		match := s.WorkspaceID == workspaceID
		if workspaceID == "" {
			match = name != "" && (s.Name == name || s.WorkspaceID == name)
		}
		if match && len(s.ProjectDirs) > 0 {
			return s.ProjectDirs[0]
		}
	}
	if dir := bvutil.ResolveProjectDir(wd); dir != "" {
		return dir
	}
	return wd
}

func listSummaryFiles(projectDir string) ([]summaryFileInfo, error) {
	summaryDir := filepath.Join(projectDir, ".nhm", "summaries")
	entries, err := os.ReadDir(summaryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []summaryFileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		session, ts, ok := parseSummaryFilename(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if ts.IsZero() {
			ts = info.ModTime()
		}
		files = append(files, summaryFileInfo{
			Session:   session,
			Timestamp: ts,
			Path:      filepath.Join(summaryDir, entry.Name()),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Timestamp.After(files[j].Timestamp)
	})

	return files, nil
}

func parseSummaryFilename(name string) (string, time.Time, bool) {
	matches := summaryFilenameRegex.FindStringSubmatch(name)
	if len(matches) != 3 {
		return "", time.Time{}, false
	}
	session := matches[1]
	ts, err := time.Parse("20060102-150405", matches[2])
	if err != nil {
		return session, time.Time{}, true
	}
	return session, ts, true
}

func resolveSummarySessionName(input string, files []summaryFileInfo) (string, bool, error) {
	if input == "" {
		return "", false, nil
	}
	sessions := uniqueSessions(files)
	for _, s := range sessions {
		if s == input {
			return s, true, nil
		}
	}
	var matches []string
	for _, s := range sessions {
		if strings.HasPrefix(s, input) {
			matches = append(matches, s)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	if len(matches) > 1 {
		return "", false, fmt.Errorf("session %q matches multiple summaries: %s (please be more specific)", input, strings.Join(matches, ", "))
	}
	return "", false, nil
}

func uniqueSessions(files []summaryFileInfo) []string {
	seen := make(map[string]struct{})
	var sessions []string
	for _, f := range files {
		if _, ok := seen[f.Session]; ok {
			continue
		}
		seen[f.Session] = struct{}{}
		sessions = append(sessions, f.Session)
	}
	sort.Strings(sessions)
	return sessions
}

func latestSummary(files []summaryFileInfo, session string) (summaryFileInfo, bool) {
	if session == "" {
		if len(files) == 0 {
			return summaryFileInfo{}, false
		}
		return files[0], true
	}
	return latestSummaryForSession(files, session)
}

func latestSummaryForSession(files []summaryFileInfo, session string) (summaryFileInfo, bool) {
	for _, f := range files {
		if session == "" || f.Session == session {
			return f, true
		}
	}
	return summaryFileInfo{}, false
}

func outputSummaryFromFile(w io.Writer, path string, format summary.SummaryFormat, jsonOut bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var sum summary.SessionSummary
	if err := json.NewDecoder(file).Decode(&sum); err != nil {
		return fmt.Errorf("failed to parse summary %s: %w", path, err)
	}

	if jsonOut {
		return encodeJSONIndent(w, sum)
	}

	fmt.Fprintln(w, summary.RenderSummary(&sum, format))
	return nil
}

type archiveFileInfo struct {
	Session   string
	Timestamp time.Time
	Path      string
}

// archiveRecord is the part of ntm's archive.ArchiveRecord, one JSONL
// line of the session archive, that summary reads.
type archiveRecord struct {
	Pane      string    `json:"pane"`
	Agent     string    `json:"agent"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
}

func regenerateSummaryFromArchive(ctx context.Context, w io.Writer, sessions []session.Session, sessionArg string, format summary.SummaryFormat, jsonOut bool, projectDir, wd string, cutoff time.Time) error {
	searchSession := strings.TrimSpace(sessionArg)

	archiveFile, sessionName, err := findArchiveFile(searchSession)
	if err != nil {
		return err
	}
	if archiveFile == "" {
		return fmt.Errorf("no archived output found")
	}

	if sessionName == "" {
		sessionName = searchSession
	}
	if projectDir == "" {
		projectDir = summaryProjectDir(sessions, sessionName, "", wd)
	}

	outputs, err := loadArchiveOutputs(archiveFile, cutoff)
	if err != nil {
		return err
	}
	if len(outputs) == 0 {
		if !cutoff.IsZero() {
			return fmt.Errorf("no archived output found for session %q within the --since window", sessionName)
		}
		return fmt.Errorf("no archived output found for session %q", sessionName)
	}

	sum, err := summary.SummarizeSession(ctx, summary.Options{
		Session:        sessionName,
		Outputs:        outputs,
		Format:         format,
		ProjectDir:     projectDir,
		IncludeGitDiff: true,
	})
	if err != nil {
		return err
	}

	if err := writeSummaryFile(projectDir, sessionName, sum); err != nil {
		return err
	}

	if jsonOut {
		return encodeJSONIndent(w, sum)
	}

	fmt.Fprintln(w, summary.RenderSummary(sum, format))
	return nil
}

func writeSummaryFile(projectDir, session string, sum *summary.SessionSummary) error {
	if projectDir == "" {
		return fmt.Errorf("project directory required to write summary")
	}
	summaryDir := filepath.Join(projectDir, ".nhm", "summaries")
	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		return fmt.Errorf("failed to create summary dir: %w", err)
	}
	timestamp := time.Now().UTC().Format("20060102-150405")
	filename := filepath.Join(summaryDir, fmt.Sprintf("%s-%s.json", session, timestamp))
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create summary file: %w", err)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(sum); err != nil {
		return fmt.Errorf("failed to write summary file: %w", err)
	}
	return nil
}

func findArchiveFile(session string) (string, string, error) {
	files, err := listArchiveFiles()
	if err != nil {
		return "", "", err
	}
	if len(files) == 0 {
		return "", "", nil
	}

	if session == "" {
		return files[0].Path, files[0].Session, nil
	}

	for _, f := range files {
		if f.Session == session {
			return f.Path, f.Session, nil
		}
	}
	return "", "", nil
}

func listArchiveFiles() ([]archiveFileInfo, error) {
	archiveDir, err := summaryArchiveDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []archiveFileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		session, ts, ok := parseArchiveFilename(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if ts.IsZero() {
			ts = info.ModTime()
		}
		files = append(files, archiveFileInfo{
			Session:   session,
			Timestamp: ts,
			Path:      filepath.Join(archiveDir, entry.Name()),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Timestamp.After(files[j].Timestamp)
	})

	return files, nil
}

func parseArchiveFilename(name string) (string, time.Time, bool) {
	matches := archiveFilenameRegex.FindStringSubmatch(name)
	if len(matches) != 3 {
		return "", time.Time{}, false
	}
	session := matches[1]
	ts, err := time.Parse("2006-01-02", matches[2])
	if err != nil {
		return session, time.Time{}, true
	}
	return session, ts, true
}

// loadArchiveOutputs aggregates archived pane content into per-pane agent
// outputs. A non-zero cutoff excludes records whose timestamp falls before
// it; records without a timestamp cannot be dated and are kept.
func loadArchiveOutputs(path string, cutoff time.Time) ([]summary.AgentOutput, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	paneBuilders := make(map[string]*strings.Builder)
	paneTypes := make(map[string]string)
	for {
		var record archiveRecord
		if err := dec.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if record.Content == "" {
			continue
		}
		if !cutoff.IsZero() && !record.Timestamp.IsZero() && record.Timestamp.Before(cutoff) {
			continue
		}
		builder := paneBuilders[record.Pane]
		if builder == nil {
			builder = &strings.Builder{}
			paneBuilders[record.Pane] = builder
		}
		builder.WriteString(record.Content)
		if record.Agent != "" {
			paneTypes[record.Pane] = record.Agent
		}
	}

	var panes []string
	for pane := range paneBuilders {
		panes = append(panes, pane)
	}
	sort.Strings(panes)

	outputs := make([]summary.AgentOutput, 0, len(panes))
	for _, pane := range panes {
		agentType := paneTypes[pane]
		if agentType == "" {
			agentType = "unknown"
		}
		outputs = append(outputs, summary.AgentOutput{
			AgentID:   pane,
			AgentType: agentType,
			Output:    paneBuilders[pane].String(),
		})
	}
	return outputs, nil
}

// collectSummaryAgentOutputs reads the last 500 lines of each agent pane;
// a pane without an agent is left out.
func collectSummaryAgentOutputs(ctx context.Context, ie *instanceEnv, panes []session.Pane) []summary.AgentOutput {
	outputs := make([]summary.AgentOutput, 0, len(panes))
	for _, p := range panes {
		if p.Type == "" || p.Type == "unknown" || p.Type == session.UserType {
			continue
		}
		out, _ := readPane(ctx, ie.herdr, p.PaneID, 500)
		outputs = append(outputs, summary.AgentOutput{
			AgentID:   p.PaneID,
			AgentType: p.Type,
			Output:    out,
		})
	}
	return outputs
}

// archivePaneAliases maps each pane's id (the summary output AgentID) to
// the "<type>_<index>" pane name that ntm's archiver records on its JSONL
// rows, so --since trimming can find a pane's archive markers.
func archivePaneAliases(panes []session.Pane) map[string]string {
	aliases := make(map[string]string, len(panes))
	for _, p := range panes {
		aliases[p.PaneID] = fmt.Sprintf("%s_%d", agent.AgentType(p.Type).Canonical(), p.Index)
	}
	return aliases
}

// trimOutputsToSinceWindow drops read pane content that the session
// archive proves is older than the --since cutoff. A pane read has no
// timestamps of its own; the archive records the same scrollback
// incrementally with timestamps, so the last record from before the cutoff
// marks where the window starts inside the read text. Panes with no dated
// pre-cutoff archive records are left untrimmed (no evidence of age).
func trimOutputsToSinceWindow(outputs []summary.AgentOutput, archivePath string, cutoff time.Time, aliases map[string]string) []summary.AgentOutput {
	if cutoff.IsZero() || len(outputs) == 0 {
		return outputs
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return outputs
	}
	defer file.Close()

	// Last pre-cutoff record content per pane.
	staleMarkers := make(map[string]string)
	dec := json.NewDecoder(file)
	for {
		var record archiveRecord
		if err := dec.Decode(&record); err != nil {
			break
		}
		if record.Content == "" || record.Timestamp.IsZero() {
			continue
		}
		if record.Timestamp.Before(cutoff) {
			staleMarkers[record.Pane] = record.Content
		}
	}
	if len(staleMarkers) == 0 {
		return outputs
	}

	trimmed := make([]summary.AgentOutput, len(outputs))
	copy(trimmed, outputs)
	for i := range trimmed {
		rawMarker, ok := staleMarkers[trimmed[i].AgentID]
		if !ok {
			rawMarker = staleMarkers[aliases[trimmed[i].AgentID]]
		}
		marker := strings.TrimRight(rawMarker, "\n")
		if marker == "" {
			continue
		}
		if idx := strings.LastIndex(trimmed[i].Output, marker); idx >= 0 {
			trimmed[i].Output = strings.TrimLeft(trimmed[i].Output[idx+len(marker):], "\n")
			continue
		}
		// The full record may have wrapped or been re-rendered; fall back to
		// its last non-blank line as the boundary marker.
		lines := strings.Split(marker, "\n")
		for j := len(lines) - 1; j >= 0; j-- {
			line := strings.TrimSpace(lines[j])
			if line == "" {
				continue
			}
			if idx := strings.LastIndex(trimmed[i].Output, line); idx >= 0 {
				trimmed[i].Output = strings.TrimLeft(trimmed[i].Output[idx+len(line):], "\n")
			}
			break
		}
	}
	return trimmed
}
