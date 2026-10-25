// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/synthesis.go (CodeBlock, FileMention, CommandMention,
// the FileAction constants, ExtractCodeBlocks, ExtractFileMentions,
// filePathPatterns, extractPathsFromLine, inferFileAction,
// ExtractCommands, exitCodePatterns, parseExitCode, SessionSummary,
// SummaryTimeRange, AgentOutputSummary, AgentActivityData,
// GenerateSummary, summarizeAgent, countErrors, extractKeyActions,
// cleanActionLine, SessionSummaryResponse) and internal/robot/tui_parity.go
// (FileConflict) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: GenerateSummary is a function that takes
// the time and the conflict detector's answer (ntm's generator holds the
// detector and runs it); the time range is in UTC (rule 8), where ntm's is
// local; a conflict's agents are [] when none, as ntm's encoder writes
// them.

package robot

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// FileConflict represents a file being edited by multiple agents.
type FileConflict struct {
	Path      string   `json:"path"`
	Agents    []string `json:"agents"`
	Severity  string   `json:"severity"`   // "warning", "critical"
	FirstEdit string   `json:"first_edit"` // RFC3339
	LastEdit  string   `json:"last_edit"`  // RFC3339
}

// CodeBlock represents an extracted code block from agent output.
type CodeBlock struct {
	Language  string `json:"language"`
	Content   string `json:"content"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

// FileMention represents a file path mentioned in agent output.
type FileMention struct {
	Path       string  `json:"path"`
	Action     string  `json:"action"` // created, modified, deleted, read
	LineNum    int     `json:"line_num,omitempty"`
	Confidence float64 `json:"confidence"` // 0.0-1.0 how confident we are about the action
}

// FileAction constants for FileMention.
const (
	FileActionCreated  = "created"
	FileActionModified = "modified"
	FileActionDeleted  = "deleted"
	FileActionRead     = "read"
	FileActionUnknown  = "unknown"
)

// CommandMention represents a shell command detected in output.
type CommandMention struct {
	Command  string `json:"command"`
	LineNum  int    `json:"line_num"`
	ExitCode *int   `json:"exit_code,omitempty"` // nil if not visible
}

// ExtractCodeBlocks extracts markdown code blocks from output.
// Handles ```lang ... ``` syntax with optional language tags.
func ExtractCodeBlocks(content string) []CodeBlock {
	var blocks []CodeBlock
	lines := strings.Split(content, "\n")

	inBlock := false
	var currentBlock CodeBlock
	var contentLines []string

	for i, line := range lines {
		lineNum := i + 1 // 1-indexed

		if !inBlock {
			// Look for opening ```
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inBlock = true
				currentBlock = CodeBlock{
					LineStart: lineNum,
				}
				// Extract language from opening fence
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 3 {
					currentBlock.Language = strings.TrimSpace(trimmed[3:])
				}
				contentLines = nil
			}
		} else {
			// Look for closing ```
			if strings.TrimSpace(line) == "```" {
				inBlock = false
				currentBlock.LineEnd = lineNum
				currentBlock.Content = strings.Join(contentLines, "\n")
				blocks = append(blocks, currentBlock)
			} else {
				contentLines = append(contentLines, line)
			}
		}
	}

	return blocks
}

// ExtractFileMentions extracts file path mentions from output with action context.
// Looks for common patterns like src/..., ./..., internal/..., and action keywords.
func ExtractFileMentions(content string) []FileMention {
	var mentions []FileMention
	seen := make(map[string]bool)
	lines := strings.Split(content, "\n")

	// Patterns that look like file paths
	// We're looking for paths, not URLs
	for i, line := range lines {
		lineNum := i + 1

		// Extract paths and determine action from context
		paths := extractPathsFromLine(line)
		for _, path := range paths {
			if seen[path] {
				continue
			}
			seen[path] = true

			action, confidence := inferFileAction(line, path)
			mentions = append(mentions, FileMention{
				Path:       path,
				Action:     action,
				LineNum:    lineNum,
				Confidence: confidence,
			})
		}
	}

	return mentions
}

var filePathPatterns = []*regexp.Regexp{
	// Paths starting with common prefixes
	regexp.MustCompile(`(?:^|[\s'"(,])((src|internal|pkg|cmd|lib|test|tests|spec|app|api|web|frontend|backend|client|server|utils|util|common|shared|core|modules|components|services|models|views|controllers|middleware|config|configs|scripts|tools|build|dist|bin|docs|examples|assets|resources|public|private|vendor|third_party|node_modules)\/[\w\-./]+\.\w+)`),
	// Relative paths
	regexp.MustCompile(`(?:^|[\s'"(,])(\.\/[\w\-./]+\.\w+)`),
	// Paths with file extensions
	regexp.MustCompile(`(?:^|[\s'"(,])([\w\-./]+\.(?:go|py|js|ts|jsx|tsx|rs|rb|java|c|cpp|h|hpp|cs|php|swift|kt|scala|vue|svelte|md|txt|json|yaml|yml|toml|xml|html|css|scss|sass|less))(?:[\s'")\]:,]|$)`),
}

