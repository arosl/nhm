// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/history/storage.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the file lives under
// $XDG_DATA_HOME/nhm; only StoragePath, Append, ReadAll and ReadRecent are
// kept, without ntm's privacy mode, encryption and redaction.

package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	historyFileName     = "history.jsonl"
	maxHistoryLineBytes = 5 * 1024 * 1024
)

var (
	// ErrNoHistory is returned when history file doesn't exist
	ErrNoHistory = errors.New("no history file found")

	// internal mutex for goroutine safety
	localMu sync.Mutex
)

// StoragePath returns the path to the history file.
// Uses XDG_DATA_HOME if set, otherwise ~/.local/share/nhm/history.jsonl
func StoragePath() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return historyFileName // fallback to current dir
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "nhm", historyFileName)
}

// Append adds an entry to the history file.
// Thread-safe and process-safe.
func Append(entry *HistoryEntry) error {
	if entry == nil {
		return nil
	}

	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	path := StoragePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := encodeHistoryLine(entry)
	if err != nil {
		return err
	}

	// Write line with newline atomically
	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadAll reads all history entries from the file.
// Returns empty slice if file doesn't exist. Malformed lines are skipped.
func ReadAll() ([]HistoryEntry, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	path := StoragePath()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []HistoryEntry{}, nil
		}
		return nil, err
	}
	defer f.Close()

	entries := []HistoryEntry{}
	scanner := bufio.NewScanner(f)
	// Set max line size for large prompts (5MB), start with 64KB
	scanner.Buffer(make([]byte, 64*1024), maxHistoryLineBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry HistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return entries, err
	}

	return entries, nil
}

// ReadRecent returns the last n entries, oldest first.
func ReadRecent(n int) ([]HistoryEntry, error) {
	entries, err := ReadAll()
	if err != nil {
		return nil, err
	}
	if n >= 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries, nil
}

// encodeHistoryLine enforces the reader's line limit, so Append never
// publishes a row that ReadAll cannot read.
func encodeHistoryLine(entry *HistoryEntry) ([]byte, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	// Reserve one byte for the newline consumed by bufio.Scanner.
	if len(data) >= maxHistoryLineBytes {
		return nil, fmt.Errorf("encoded history entry must be smaller than %d bytes", maxHistoryLineBytes)
	}
	return data, nil
}
