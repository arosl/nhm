package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A relative XDG_STATE_HOME is invalid (the XDG spec) and ignored
// (nhm-mwo).
func TestStateRootIgnoresARelativeXDGStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join("rel", "state"))
	if got, err := StateRoot(); err != nil || got != filepath.Join(home, ".local", "state", "nhm") {
		t.Errorf("relative XDG_STATE_HOME: StateRoot = %q, %v", got, err)
	}
	abs := t.TempDir()
	t.Setenv("XDG_STATE_HOME", abs)
	if got, err := StateRoot(); err != nil || got != filepath.Join(abs, "nhm") {
		t.Errorf("absolute XDG_STATE_HOME: StateRoot = %q, %v", got, err)
	}
}

// A relative instance directory opens: its database path goes into the
// SQLite URI absolute, so no part of it reads as the URI's authority
// (nhm-mwo: "invalid uri authority").
func TestOpenARelativeInstanceDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("inst", 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := Open("inst")
	if err != nil {
		t.Fatalf("Open(relative) = %v", err)
	}
	_ = db.Close()
	if _, err := os.Stat(filepath.Join(dir, "inst", DatabaseFile)); err != nil {
		t.Fatalf("the database is not in the instance directory: %v", err)
	}
	db, err = OpenForHook(context.Background(), "inst", time.Second)
	if err != nil {
		t.Fatalf("OpenForHook(relative) = %v", err)
	}
	_ = db.Close()
}
