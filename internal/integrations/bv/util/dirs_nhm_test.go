package util

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A git that answers and exits but leaves a child that holds stdout open
// does not hold the lookup until the child exits (nhm-fty).
func TestFindGitRootEndsWhenAChildHoldsStdout(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 30 &\necho /elsewhere\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	if _, err := findGitRootUncached(t.TempDir()); !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("err = %v, want exec.ErrWaitDelay", err)
	}
	// Under the lookup's own 5 s timeout: WaitDelay ended it, not ctx.
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("git rev-parse took %v", took)
	}
}
