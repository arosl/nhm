// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/errors_test.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the TestIsRobotErrorLine_*
// cases as one table without subtests, and TestRobotErrorPatterns_NotEmpty;
// the agent type detection tests are left out (herdr's kinds replace
// ntm's detection).

package robot

import "testing"

func TestIsRobotErrorLine(t *testing.T) {
	for _, c := range []struct {
		line, wantType string // wantType "" for no match
	}{
		// Python
		{"Traceback (most recent call last)", "traceback"},
		{"traceback (most recent call last)", "traceback"},
		{"FileNotFoundError: [Errno 2] No such file", "exception"},
		{"ImportError: No module named 'foo'", "exception"},
		{"  TypeError: unsupported operand type", "exception"},
		{"ValueError: invalid literal", "exception"},
		{"KeyError: 'missing_key'", "exception"},
		{"IndexError: list index out of range", "exception"},
		{"RuntimeError: maximum recursion depth", "exception"},
		{"NameError: name 'x' is not defined", "exception"},
		{"SyntaxError: unexpected EOF", "exception"},
		{"ZeroDivisionError: division by zero", "exception"},
		{"PermissionError: [Errno 13]", "exception"},
		{"ConnectionError: connection refused", "exception"},
		{"TimeoutError: timed out", "exception"},
		// Go
		{"panic: runtime error: index out of range", "panic"},
		{"goroutine 1 [running]:", "panic"},
		{"fatal error: concurrent map writes", "fatal"},
		// JavaScript
		{"TypeError: Cannot read properties of undefined", "exception"},
		{"ReferenceError: x is not defined", "exception"},
		{"SyntaxError: Unexpected token", "exception"},
		{"Error: ENOENT: no such file or directory", "error"},
		{"Error: EACCES: permission denied", "error"},
		// Rust
		{"thread 'main' panicked at 'index out of bounds'", "panic"},
		{"error[E0308]: mismatched types", "error"},
		// Generic
		{"error: something went wrong", "error"},
		{"Error: bad request", "error"},
		{"ERROR: disk full", "error"},
		{"ERROR disk full", "error"},
		{"FATAL: out of memory", "fatal"},
		{"FATAL out of memory", "fatal"},
		{"CRITICAL: service down", "critical"},
		{"CRITICAL service down", "critical"},
		// Build and test failures
		{"FAIL\tgithub.com/foo/bar\t0.5s", "failed"},
		{"--- FAIL: TestSomething (0.01s)", "failed"},
		{"build failed", "failed"},
		{"compilation failed", "failed"},
		{"FAILED", "failed"},
		// Exit codes; 0 is not an error
		{"exit code 1", "exit"},
		{"exited with code 127", "exit"},
		{"exit status 2", "exit"},
		{"Process exited with code 1", "exit"},
		{"exit code 0", ""},
		{"exit status 0", ""},
		// Agent errors
		{"rate limit exceeded", "rate_limit"},
		{"too many requests", "rate_limit"},
		{"429 Too Many Requests", "rate_limit"},
		{"context window exceeded", "context_limit"},
		{"context limit reached", "context_limit"},
		{"claude error: api timeout", "agent_error"},
		// Non-errors
		{"", ""}, {"   ", ""}, {"Hello world", ""}, {"Processing file...", ""},
		{"ok\tgithub.com/foo/bar\t0.5s", ""}, {"--- PASS: TestSomething (0.01s)", ""}, {"Build succeeded", ""},
		{"normal log output here", ""}, {"no errors found", ""},
		// The stacktrace pattern wants leading space, and the line is
		// trimmed first: a stack frame never matches, as in ntm.
		{"    at Object.<anonymous> (/app/index.js:10:5)", ""},
	} {
		got, typ := IsRobotErrorLine(c.line)
		if got != (c.wantType != "") || typ != c.wantType {
			t.Errorf("IsRobotErrorLine(%q) = %v, %q; want %q", c.line, got, typ, c.wantType)
		}
	}
}

func TestRobotErrorPatterns_NotEmpty(t *testing.T) {
	if len(robotErrorPatterns) == 0 {
		t.Fatal("robotErrorPatterns should not be empty")
	}
	for i, p := range robotErrorPatterns {
		if p.Pattern == nil || p.MatchType == "" {
			t.Errorf("pattern %d is incomplete", i)
		}
	}
}
