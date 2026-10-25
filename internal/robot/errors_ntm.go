// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/errors.go (ErrorEntry, ErrorsSummary, ErrorsOutput,
// robotErrorPatterns, isRobotErrorLine) at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: isRobotErrorLine is
// exported as IsRobotErrorLine; the session read (GetErrors, PrintErrors)
// is nhm's, on herdr's panes (internal/cli).

package robot

import (
	"regexp"
	"strings"
	"time"
)

// ErrorEntry represents a single error detected in pane output.
type ErrorEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	PaneID     string    `json:"pane_id"`
	PaneName   string    `json:"pane_name"`
	PaneIndex  int       `json:"pane_index"`
	AgentType  string    `json:"agent_type"`
	LineNumber int       `json:"line_number"`
	Content    string    `json:"content"`
	MatchType  string    `json:"match_type"`
	Context    []string  `json:"context,omitempty"`
}

// ErrorsSummary provides aggregate statistics.
type ErrorsSummary struct {
	TotalErrors   int            `json:"total_errors"`
	TotalLines    int            `json:"total_lines_searched"`
	PanesSearched int            `json:"panes_searched"`
	ByType        map[string]int `json:"by_type"`
	ByAgent       map[string]int `json:"by_agent"`
}

// ErrorsOutput is the response for --robot-errors.
type ErrorsOutput struct {
	RobotResponse
	Session string        `json:"session"`
	Errors  []ErrorEntry  `json:"errors"`
	Summary ErrorsSummary `json:"summary"`
}

// Error pattern matchers for robot errors command
var robotErrorPatterns = []struct {
	Pattern   *regexp.Regexp
	MatchType string
}{
	// Python errors
	{regexp.MustCompile(`(?i)^Traceback \(most recent call last\)`), "traceback"},
	{regexp.MustCompile(`(?i)^\s*(FileNotFoundError|ImportError|ModuleNotFoundError|AttributeError|TypeError|ValueError|KeyError|IndexError|RuntimeError|NameError|SyntaxError|IndentationError|ZeroDivisionError|AssertionError|OSError|PermissionError|ConnectionError|TimeoutError):`), "exception"},

	// Go errors
	{regexp.MustCompile(`(?i)^panic:`), "panic"},
	{regexp.MustCompile(`(?i)^goroutine \d+ \[running\]:`), "panic"},
	{regexp.MustCompile(`(?i)fatal error:`), "fatal"},

	// JavaScript/Node errors
	{regexp.MustCompile(`(?i)^(TypeError|ReferenceError|SyntaxError|RangeError|EvalError|URIError):`), "exception"},
	{regexp.MustCompile(`(?i)Error: ENOENT|EACCES|EEXIST|ENOTDIR|EISDIR|EMFILE|EPERM`), "error"},

	// Rust errors
	{regexp.MustCompile(`(?i)^thread '.+' panicked at`), "panic"},
	{regexp.MustCompile(`(?i)^error\[E\d+\]:`), "error"},

	// Generic error patterns
	{regexp.MustCompile(`(?i)^error:`), "error"},
	{regexp.MustCompile(`(?i)^Error:`), "error"},
	{regexp.MustCompile(`(?i)^ERROR[:\s]`), "error"},
	{regexp.MustCompile(`(?i)^FATAL[:\s]`), "fatal"},
	{regexp.MustCompile(`(?i)^CRITICAL[:\s]`), "critical"},

	// Build/test failures
	{regexp.MustCompile(`(?i)\bfailed\b.*:|\bFAILED\b`), "failed"},
	{regexp.MustCompile(`(?i)^FAIL\s+`), "failed"},
	{regexp.MustCompile(`(?i)^--- FAIL:`), "failed"},
	{regexp.MustCompile(`(?i)build failed`), "failed"},
	{regexp.MustCompile(`(?i)compilation failed`), "failed"},

	// Exit codes
	{regexp.MustCompile(`(?i)exit(?:ed)?\s+(?:with\s+)?(?:code|status)\s+[1-9]\d*`), "exit"},
	{regexp.MustCompile(`(?i)Process exited with code [1-9]\d*`), "exit"},

	// Stack traces
	{regexp.MustCompile(`^\s+at\s+.+\(.+:\d+:\d+\)`), "stacktrace"},

	// Agent-specific errors
	{regexp.MustCompile(`(?i)^claude.*error`), "agent_error"},
	{regexp.MustCompile(`(?i)rate limit|too many requests|429`), "rate_limit"},
	{regexp.MustCompile(`(?i)context.*(?:exceeded|limit|full|window)`), "context_limit"},
}

// IsRobotErrorLine checks if a line matches any error pattern.
func IsRobotErrorLine(line string) (bool, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, ""
	}

	for _, p := range robotErrorPatterns {
		if p.Pattern.MatchString(line) {
			return true, p.MatchType
		}
	}
	return false, ""
}
