// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/watcher/watcher_test.go and internal/watcher/helpers_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: only the tests of the fsnotify watcher that watcher.go
// keeps (the polling, event filter and WithDebounceDuration tests are left
// out); the test helpers WatchedPaths and withDebouncer (ntm's
// WithDebouncer option); TestNewWatcher checks no poll mode; plus nhm's
// tests of WithMatch, a new folder's watch and running out of watches.

package watcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchedPaths returns a slice of all currently watched paths.
func (w *Watcher) WatchedPaths() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	paths := make([]string, 0, len(w.watchedPaths))
	for p := range w.watchedPaths {
		paths = append(paths, p)
	}
	return paths
}

// withDebouncer sets a custom debouncer.
func withDebouncer(d *Debouncer) Option {
	return func(w *Watcher) {
		if d != nil {
			w.debouncer = d
		}
	}
}

func TestNewWatcher(t *testing.T) {
	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if w.fsWatcher == nil {
		t.Error("fsWatcher should not be nil")
	}
	if w.debouncer == nil {
		t.Error("debouncer should not be nil")
	}
}

func TestWatcherRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	w, err := New(
		func(events []Event) {},
		WithRecursive(true),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	paths := w.WatchedPaths()
	if len(paths) != 2 {
		t.Errorf("WatchedPaths() = %v, want 2 paths (root + subdir)", paths)
	}
}

