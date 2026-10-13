package board

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An operation is recorded once; an answer for an operation the board
// does not know, or a second answer, changes nothing.
func TestResultsRecordOnce(t *testing.T) {
	var r Results
	icy := Key{BindingID: "bnd-1", Generation: 1}
	if err := r.Begin("op-1", "interrupt", []Key{icy}, map[string]string{icy.Key(): "IcyKnoll"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := r.Begin("op-1", "interrupt", []Key{icy}, nil, t0); !errors.Is(err, ErrKnown) {
		t.Fatalf("second Begin = %v", err)
	}
	if got, _ := r.Get("op-1"); got.Targets[0].Name != "IcyKnoll" {
		t.Fatalf("result = %+v", got)
	}
	if r.InFlight() != 1 {
		t.Fatalf("in flight = %d", r.InFlight())
	}
	if r.End("op-old", OutcomeDone, "", nil, t0) {
		t.Fatal("an unknown operation was taken")
	}
	if !r.End("op-1", OutcomeUnknown, "no answer from the core", nil, t0.Add(time.Second)) {
		t.Fatal("End refused the operation")
	}
	if r.End("op-1", OutcomeDone, "", nil, t0) {
		t.Fatal("a second answer was taken")
	}
	got, ok := r.Get("op-1")
	if !ok || got.Outcome != OutcomeUnknown || got.Targets[0].Status != OutcomeUnknown || r.InFlight() != 0 {
		t.Fatalf("result = %+v", got)
	}
	for i := range maxResults + 5 {
		_ = r.Begin("op-x"+strings.Repeat("y", i), "send", nil, nil, t0)
	}
	if len(r.List()) != maxResults {
		t.Fatalf("%d results kept", len(r.List()))
	}
}

// A binary renamed over the old one, or changed in place, freezes the
// board's mutations.
func TestBinaryWatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nhm")
	if err := os.WriteFile(path, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "nhm-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	w, err := WatchBinary(link)
	if err != nil || w.Path() != path {
		t.Fatalf("WatchBinary = %v, %v", w, err)
	}
	if err := w.Check(); err != nil {
		t.Fatalf("unchanged binary: %v", err)
	}
	next := filepath.Join(dir, "nhm.new")
	if err := os.WriteFile(next, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(); !errors.Is(err, ErrUpgraded) {
		t.Fatalf("after a rename over it: %v", err)
	}
	w, _ = WatchBinary(path)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(); !errors.Is(err, ErrUpgraded) {
		t.Fatalf("after a change in place: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := w.Check(); !errors.Is(err, ErrUpgraded) {
		t.Fatalf("after a removal: %v", err)
	}
}

func TestCleanRemovesControls(t *testing.T) {
	in := "a\x1b[2J\x1b[Hb\x1b]0;evil\x07c\x1b]8;;http://x\x1b\\d\x07\x08e\r\n\tf\u009bg\x1bPh"
	if got := Clean(in); got != "abcde\n\tfgh" {
		t.Fatalf("Clean = %q", got)
	}
}

func TestBoundKeepsTheTail(t *testing.T) {
	var lines []string
	for i := range 100 {
		lines = append(lines, strings.Repeat("x", i%7)+"|")
	}
	got, cut := bound(strings.Join(lines, "\n")+"\n", 10, 1<<20)
	if !cut || strings.Count(got, "\n") != 9 || !strings.HasSuffix(got, lines[99]) {
		t.Fatalf("bound by lines = %q, %v", got, cut)
	}
	got, cut = bound("aaaa\nbbbb\ncccc", 10, 7)
	if !cut || got != "cccc" {
		t.Fatalf("bound by bytes = %q, %v", got, cut)
	}
	got, cut = bound("short", 10, 100)
	if cut || got != "short" {
		t.Fatalf("bound = %q, %v", got, cut)
	}
	// A cut in the middle of a rune leaves valid UTF-8.
	got, _ = bound("ééééé", 10, 3)
	if !strings.HasSuffix("ééééé", got) || strings.ContainsRune(got, '�') {
		t.Fatalf("bound split a rune: %q", got)
	}
}
