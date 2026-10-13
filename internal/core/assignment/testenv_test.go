package assignment

import (
	"os"

	// An absolute TMPDIR for these tests (internal/testenv, nhm-mwo).
	_ "github.com/arosl/nhm/internal/testenv"
)

// ntm's tests isolate the ledger by setting HOME. nhm's state root prefers
// XDG_STATE_HOME, so the test binary, and the helper processes the e2e
// tests start from it, drop it: StorageDir then follows the test's HOME.
func init() {
	_ = os.Unsetenv("XDG_STATE_HOME")
}
