package cli

import (
	"fmt"
	"os"
	"testing"
)

// TestMain keeps the package's tests off the user's data and config: a
// send writes history.jsonl under XDG_DATA_HOME, and the lifecycle verbs
// read nhm's config under XDG_CONFIG_HOME and NHM_BEAD_DBS. Every test
// gets temporary ones (a test may still set its own).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nhm-cli-test-data-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{"XDG_DATA_HOME": dir + "/data", "XDG_CONFIG_HOME": dir + "/config"} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	_ = os.Unsetenv("NHM_BEAD_DBS")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
