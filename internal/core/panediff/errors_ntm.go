// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/errors.go (errorsCommandPatterns, isErrorLine) at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: the package (nhm's panediff); isErrorLine is exported as
// IsErrorLine.

package panediff

import (
	"regexp"
	"strings"
)

// Error pattern matchers for the errors command
var errorsCommandPatterns = []struct {
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
	{regexp.MustCompile(`^at\s+.+\(.+:\d+:\d+\)`), "stacktrace"},

	// Agent-specific errors
	{regexp.MustCompile(`(?i)^claude.*error`), "agent_error"},
	{regexp.MustCompile(`(?i)rate limit|too many requests|429`), "rate_limit"},
	{regexp.MustCompile(`(?i)context.*(?:exceeded|limit|full|window)`), "context_limit"},
}

// IsErrorLine checks if a line matches any error pattern and returns the match type
func IsErrorLine(line string) (bool, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false, ""
	}

	for _, p := range errorsCommandPatterns {
		if p.Pattern.MatchString(line) {
			return true, p.MatchType
		}
	}
	return false, ""
}
