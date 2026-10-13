// Package testenv makes a test binary that imports it run with an absolute
// TMPDIR outside any checkout. Only _test.go files import it, for this side
// effect:
//
//	import _ "github.com/arosl/nhm/internal/testenv"
//
// nhm does not use a relative XDG directory, as the XDG Base Directory
// spec says: store.StateRoot, quota's record and the template loader
// ignore it, and os.UserConfigDir returns an error for it. nhm's tests
// set those directories to t.TempDir(). Under a relative TMPDIR (a gate's
// `env -i TMPDIR=rel go test`, nhm-mwo) t.TempDir() is relative too, and
// the tests would fall back to the user's real state and config
// directories, or fail.
//
// The relative TMPDIR is not made absolute where it stands: go test runs in
// the package's directory, so it would lie inside nhm's git work tree, and
// a test project made there resolves to nhm's own root (nhm-cvw). A fresh
// directory under /tmp takes its place.
package testenv

import (
	"os"
	"path/filepath"
)

func init() {
	if d := os.Getenv("TMPDIR"); d != "" && !filepath.IsAbs(d) {
		if dir, err := outsideTempDir(); err == nil {
			_ = os.Setenv("TMPDIR", dir)
		} else if abs, err := filepath.Abs(d); err == nil {
			_ = os.Setenv("TMPDIR", abs)
		}
	}
}

// outsideTempDir makes a fresh directory under /tmp, outside any checkout.
func outsideTempDir() (string, error) {
	return os.MkdirTemp("/tmp", "nhm-test-")
}
