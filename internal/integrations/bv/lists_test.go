// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/bv_helpers_test.go at commit 9d5ed977:
// TestGetInProgressListContextZeroLimitReturnsFullList; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; the readers'
// test project in a helper; plus nhm's tests of the limits and of
// GetBlockedListContext (nhm-y55).

package bv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listsProject makes a project with .git and .beads, and a fake br on PATH
// that prints out and logs its arguments to the returned file.
func listsProject(t *testing.T, out string) (dir, argsLog string) {
	t.Helper()
	dir = t.TempDir()
	for _, name := range []string{".git", ".beads"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	binDir := t.TempDir()
	argsLog = filepath.Join(binDir, "args")
	script := "#!/bin/sh\necho \"$*\" > " + argsLog + "\nprintf '%s\\n' '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir)
	return dir, argsLog
}

func TestGetInProgressListContextZeroLimitReturnsFullList(t *testing.T) {
	dir, _ := listsProject(t, `[{"id":"ntm-active-1","title":"first"},{"id":"ntm-active-2","title":"second"}]`)

	items, err := GetInProgressListContext(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("GetInProgressListContext error: %v", err)
	}
	if len(items) != 2 || items[0].ID != "ntm-active-1" || items[1].ID != "ntm-active-2" {
		t.Fatalf("in-progress items=%+v, want both rows", items)
	}
}

// A positive limit goes to br and caps the rows; a negative one is
// refused before br runs.
func TestGetInProgressListContextLimit(t *testing.T) {
	dir, argsLog := listsProject(t, `[{"id":"a","title":"A","assignee":"IcyKnoll","updated_at":"2026-10-23T00:00:00Z"},{"id":"b","title":"B"}]`)
	items, err := GetInProgressListContext(context.Background(), dir, 1)
	if err != nil || len(items) != 1 || items[0].Assignee != "IcyKnoll" || items[0].UpdatedAt.IsZero() {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "list --status=in_progress --json --limit 1") {
		t.Fatalf("br args = %q", args)
	}
	if _, err := GetInProgressListContext(context.Background(), dir, -1); err == nil {
		t.Fatal("a negative limit was accepted")
	}
}

// GetBlockedListContext runs br blocked --json and keeps up to limit rows,
// as ntm's: --robot-assign asks for 20.
func TestGetBlockedListContext(t *testing.T) {
	dir, argsLog := listsProject(t, `[{"id":"x","title":"X"},{"id":"y","title":"Y"},{"id":"z","title":"Z"}]`)
	items, err := GetBlockedListContext(context.Background(), dir, 2)
	if err != nil || len(items) != 2 || items[0].ID != "x" || items[1].Title != "Y" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.HasSuffix(strings.TrimSpace(string(args)), " blocked --json") {
		t.Fatalf("br args = %q", args)
	}
}
