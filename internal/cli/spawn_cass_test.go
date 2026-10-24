package cli

import (
	"path/filepath"
	"testing"

	"github.com/arosl/nhm/internal/core/enrich"
)

// ntm's CASS context flags, on spawn and add: --cass-context Q turns CASS
// on with Q as its query, --cass-context-days and --cass-context-limit
// bound it (when above 0), and --no-cass-context wins (nhm-2p6.4.6).
func TestSpawnCassContextFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NHM_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	for _, add := range []bool{false, true} {
		_, req, err := parseVerbArgs(t, add, "--cc=1", "--cass-context", "login timeout", "--cass-context-days", "7",
			"--cass-context-limit", "2")
		if e := req.Enrich; err != nil || e.CASS != enrich.BestEffort || e.CASSQuery != "login timeout" || e.CASSDays != 7 || e.CASSLimit != 2 {
			t.Fatalf("add %v: enrichment = %+v %v", add, req.Enrich, err)
		}
		_, req, err = parseVerbArgs(t, add, "--cc=1", "--cass-context", "login", "--no-cass-context", "--cass-context-limit=-1")
		if e := req.Enrich; err != nil || e.CASS != enrich.Off || e.CASSLimit != 0 {
			t.Fatalf("add %v: --no-cass-context = %+v %v", add, req.Enrich, err)
		}
	}
}
