// Ported from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/errors_test.go (TestIsErrorLine) at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: the
// package; the cases as one table without subtests or logging.

package panediff

import "testing"

func TestIsErrorLine(t *testing.T) {
	for _, c := range []struct {
		line, wantType string
	}{
		// Python errors
		{"Traceback (most recent call last):", "traceback"},
		{"FileNotFoundError: [Errno 2] No such file or directory: 'test.txt'", "exception"},
		{"ImportError: No module named 'nonexistent'", "exception"},
		{"TypeError: 'NoneType' object is not subscriptable", "exception"},
		{"ValueError: invalid literal for int() with base 10: 'abc'", "exception"},
		{"KeyError: 'missing_key'", "exception"},
		{"AttributeError: 'str' object has no attribute 'foo'", "exception"},
		// Go errors
		{"panic: runtime error: index out of range [5] with length 3", "panic"},
		{"goroutine 1 [running]:", "panic"},
		{"fatal error: concurrent map writes", "fatal"},
		// JavaScript/Node errors
		{"TypeError: Cannot read property 'x' of undefined", "exception"},
		{"ReferenceError: someVar is not defined", "exception"},
		{"Error: ENOENT: no such file or directory, open 'missing.txt'", "error"},
		{"Error: EACCES: permission denied, open '/root/secret'", "error"},
		// Rust errors
		{"thread 'main' panicked at 'called unwrap on None'", "panic"},
		{"error[E0382]: borrow of moved value", "error"},
		// Generic error patterns
		{"error: something went wrong", "error"},
		{"Error: configuration invalid", "error"},
		{"ERROR: database connection failed", "error"},
		{"FATAL: out of memory", "fatal"},
		{"CRITICAL: system failure detected", "critical"},
		// Build/test failures
		{"test failed: expected 5 got 3", "failed"},
		{"FAILED tests/test_auth.py::test_login", "failed"},
		{"FAIL github.com/example/pkg", "failed"},
		{"--- FAIL: TestSomething (0.00s)", "failed"},
		{"build failed: compilation error in main.go", "failed"},
		{"compilation failed with 3 errors", "failed"},
		// Exit codes
		{"Process exited with code 1", "exit"},
		{"exit status 127", "exit"},
		{"Command exited with code 2", "exit"},
		{"    at doThing (/tmp/app.js:10:5)", "stacktrace"},
		// Agent-specific errors
		{"API rate limit exceeded, please try again later", "rate_limit"},
		{"Received 429 Too Many Requests", "rate_limit"},
		{"context window exceeded, conversation too long", "context_limit"},
		// "Error:" comes before context_limit.
		{"Error: context full, please compact", "error"},
		{"context window exceeded, cannot continue", "context_limit"},
		// Non-errors
		{"Building project...", ""},
		{"INFO: Starting server on port 8080", ""},
		{"All tests passed", ""},
		{"Process exited with code 0", ""},
		{"DEBUG: processing request", ""},
		{"warning: unused variable", ""},
		{"", ""},
		{"   \t  ", ""},
		{"The word error appears in a sentence about documentation.", ""},
	} {
		got, typ := IsErrorLine(c.line)
		if got != (c.wantType != "") || typ != c.wantType {
			t.Errorf("IsErrorLine(%q) = %v, %q; want %q", c.line, got, typ, c.wantType)
		}
	}
}
