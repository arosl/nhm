package history

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendAndRead(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if got, want := StoragePath(), filepath.Join(os.Getenv("XDG_DATA_HOME"), "nhm", "history.jsonl"); got != want {
		t.Fatalf("StoragePath = %q, want %q", got, want)
	}
	entries, err := ReadAll()
	if err != nil || len(entries) != 0 {
		t.Fatalf("ReadAll on no file = %v, %v", entries, err)
	}

	e := NewEntry("flywheel", []string{"1", "2"}, "hello", SourceCLI)
	e.OperationID = "op-1"
	e.SetSuccess()
	if err := Append(e); err != nil {
		t.Fatal(err)
	}
	failed := NewEntry("flywheel", []string{"3"}, "again", SourcePalette)
	failed.SetError(errors.New("refused (blocked)"))
	if err := Append(failed); err != nil {
		t.Fatal(err)
	}
	if err := Append(nil); err != nil {
		t.Fatal(err)
	}

	entries, err = ReadAll()
	if err != nil || len(entries) != 2 {
		t.Fatalf("ReadAll = %d entries, %v", len(entries), err)
	}
	if entries[0].OperationID != "op-1" || !entries[0].Success || entries[1].Error != "refused (blocked)" {
		t.Fatalf("entries = %+v", entries)
	}
	recent, err := ReadRecent(1)
	if err != nil || len(recent) != 1 || recent[0].Prompt != "again" {
		t.Fatalf("ReadRecent(1) = %+v, %v", recent, err)
	}

	info, err := os.Stat(StoragePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode = %v, %v", info.Mode().Perm(), err)
	}
}

// A line without operation_id is ntm's format; nhm reads it, and nhm's
// own lines keep ntm's keys.
func TestNtmCompatibleLines(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(StoragePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	ntmLine := `{"id":"1-ab","ts":"2026-10-12T00:00:00Z","session":"s","targets":["1"],"prompt":"p","source":"robot","success":true}`
	if err := os.WriteFile(StoragePath(), []byte(ntmLine+"\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadAll()
	if err != nil || len(entries) != 1 || entries[0].Source != SourceRobot {
		t.Fatalf("ReadAll = %+v, %v", entries, err)
	}
	data, err := json.Marshal(NewEntry("s", []string{"1"}, "p", SourceCLI))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id"`, `"ts"`, `"session"`, `"targets"`, `"prompt"`, `"source"`, `"success"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("encoded entry lacks %s: %s", key, data)
		}
	}
	if strings.Contains(string(data), "operation_id") {
		t.Errorf("empty operation_id was encoded: %s", data)
	}
}

func TestConcurrentAppendsKeepWholeLines(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Append(NewEntry("s", []string{"1"}, strings.Repeat("x", 10000), SourceCLI)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	entries, err := ReadAll()
	if err != nil || len(entries) != 20 {
		t.Fatalf("ReadAll = %d entries, %v", len(entries), err)
	}
}

func TestOversizedEntryIsRefused(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Append(NewEntry("s", nil, strings.Repeat("x", maxHistoryLineBytes), SourceCLI)); err == nil {
		t.Fatal("an entry larger than the reader's limit was written")
	}
}
