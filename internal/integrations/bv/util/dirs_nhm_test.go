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

// The project is the nearest folder with nhm's .nhm/config.toml; ntm's
// .ntm/config.toml does not make one, so the beads root wins over it
// (nhm-2p6.28.2).
func TestResolveProjectDirFindsNhmsProjectConfig(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	deep := filepath.Join(sub, "deep")
	for _, d := range []string{filepath.Join(root, ".beads"), filepath.Join(sub, ".ntm"), deep} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sub, ".ntm", "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveProjectDir(deep); got != root {
		t.Fatalf("with .ntm/config.toml: %q, want the beads root %q", got, root)
	}
	if err := os.MkdirAll(filepath.Join(sub, ".nhm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".nhm", "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveProjectDir(deep); got != sub {
		t.Fatalf("with .nhm/config.toml: %q, want %q", got, sub)
	}
}
