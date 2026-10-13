package assignment

import (
	"fmt"
	"os"
	"testing"

	// An absolute TMPDIR for these tests (internal/testenv, nhm-mwo).
	_ "github.com/arosl/nhm/internal/testenv"
)

// testHomeEnv marks a test binary whose HOME is already a temporary one: the
// e2e tests start helper processes from this binary with the parent's
// environment and a HOME of their choosing, which must stay theirs.
const testHomeEnv = "NHM_ASSIGNMENT_TEST_HOME"

// TestMain keeps every test away from the user's state. The ledger lives
// under nhm's state root, which prefers XDG_STATE_HOME and falls back to
// HOME. ntm's tests isolate the ledger by setting HOME, and some set
// nothing, so the binary drops XDG_STATE_HOME and starts from a temporary
// HOME of its own; a test's own t.Setenv("HOME", …) still wins.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("XDG_STATE_HOME")
	if os.Getenv(testHomeEnv) != "" {
		os.Exit(m.Run())
	}
	home, err := os.MkdirTemp("", "nhm-assignment-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv(testHomeEnv, home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