// extractPathsFromLine extracts file paths from a single line.
func extractPathsFromLine(line string) []string {
	var paths []string

	for _, re := range filePathPatterns {
		matches := re.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) > 1 {
				path := strings.Trim(match[1], `'"`)
				// Skip if it looks like a URL
				if strings.Contains(path, "://") || strings.HasPrefix(path, "http") {
					continue
				}
				// Skip if too short or doesn't look like a path
				if len(path) < 3 || !strings.Contains(path, "/") && !strings.Contains(path, ".") {
					continue
				}
				paths = append(paths, path)
			}
		}
	}

	return paths
}

// inferFileAction determines the likely action on a file from context.
func inferFileAction(line, path string) (string, float64) {
	lineLower := strings.ToLower(line)

	// High confidence patterns
	if strings.Contains(lineLower, "created "+path) ||
		strings.Contains(lineLower, "creating "+path) ||
		strings.Contains(lineLower, "create file") ||
		strings.Contains(lineLower, "new file") ||
		strings.Contains(lineLower, "write to "+strings.ToLower(path)) {
		return FileActionCreated, 0.9
	}

	if strings.Contains(lineLower, "modified "+path) ||
		strings.Contains(lineLower, "modifying "+path) ||
		strings.Contains(lineLower, "updated "+path) ||
		strings.Contains(lineLower, "updating "+path) ||
		strings.Contains(lineLower, "edited "+path) ||
		strings.Contains(lineLower, "editing "+path) ||
		strings.Contains(lineLower, "changed "+path) {
		return FileActionModified, 0.9
	}

	if strings.Contains(lineLower, "deleted "+path) ||
		strings.Contains(lineLower, "deleting "+path) ||
		strings.Contains(lineLower, "removed "+path) ||
		strings.Contains(lineLower, "removing "+path) {
		return FileActionDeleted, 0.9
	}

	if strings.Contains(lineLower, "reading "+path) ||
		strings.Contains(lineLower, "read "+path) ||
		strings.Contains(lineLower, "opened "+path) ||
		strings.Contains(lineLower, "loading "+path) {
		return FileActionRead, 0.9
	}

	// Medium confidence: action keywords near path
	if strings.Contains(lineLower, "creat") || strings.Contains(lineLower, "new") {
		return FileActionCreated, 0.6
	}
	if strings.Contains(lineLower, "modif") || strings.Contains(lineLower, "edit") ||
		strings.Contains(lineLower, "updat") || strings.Contains(lineLower, "chang") {
		return FileActionModified, 0.6
	}
	if strings.Contains(lineLower, "delet") || strings.Contains(lineLower, "remov") {
		return FileActionDeleted, 0.6
	}
	if strings.Contains(lineLower, "read") || strings.Contains(lineLower, "open") ||
		strings.Contains(lineLower, "load") || strings.Contains(lineLower, "view") {
		return FileActionRead, 0.6
	}

	return FileActionUnknown, 0.3
}

// ExtractCommands extracts shell commands from output.
// Looks for lines starting with $, %, >, or common shell patterns.
func ExtractCommands(content string) []CommandMention {
	var commands []CommandMention
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		lineNum := i + 1
		trimmed := strings.TrimSpace(line)

		if len(trimmed) == 0 {
			continue
		}

		// Check for command prompt patterns
		var cmd string

		// $ command (most common)
		if strings.HasPrefix(trimmed, "$ ") {
			cmd = strings.TrimPrefix(trimmed, "$ ")
		} else if strings.HasPrefix(trimmed, "% ") {
			cmd = strings.TrimPrefix(trimmed, "% ")
		} else if strings.HasPrefix(trimmed, "> ") && !strings.HasPrefix(trimmed, ">>") {
			// > but not >> (append redirect)
			cmd = strings.TrimPrefix(trimmed, "> ")
		} else if strings.HasPrefix(trimmed, ">>> ") {
			// Python REPL - skip
			continue
		}

		if cmd != "" {
			mention := CommandMention{
				Command: cmd,
				LineNum: lineNum,
			}

			// Try to find exit code in subsequent lines
			if i+1 < len(lines) {
				nextLine := strings.TrimSpace(lines[i+1])
				// Look for "exit code: N" or similar patterns
				if exitCode := parseExitCode(nextLine); exitCode != nil {
					mention.ExitCode = exitCode
				}
			}

			commands = append(commands, mention)
		}
	}

	return commands
}

