// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/tui_parity.go (InspectPaneOutput, InspectPaneAgent,
// InspectPaneOutput_, CodeBlockInfo, InspectPaneContext, detectErrors,
// parseCodeBlocks, extractFileReferences, isLikelyFilePath) and
// internal/robot/types.go (RobotAction) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: detectErrors,
// parseCodeBlocks and extractFileReferences are exported (as
// InspectErrors, InspectCodeBlocks, InspectFileReferences);
// InspectFileReferences sorts its answer, where ntm's map order is random;
// the session read (GetInspectPane) is nhm's, on herdr's panes
// (internal/cli).

package robot

import (
	"sort"
	"strings"
)

// RobotAction represents a recommended action for an AI agent in JSON output.
type RobotAction struct {
	// Action is the type of action (e.g., "send_prompt", "wait", "spawn").
	Action string `json:"action"`

	// Target describes what the action should be applied to.
	// Example: "idle agents", "pane 2", "session myproject"
	Target string `json:"target,omitempty"`

	// Reason explains why this action is suggested.
	// Example: "2 agents available", "context at 95%"
	Reason string `json:"reason,omitempty"`

	// Priority indicates relative importance (higher = more important).
	Priority int `json:"priority,omitempty"`

	// Details provides additional information about the action.
	// Example: shell command, prompt text, or configuration.
	Details string `json:"details,omitempty"`
}

// InspectPaneOutput represents detailed pane inspection
type InspectPaneOutput struct {
	RobotResponse
	Session    string             `json:"session"`
	PaneIndex  int                `json:"pane_index"`
	PaneID     string             `json:"pane_id"`
	Agent      InspectPaneAgent   `json:"agent"`
	Output     InspectPaneOutput_ `json:"output"`
	Context    InspectPaneContext `json:"context"`
	AgentHints *AgentHints        `json:"_agent_hints,omitempty"`
}

// InspectPaneAgent contains agent-specific information
type InspectPaneAgent struct {
	Type            string  `json:"type"` // claude, codex, gemini, user
	Variant         string  `json:"variant,omitempty"`
	Title           string  `json:"title"`
	State           string  `json:"state"` // generating, waiting, thinking, error
	StateConfidence float64 `json:"state_confidence"`
	Command         string  `json:"command,omitempty"`
	ProcessRunning  bool    `json:"process_running"`
}

// InspectPaneOutput_ contains the pane output analysis
type InspectPaneOutput_ struct {
	Lines       int             `json:"lines"`                  // Total lines captured
	Characters  int             `json:"characters"`             // Total characters
	LastLines   []string        `json:"last_lines"`             // Last N lines (configurable)
	CodeBlocks  []CodeBlockInfo `json:"code_blocks,omitempty"`  // Detected code blocks
	ErrorsFound []string        `json:"errors_found,omitempty"` // Detected error messages
}

// CodeBlockInfo represents a detected code block in output
type CodeBlockInfo struct {
	Language  string `json:"language,omitempty"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	FilePath  string `json:"file_path,omitempty"` // Detected target file
}

// InspectPaneContext contains context information
type InspectPaneContext struct {
	WorkingDir     string   `json:"working_dir,omitempty"`
	RecentFiles    []string `json:"recent_files,omitempty"` // Files mentioned in output
	PendingMail    int      `json:"pending_mail"`
	CurrentBead    string   `json:"current_bead,omitempty"`
	ContextPercent float64  `json:"context_percent,omitempty"` // Estimated context usage
}

// InspectErrors scans output lines for error patterns
func InspectErrors(lines []string) []string {
	var errors []string
	errorPatterns := []string{
		"error:",
		"Error:",
		"ERROR:",
		"failed:",
		"Failed:",
		"FAILED:",
		"panic:",
		"Panic:",
		"exception:",
		"Exception:",
		"traceback",
		"Traceback",
	}

	for _, line := range lines {
		for _, pattern := range errorPatterns {
			if strings.Contains(line, pattern) {
				// Truncate long error messages
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				errors = append(errors, line)
				break
			}
		}
	}

	// Limit to 10 errors
	if len(errors) > 10 {
		errors = errors[:10]
	}

	return errors
}

// InspectCodeBlocks extracts code block information from output
func InspectCodeBlocks(lines []string) []CodeBlockInfo {
	var blocks []CodeBlockInfo
	inBlock := false
	var currentBlock CodeBlockInfo

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inBlock {
				// Start of block
				inBlock = true
				currentBlock = CodeBlockInfo{
					LineStart: i,
					Language:  strings.TrimPrefix(trimmed, "```"),
				}
			} else {
				// End of block
				currentBlock.LineEnd = i
				blocks = append(blocks, currentBlock)
				inBlock = false
			}
		}
	}

	return blocks
}

// InspectFileReferences finds file paths mentioned in output, sorted.
func InspectFileReferences(lines []string) []string {
	files := make(map[string]struct{})

	for _, line := range lines {
		// Look for common file path patterns
		// This is a simplified heuristic
		words := strings.Fields(line)
		for _, word := range words {
			// Clean up the word
			word = strings.Trim(word, "\"'`()[]{},:;")

			// Check if it looks like a file path
			if strings.Contains(word, "/") || strings.Contains(word, ".") {
				if isLikelyFilePath(word) {
					files[word] = struct{}{}
				}
			}
		}
	}

	var result []string
	for f := range files {
		result = append(result, f)
	}
	sort.Strings(result)

	// Limit results
	if len(result) > 20 {
		result = result[:20]
	}

	return result
}

// isLikelyFilePath checks if a string looks like a file path
func isLikelyFilePath(s string) bool {
	// Must have an extension or look like a path
	if !strings.Contains(s, ".") && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "./") {
		return false
	}

	// Common file extensions
	extensions := []string{".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".json", ".yaml", ".yml", ".toml", ".md", ".txt", ".css", ".html", ".sh", ".bash"}
	for _, ext := range extensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}

	// Looks like a relative or absolute path
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}

	return false
}
