package worksource

import (
	"fmt"
	"os"
	"testing"

	// An absolute TMPDIR for these tests (internal/testenv, nhm-mwo).
	_ "github.com/arosl/nhm/internal/testenv"
)

// TestMain gives the test binary a TMPDIR of its own under /tmp, outside
// any checkout: ntm's tests build Git and non-Git fixtures in temporary
// directories, and under a TMPDIR inside a checkout (a gate's relative
// TMPDIR) each fixture would sit inside that checkout's repository.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("/tmp", "nhm-worksource-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("TMPDIR", root)
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