func TestWatcherRecursiveRemoveEventCleansDescendantWatchPaths(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "root")
	child := filepath.Join(root, "child")
	grandchild := filepath.Join(child, "grandchild")

	w, err := New(
		func(events []Event) {},
		WithRecursive(true),
		withDebouncer(NewDebouncer(time.Hour)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	w.mu.Lock()
	w.watchedPaths[root] = true
	w.watchedPaths[child] = true
	w.watchedPaths[grandchild] = true
	w.mu.Unlock()

	w.handleEvent(fsnotify.Event{Name: child, Op: fsnotify.Rename})

	paths := w.WatchedPaths()
	for _, path := range paths {
		if path == child || path == grandchild {
			t.Fatalf("recursive cleanup left stale watched path %q in %v", path, paths)
		}
	}
	if len(paths) != 1 || paths[0] != root {
		t.Fatalf("expected only root to remain watched, got %v", paths)
	}
}

func TestWatcherRecursiveCreateEventAddsNestedDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	grandchild := filepath.Join(child, "grandchild")
	if err := os.MkdirAll(grandchild, 0o755); err != nil {
		t.Fatalf("mkdir nested tree failed: %v", err)
	}

	w, err := New(
		func(events []Event) {},
		WithRecursive(true),
		withDebouncer(NewDebouncer(time.Hour)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	w.handleEvent(fsnotify.Event{Name: child, Op: fsnotify.Create})

	paths := w.WatchedPaths()
	want := map[string]bool{
		child:      false,
		grandchild: false,
	}
	for _, path := range paths {
		if _, ok := want[path]; ok {
			want[path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("expected recursive create to watch %q; watched paths: %v", path, paths)
		}
	}
}

func TestWatcherEvents(t *testing.T) {
	tmpDir := t.TempDir()

	var mu sync.Mutex
	var receivedEvents []Event
	eventReceived := make(chan struct{}, 10)

	w, err := New(
		func(events []Event) {
			mu.Lock()
			receivedEvents = append(receivedEvents, events...)
			mu.Unlock()
			select {
			case eventReceived <- struct{}{}:
			default:
			}
		},
		withDebouncer(NewDebouncer(50*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	// Create a file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Wait for event
	select {
	case <-eventReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(receivedEvents) == 0 {
		t.Error("expected at least one event")
		return
	}

	// Check that we got a create event
	found := false
	for _, e := range receivedEvents {
		if filepath.Base(e.Path) == "test.txt" && e.Type&Create != 0 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Create event for test.txt, got %+v", receivedEvents)
	}
}

func TestWatcherClose(t *testing.T) {
	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Close should succeed
	if err := w.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Close again should be no-op
	if err := w.Close(); err != nil {
		t.Fatalf("Close() again failed: %v", err)
	}

	// Operations after close should return ErrClosed
	tmpDir := t.TempDir()
	if err := w.Add(tmpDir); err != ErrClosed {
		t.Errorf("Add() after close = %v, want %v", err, ErrClosed)
	}
}

func TestWatcherClose_WaitsForDebouncedHandler(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "event.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once

	w, err := New(
		func(events []Event) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
		},
		withDebouncer(NewDebouncer(5*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		_ = w.Close()
	}()

	w.handleEvent(fsnotify.Event{Name: testFile, Op: fsnotify.Write})

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("debounced handler did not start")
	}

	closed := make(chan struct{})
	go func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close() failed: %v", err)
		}
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close() returned before debounced handler completed")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close() did not wait for debounced handler completion")
	}
}

func TestWatcherNonExistentPath(t *testing.T) {
	w, err := New(func(events []Event) {})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	// Try to add non-existent path
	err = w.Add("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("Add() for non-existent path should fail")
	}
}

func TestEventTypeFromFsnotify(t *testing.T) {
	tests := []struct {
		op       fsnotify.Op
		expected EventType
	}{
		{fsnotify.Create, Create},
		{fsnotify.Write, Write},
		{fsnotify.Remove, Remove},
		{fsnotify.Rename, Rename},
		{fsnotify.Chmod, Chmod},
		{fsnotify.Create | fsnotify.Write, Create | Write},
	}

	for _, tt := range tests {
		got := eventTypeFromFsnotify(tt.op)
		if got != tt.expected {
			t.Errorf("eventTypeFromFsnotify(%v) = %v, want %v", tt.op, got, tt.expected)
		}
	}
}

func TestWatcherIgnorePaths(t *testing.T) {
	tmpDir := t.TempDir()
	ignoredDir := filepath.Join(tmpDir, "ignored")
	if err := os.Mkdir(ignoredDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	var mu sync.Mutex
	var received []Event
	eventReceived := make(chan struct{}, 10)

	w, err := New(
		func(events []Event) {
			mu.Lock()
			received = append(received, events...)
			mu.Unlock()
			select {
			case eventReceived <- struct{}{}:
			default:
			}
		},
		WithRecursive(true),
		WithIgnorePaths([]string{"ignored"}),
		withDebouncer(NewDebouncer(50*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()

	if err := w.Add(tmpDir); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	// Create file in ignored dir
	ignoredFile := filepath.Join(ignoredDir, "should_be_ignored.txt")
	if err := os.WriteFile(ignoredFile, []byte("ignored"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Create file in root (should be detected)
	rootFile := filepath.Join(tmpDir, "root.txt")
	if err := os.WriteFile(rootFile, []byte("detected"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Wait for event
	select {
	case <-eventReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	mu.Lock()
	defer mu.Unlock()

	for _, e := range received {
		if e.Path == ignoredFile {
			t.Errorf("received event for ignored file: %s", e.Path)
		}
	}
}

// With WithMatch, a burst of matching writes reaches the handler once,
// after ntm's 250 ms debounce, and a file that does not match neither
// reaches it nor holds it back: writes to notes.txt go on for 2 s, and the
// handler runs long before they stop (nhm-98l).
func TestWatcherMatchHoldsOnlyMatchingPaths(t *testing.T) {
	root := t.TempDir()
	calls := make(chan []Event, 10)
	w, err := New(func(events []Event) { calls <- events },
		WithRecursive(true),
		WithMatch(func(p string) bool { return filepath.Ext(p) == ".go" }))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()
	if err := w.Add(root); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	write := func(name string, i int) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(fmt.Sprint(i)), 0o600); err != nil {
			t.Error(err)
		}
	}
	start := time.Now()
	for i := range 3 {
		write("a.go", i)
	}
	stop := make(chan struct{})
	noise := make(chan struct{})
	go func() {
		defer close(noise)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
				write("notes.txt", i)
			}
			if time.Since(start) > 2*time.Second {
				return
			}
		}
	}()

	var events []Event
	select {
	case events = <-calls:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the burst")
	}
	took := time.Since(start)
	close(stop)
	<-noise
	if took < DefaultDebounceDuration || took > 1500*time.Millisecond {
		t.Errorf("the handler ran %v after the first write, want the 250ms debounce, not held back by notes.txt", took)
	}
	for _, e := range events {
		if e.Path != filepath.Join(root, "a.go") {
			t.Errorf("handler got %+v, want only a.go", e)
		}
	}
	select {
	case more := <-calls:
		t.Errorf("a second call for one burst: %+v", more)
	case <-time.After(2 * DefaultDebounceDuration):
	}
}

// A folder made after Add gets its own watch, so a file written in it
// later is seen (ntm's recursive watch of new directories).
func TestWatcherWatchesNewDirectory(t *testing.T) {
	root := t.TempDir()
	calls := make(chan []Event, 10)
	w, err := New(func(events []Event) { calls <- events },
		WithRecursive(true),
		WithMatch(func(p string) bool { return filepath.Ext(p) == ".go" }),
		withDebouncer(NewDebouncer(20*time.Millisecond)))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()
	if err := w.Add(root); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}
	sub := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); !slices.Contains(w.WatchedPaths(), sub); {
		if time.Now().After(deadline) {
			t.Fatalf("no watch of %s: %v", sub, w.WatchedPaths())
		}
		time.Sleep(10 * time.Millisecond)
	}
	file := filepath.Join(sub, "x.go")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case events := <-calls:
		if events[0].Path != file {
			t.Errorf("events = %+v, want %s", events, file)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the file in the new folder")
	}
}

// When inotify has no watch left, Add stops at the first folder that
// fails and returns ErrNoWatches, so that the caller can poll instead; a
// folder made later that fails the same way reaches the error handler.
// Another error skips its folder, as ntm's (nhm-98l).
func TestWatcherOutOfWatches(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c", "denied"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var errs []error
	var errMu sync.Mutex
	w, err := New(func([]Event) {}, WithRecursive(true), WithErrorHandler(func(err error) {
		errMu.Lock()
		errs = append(errs, err)
		errMu.Unlock()
	}), withDebouncer(NewDebouncer(time.Hour)))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer w.Close()
	var tried []string
	w.mu.Lock()
	w.add = func(path string) error {
		tried = append(tried, path)
		switch {
		case filepath.Base(path) == "denied":
			return os.ErrPermission
		case len(tried) > 3:
			return ErrNoWatches
		}
		return nil
	}
	w.mu.Unlock()

	err = w.Add(root)
	if !errors.Is(err, ErrNoWatches) {
		t.Fatalf("Add() = %v, want ErrNoWatches", err)
	}
	// WalkDir goes in name order: root, a, b, then c fails and the walk
	// stops before denied.
	if len(tried) != 4 || len(w.WatchedPaths()) != 3 {
		t.Fatalf("tried %v, watched %v", tried, w.WatchedPaths())
	}

	w.handleEvent(fsnotify.Event{Name: filepath.Join(root, "denied"), Op: fsnotify.Create})
	w.handleEvent(fsnotify.Event{Name: filepath.Join(root, "c"), Op: fsnotify.Create})
	errMu.Lock()
	defer errMu.Unlock()
	if len(errs) != 2 || errors.Is(errs[0], ErrNoWatches) || !errors.Is(errs[0], os.ErrPermission) || !errors.Is(errs[1], ErrNoWatches) {
		t.Fatalf("errors = %v, want the denied folder's, then no watches", errs)
	}
}