var exitCodePatterns = []*regexp.Regexp{
	regexp.MustCompile(`exit(?:\s+code)?[:\s]+(\d+)`),
	regexp.MustCompile(`returned?\s+(\d+)`),
	regexp.MustCompile(`status[:\s]+(\d+)`),
	regexp.MustCompile(`\[(\d+)\]$`),
}

// parseExitCode tries to parse an exit code from a line.
func parseExitCode(line string) *int {
	lineLower := strings.ToLower(line)

	for _, re := range exitCodePatterns {
		if match := re.FindStringSubmatch(lineLower); len(match) > 1 {
			var code int
			if _, err := fmt.Sscanf(match[1], "%d", &code); err == nil {
				return &code
			}
		}
	}

	return nil
}

// SessionSummary provides an overview of agent activity in a session.
type SessionSummary struct {
	Session     string               `json:"session"`
	TimeRange   SummaryTimeRange     `json:"time_range"`
	Agents      []AgentOutputSummary `json:"agents"`
	TotalFiles  int                  `json:"total_files"`
	TotalOutput int                  `json:"total_output_lines"`
	Conflicts   []FileConflict       `json:"conflicts,omitempty"`
	Timestamp   string               `json:"timestamp"`
}

// SummaryTimeRange represents a time period for the summary.
type SummaryTimeRange struct {
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	Duration time.Duration `json:"duration"`
}

// AgentOutputSummary provides metrics and highlights for a single agent.
type AgentOutputSummary struct {
	PaneID    string `json:"pane_id"`
	PaneTitle string `json:"pane_title,omitempty"`
	AgentType string `json:"agent_type"`

	// Activity metrics
	ActiveTime time.Duration `json:"active_time"` // Time in generating state
	// IdleTime is omitted while unset rather than serialized as 0. Nothing
	// tracks waiting-state duration.
	IdleTime    time.Duration `json:"idle_time,omitempty"` // Time in waiting state
	OutputLines int           `json:"output_lines"`
	OutputChars int           `json:"output_chars"`

	// Content summary
	FilesModified []string `json:"files_modified,omitempty"`
	CodeBlocks    int      `json:"code_blocks"`
	Commands      int      `json:"commands_run"`

	// Health
	Errors   int `json:"errors"`
	Restarts int `json:"restarts"`

	// Highlights
	KeyActions []string `json:"key_actions,omitempty"`
	State      string   `json:"state"` // Current state: generating, idle, error
}

// SessionSummaryResponse is the robot command response for session summary.
type SessionSummaryResponse struct {
	RobotResponse
	Summary *SessionSummary `json:"summary,omitempty"`
}

// AgentActivityData holds raw data for summarizing an agent.
type AgentActivityData struct {
	PaneID      string
	PaneTitle   string
	AgentType   string
	Output      string
	State       string
	ActiveStart *time.Time
	ActiveEnd   *time.Time
}

// GenerateSummary creates a session summary from collected data, as of
// now, with the conflict detector's conflicts.
func GenerateSummary(session string, since time.Duration, now time.Time, agentData []AgentActivityData, conflicts []DetectedConflict) *SessionSummary {
	now = now.UTC()
	sinceTime := now.Add(-since)

	summary := &SessionSummary{
		Session: session,
		TimeRange: SummaryTimeRange{
			Start:    sinceTime,
			End:      now,
			Duration: since,
		},
		Agents:    make([]AgentOutputSummary, 0, len(agentData)),
		Timestamp: now.Format(time.RFC3339),
	}

	fileSet := make(map[string]bool)

	for _, data := range agentData {
		agentSummary := summarizeAgent(data)
		summary.Agents = append(summary.Agents, agentSummary)
		summary.TotalOutput += agentSummary.OutputLines

		for _, f := range agentSummary.FilesModified {
			fileSet[f] = true
		}
	}

	summary.TotalFiles = len(fileSet)

	for _, c := range conflicts {
		agents := c.LikelyModifiers
		if agents == nil {
			agents = []string{}
		}
		summary.Conflicts = append(summary.Conflicts, FileConflict{
			Path:   c.Path,
			Agents: agents,
		})
	}

	return summary
}

