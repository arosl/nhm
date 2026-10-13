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

// An install as install.sh does it: the new release goes in beside the
// old one, and the links switch (releases/<version>, current). The old
// file does not change; the board still sees the upgrade, by its launch
// path (the plugin's "<plugin dir>/../nhm", a link through current).
func TestBinaryWatchSeesALinkSwitch(t *testing.T) {
	dir := t.TempDir()
	release := func(name, version string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "releases", name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "releases", name, "nhm"), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		relink(t, name, filepath.Join(dir, "releases", version))
		relink(t, filepath.Join("releases", version), filepath.Join(dir, "current"))
	}
	release("0.1.6@aaa.1", "0.1.6")
	launch := filepath.Join(dir, "nhm")
	if err := os.Symlink(filepath.Join("current", "nhm"), launch); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "releases", "0.1.6@aaa.1", "nhm")
	w, err := WatchBinary(launch)
	if err != nil || w.Path() != old {
		t.Fatalf("WatchBinary = %v, %v", w, err)
	}
	if err := w.Check(); err != nil {
		t.Fatalf("before the install: %v", err)
	}
	release("0.1.7@bbb.2", "0.1.7")
	if err := w.Check(); !errors.Is(err, ErrUpgraded) {
		t.Fatalf("after current switched to a new release: %v", err)
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "0.1.6@aaa.1" {
		t.Fatalf("the old release changed: %q, %v", data, err)
	}
	// The same version installed again: releases/<version> switches.
	w, _ = WatchBinary(launch)
	release("0.1.7@ccc.3", "0.1.7")
	if err := w.Check(); !errors.Is(err, ErrUpgraded) {
		t.Fatalf("after the same version was installed again: %v", err)
	}
}

// relink points the link at target by one rename, as install.sh does.
func relink(t *testing.T, target, link string) {
	t.Helper()
	tmp := link + ".new"
	if err := os.Symlink(target, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
}

// A board that cannot watch its binary is frozen from the start.
func TestUnwatchedBinaryIsFrozen(t *testing.T) {
	if _, err := WatchBinary(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing binary was watched")
	}
	err := Unwatched(errors.New("no such file")).Check()
	if !errors.Is(err, ErrUnwatched) || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("Check = %v", err)
	}
}

// The launch path is os.Args[0] made absolute, a relative one too.
func TestLaunchPathIsAbsolute(t *testing.T) {
	arg0 := os.Args[0]
	t.Cleanup(func() { os.Args[0] = arg0 })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	os.Args[0] = filepath.Join("..", "nhm")
	if got, err := LaunchPath(); err != nil || got != filepath.Join(filepath.Dir(cwd), "nhm") {
		t.Fatalf("LaunchPath = %q, %v", got, err)
	}
	os.Args[0] = arg0
	if got, err := LaunchPath(); err != nil || !filepath.IsAbs(got) {
		t.Fatalf("LaunchPath = %q, %v", got, err)
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
