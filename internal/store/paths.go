// Package store holds nhm's state: one directory and one SQLite file per
// enrolled herdr instance, under $XDG_STATE_HOME/nhm (plan-v8 sections 1
// and 3).
//
//	~/.local/state/nhm/<instance-id>/   mode 0700
//	  instance.toml   the instance id, the herdr session, the socket path
//	  nhm.sqlite3     bindings, operations, receipts, ... (schema.go)
//	  owner.toml      the owner switch (internal/core/owner.go)
//	  owner.lock      the flock that the owner switch and the mutators take
//	  secrets/        registration tokens by binding id, mode 0600
//	  transcripts/    preserved transcripts
//	  runs/           ensemble runs
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StateRoot returns $XDG_STATE_HOME/nhm, or ~/.local/state/nhm. A
// relative XDG_STATE_HOME is ignored: the XDG Base Directory spec calls it
// invalid, as os.UserConfigDir does for XDG_CONFIG_HOME, and it would name
// another state directory in each working directory (nhm-mwo).
func StateRoot() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "nhm"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("store: no home directory and no XDG_STATE_HOME")
	}
	return filepath.Join(home, ".local", "state", "nhm"), nil
}

// Instance directory entries.
const (
	InstanceFile   = "instance.toml"
	DatabaseFile   = "nhm.sqlite3"
	OwnerFile      = "owner.toml"
	OwnerLockFile  = "owner.lock"
	SecretsDir     = "secrets"
	TranscriptsDir = "transcripts"
	RunsDir        = "runs"
)

// WriteFileAtomic writes data to path through a temporary file in the same
// directory and a rename, so a reader sees the old file or the new one.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := writeFileAtomic(path, data, perm); err != nil {
		return fmt.Errorf("store: write %s: %w", path, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