// summarizeAgent creates an agent summary from activity data.
func summarizeAgent(data AgentActivityData) AgentOutputSummary {
	summary := AgentOutputSummary{
		PaneID:    data.PaneID,
		PaneTitle: data.PaneTitle,
		AgentType: data.AgentType,
		State:     data.State,
	}

	// Calculate activity times
	if data.ActiveStart != nil && data.ActiveEnd != nil {
		summary.ActiveTime = data.ActiveEnd.Sub(*data.ActiveStart)
	}

	// Count output
	if data.Output != "" {
		lines := strings.Split(data.Output, "\n")
		summary.OutputLines = len(lines)
		summary.OutputChars = len(data.Output)

		// Extract structures from output
		codeBlocks := ExtractCodeBlocks(data.Output)
		summary.CodeBlocks = len(codeBlocks)

		commands := ExtractCommands(data.Output)
		summary.Commands = len(commands)

		fileMentions := ExtractFileMentions(data.Output)
		for _, fm := range fileMentions {
			if fm.Action == FileActionCreated || fm.Action == FileActionModified {
				summary.FilesModified = append(summary.FilesModified, fm.Path)
			}
		}

		// Count errors
		summary.Errors = countErrors(lines)

		// Extract key actions
		summary.KeyActions = extractKeyActions(lines, 5)
	}

	return summary
}

// countErrors counts error indicators in output lines.
func countErrors(lines []string) int {
	count := 0
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error:") ||
			strings.Contains(lower, "error!") ||
			strings.Contains(lower, "failed:") ||
			strings.Contains(lower, "fatal:") ||
			strings.Contains(lower, "panic:") {
			count++
		}
	}
	return count
}

// extractKeyActions extracts key action highlights from output lines.
// Looks for action verbs and summaries, limited to maxActions.
func extractKeyActions(lines []string, maxActions int) []string {
	var actions []string
	seen := make(map[string]bool)

	// Patterns indicating key actions (prioritized)
	actionPatterns := []struct {
		keywords []string
		priority int
	}{
		{[]string{"created ", "creating "}, 1},
		{[]string{"modified ", "modifying ", "updated ", "updating "}, 2},
		{[]string{"fixed ", "fixing "}, 1},
		{[]string{"added ", "adding "}, 2},
		{[]string{"removed ", "removing ", "deleted ", "deleting "}, 2},
		{[]string{"implemented ", "implementing "}, 1},
		{[]string{"refactored ", "refactoring "}, 2},
		{[]string{"tested ", "testing "}, 3},
	}

	// Also look for summary sections
	summaryPrefixes := []string{
		"summary:",
		"changes made:",
		"completed:",
		"done:",
		"result:",
	}

	type actionCandidate struct {
		text     string
		priority int
	}
	var candidates []actionCandidate

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 10 || len(trimmed) > 200 {
			continue
		}
		lower := strings.ToLower(trimmed)

		// Check for summary sections
		for _, prefix := range summaryPrefixes {
			if strings.HasPrefix(lower, prefix) {
				content := strings.TrimSpace(trimmed[len(prefix):])
				if content != "" && !seen[content] {
					seen[content] = true
					candidates = append(candidates, actionCandidate{content, 0})
				}
			}
		}

		// Check for action patterns
		for _, pattern := range actionPatterns {
			for _, keyword := range pattern.keywords {
				if strings.Contains(lower, keyword) {
					action := cleanActionLine(trimmed)
					if action != "" && !seen[action] {
						seen[action] = true
						candidates = append(candidates, actionCandidate{action, pattern.priority})
					}
					break
				}
			}
		}
	}

	// Sort by priority and take top maxActions
	// Lower priority number = more important
	for p := 0; p <= 3; p++ {
		for _, c := range candidates {
			if c.priority == p {
				actions = append(actions, c.text)
				if len(actions) >= maxActions {
					return actions
				}
			}
		}
	}

	return actions
}

// cleanActionLine cleans up an action line for display.
func cleanActionLine(line string) string {
	// Remove common prefixes like bullet points, numbers, timestamps
	line = strings.TrimSpace(line)

	// Remove leading bullets/dashes/numbers
	prefixPatterns := []string{
		"- ", "* ", "• ", "◦ ",
		"1. ", "2. ", "3. ", "4. ", "5. ",
		"[x] ", "[ ] ",
	}
	for _, prefix := range prefixPatterns {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimPrefix(line, prefix)
			break
		}
	}

	// Truncate if too long
	r := []rune(line)
	if len(r) > 80 {
		// Find a good break point
		line80 := string(r[:80])
		if idx := strings.LastIndex(line80, " "); idx > 40 {
			line = line80[:idx] + "..."
		} else {
			line = string(r[:77]) + "..."
		}
	}

	return strings.TrimSpace(line)
}
