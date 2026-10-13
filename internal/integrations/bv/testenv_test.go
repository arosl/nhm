package bv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	// An absolute TMPDIR for these tests (internal/testenv, nhm-mwo).
	_ "github.com/arosl/nhm/internal/testenv"
)

// TestMain gives the test binary a temporary root of its own under /tmp,
// outside any checkout: ntm's project resolution walks up to a git root, so
// under a TMPDIR inside a checkout (a gate's relative TMPDIR) every test
// project would resolve to the checkout itself. The claim lock lives under
// nhm's state root, so that goes there too, never into the user's.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("/tmp", "nhm-bv-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tmp := filepath.Join(root, "tmp")
	state := filepath.Join(root, "state")
	for _, dir := range []string{tmp, state} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}
	_ = os.Setenv("TMPDIR", tmp)
	_ = os.Setenv("XDG_STATE_HOME", state)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
