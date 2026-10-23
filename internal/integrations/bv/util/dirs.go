// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/util/dirs.go at commit 9d5ed977; see LICENSE in this directory
// and PROVENANCE.md. Local changes: this header and the package comment;
// only ResolveProjectDir and what it calls (FindProjectConfigRoot,
// FindBeadsRoot, FindGitRoot with its cache) are kept, for the bv port's
// normalizeTriageDir. NTMDir, ExpandPath, EnsureDir and the project scoring
// are left out. findGitRootUncached sets cmd.WaitDelay (nhm-fty).

// Package util is the part of ntm's util package that nhm's port of ntm's
// bv package calls.
package util

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// gitRootCache memoizes FindGitRoot results per start directory so hot paths
// (e.g. --robot-status resolving project dirs per collection) do not spawn
// `git rev-parse --show-toplevel` repeatedly for the same directory.
// Entries expire after gitRootCacheTTL; the map is bounded at
// gitRootCacheMaxEntries and reset wholesale when full (bd-6afgy).
var gitRootCache = struct {
	sync.Mutex
	entries map[string]gitRootCacheEntry
}{entries: make(map[string]gitRootCacheEntry)}

type gitRootCacheEntry struct {
	root    string
	err     error
	expires time.Time
}

const (
	gitRootCacheTTL        = 30 * time.Second
	gitRootCacheMaxEntries = 256
)

// FindGitRoot attempts to find the root of the git repository
// containing the given directory. Returns empty string if not found.
// Results (including failures) are cached for a short TTL per start
// directory; repo moves/deletions are picked up after the TTL lapses.
func FindGitRoot(startDir string) (string, error) {
	now := time.Now()

	gitRootCache.Lock()
	if entry, ok := gitRootCache.entries[startDir]; ok && now.Before(entry.expires) {
		gitRootCache.Unlock()
		return entry.root, entry.err
	}
	gitRootCache.Unlock()

	root, err := findGitRootUncached(startDir)

	gitRootCache.Lock()
	if len(gitRootCache.entries) >= gitRootCacheMaxEntries {
		gitRootCache.entries = make(map[string]gitRootCacheEntry)
	}
	gitRootCache.entries[startDir] = gitRootCacheEntry{
		root:    root,
		err:     err,
		expires: now.Add(gitRootCacheTTL),
	}
	gitRootCache.Unlock()

	return root, err
}

func findGitRootUncached(startDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = startDir
	cmd.WaitDelay = time.Second // nhm-fty: a child of git that holds stdout open
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// FindProjectConfigRoot walks up from startDir looking for a local .ntm/config.toml.
// It returns the directory that owns the project config.
func FindProjectConfigRoot(startDir string) (string, error) {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		configPath := filepath.Join(dir, ".ntm", "config.toml")
		if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("project config not found from %s", startDir)
		}
		dir = parent
	}
}

// FindBeadsRoot walks up from startDir looking for a local .beads directory.
// It returns the directory that owns the beads state.
func FindBeadsRoot(startDir string) (string, error) {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for {
		beadsPath := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsPath); err == nil && info.IsDir() {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("beads root not found from %s", startDir)
		}
		dir = parent
	}
}

// ResolveProjectDir prefers an explicit project config root, then a beads root,
// then a git root, and finally falls back to the starting directory itself.
func ResolveProjectDir(startDir string) string {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		startDir = cwd
	}

	absDir, err := filepath.Abs(startDir)
	if err == nil {
		startDir = absDir
	}

	if projectDir, err := FindProjectConfigRoot(startDir); err == nil && projectDir != "" {
		return projectDir
	}
	if beadsRoot, err := FindBeadsRoot(startDir); err == nil && beadsRoot != "" {
		return beadsRoot
	}
	if gitRoot, err := FindGitRoot(startDir); err == nil && gitRoot != "" {
		return gitRoot
	}
	return filepath.Clean(startDir)
}
