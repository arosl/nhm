package testenv

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// setsUserDirs is a test that points HOME or an XDG directory elsewhere.
var setsUserDirs = regexp.MustCompile(`Setenv\("(HOME|XDG_[A-Z_]+)"`)

// Each test package that sets HOME or an XDG directory imports testenv:
// without it, a relative TMPDIR makes t.TempDir() relative, and the tests
// use the user's real state and config directories (nhm-mwo).
func TestPackagesThatSetUserDirsImportTestenv(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	sets, imports := map[string]bool{}, map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		sets[dir] = sets[dir] || setsUserDirs.Match(data)
		imports[dir] = imports[dir] || strings.Contains(string(data), `"github.com/arosl/nhm/internal/testenv"`)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for dir, set := range sets {
		if set && !imports[dir] && filepath.Base(dir) != "testenv" {
			rel, _ := filepath.Rel(root, dir)
			missing = append(missing, rel)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these test packages set HOME or XDG_* but do not import internal/testenv: %s", strings.Join(missing, ", "))
	}
	if len(sets) == 0 {
		t.Fatal("no test files found: the walk did not start at the module root")
	}
}

// A relative TMPDIR gives way to a fresh directory under /tmp, absolute and
// outside nhm's work tree, so a test project made in it never resolves to
// nhm's own root (nhm-cvw).
func TestOutsideTempDir(t *testing.T) {
	dir, err := outsideTempDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(dir) })
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dir) || !strings.HasPrefix(dir, "/tmp/nhm-test-") || strings.HasPrefix(dir, root+string(filepath.Separator)) {
		t.Fatalf("dir = %q (work tree %q)", dir, root)
	}
}
