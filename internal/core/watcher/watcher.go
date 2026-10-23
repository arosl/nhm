// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/watcher/watcher.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: only what ntm watch
// --pattern uses, on fsnotify. ntm's polling mode (runPoll, pollOnce and
// their snapshots) and WithPollInterval are left out: New returns
// fsnotify's error, and nhm watch falls back to its own paced polling
// scan. A recursive add stops at the first folder that inotify has no
// watch left for and returns ErrNoWatches (ntm reports each folder and
// goes on), so the caller can fall back. WithErrorHandler (a test helper
// in ntm) is an option, and WithMatch is new: an event whose path does
// not match does not reach the handler and does not hold back the
// debounce. WithEventFilter, WithDebounceDuration and WithDebouncer are
// left out (the filter is always All), and the comment on the watch of
// new folders names the match. The add of a folder goes through the add
// field, so that tests can run out of watches.

// Package watcher provides file watching with debouncing using fsnotify.
package watcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/fsnotify/fsnotify"
)

// ErrClosed is returned when operations are called on a closed Watcher.
var ErrClosed = errors.New("watcher: watcher is closed")

// ErrNoWatches is the error of an add when inotify has no watch left
// (fs.inotify.max_user_watches): errors.Is matches it. The kernel's own
// text for it is "no space left on device".
var ErrNoWatches = syscall.ENOSPC

// EventType represents the type of file system event.
type EventType uint32

const (
	// Create is triggered when a file or directory is created.
	Create EventType = 1 << iota
	// Write is triggered when a file is modified.
	Write
	// Remove is triggered when a file or directory is removed.
	Remove
	// Rename is triggered when a file or directory is renamed.
	Rename
	// Chmod is triggered when file permissions change.
	Chmod
	// All events.
	All = Create | Write | Remove | Rename | Chmod
)

// Event represents a file system event.
type Event struct {
	// Path is the absolute path to the file or directory.
	Path string
	// Type is the type of event.
	Type EventType
	// IsDir is true if the event is for a directory.
	IsDir bool
}

// eventTypeFromFsnotify converts fsnotify.Op to EventType.
func eventTypeFromFsnotify(op fsnotify.Op) EventType {
	var t EventType
	if op.Has(fsnotify.Create) {
		t |= Create
	}
	if op.Has(fsnotify.Write) {
		t |= Write
	}
	if op.Has(fsnotify.Remove) {
		t |= Remove
	}
	if op.Has(fsnotify.Rename) {
		t |= Rename
	}
	if op.Has(fsnotify.Chmod) {
		t |= Chmod
	}
	return t
}

// Handler is called when a file system event occurs.
// Multiple events may be coalesced into a single call due to debouncing.
type Handler func(events []Event)

// ErrorHandler is called when a watch error occurs.
type ErrorHandler func(err error)

// Watcher watches files and directories for changes.
type Watcher struct {
	fsWatcher    *fsnotify.Watcher
	debouncer    *Debouncer
	handler      Handler
	errorHandler ErrorHandler
	match        func(path string) bool
	recursive    bool
	// add adds one inotify watch: fsWatcher.Add, or a test's.
	add func(path string) error

	wg                sync.WaitGroup
	deliveryMu        sync.Mutex
	deliveryCond      *sync.Cond
	activeDeliveries  int
	closingDeliveries bool

	mu            sync.Mutex
	watchedPaths  map[string]bool
	ignorePaths   []string
	pendingEvents []Event
	closed        bool
}

// New creates a new Watcher.
// Use WithRecursive to watch directories recursively.
// It fails when fsnotify cannot start; the caller then polls.
func New(handler Handler, opts ...Option) (*Watcher, error) {
	w := &Watcher{
		debouncer:    NewDebouncer(DefaultDebounceDuration),
		handler:      handler,
		watchedPaths: make(map[string]bool),
	}
	w.deliveryCond = sync.NewCond(&w.deliveryMu)

	for _, opt := range opts {
		opt(w)
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w.fsWatcher = fsWatcher
	w.add = fsWatcher.Add

	w.wg.Add(1)
	go w.run()

	return w, nil
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithRecursive enables recursive watching of directories.
func WithRecursive(recursive bool) Option {
	return func(w *Watcher) {
		w.recursive = recursive
	}
}

// WithIgnorePaths sets patterns to ignore (matched against file/dir name).
func WithIgnorePaths(patterns []string) Option {
	return func(w *Watcher) {
		w.ignorePaths = patterns
	}
}

// WithErrorHandler sets the error handler.
func WithErrorHandler(handler ErrorHandler) Option {
	return func(w *Watcher) {
		w.errorHandler = handler
	}
}

// WithMatch keeps only the events whose path matches: the others do not
// reach the handler and do not hold back the debounce. The watch of new
// directories goes on for every path.
func WithMatch(match func(path string) bool) Option {
	return func(w *Watcher) {
		w.match = match
	}
}

// Add adds a path to the watcher.
// If the path is a directory and recursive is enabled, all subdirectories are also watched.
func (w *Watcher) Add(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrClosed
	}

	if w.watchedPaths[absPath] {
		w.mu.Unlock()
		return nil // Already watching
	}

	if info.IsDir() && w.recursive {
		w.mu.Unlock() // Release lock for recursive add
		return w.addRecursive(absPath)
	}

	// Non-recursive or file
	if err := w.add(absPath); err != nil {
		w.mu.Unlock()
		return err
	}
	w.watchedPaths[absPath] = true
	w.mu.Unlock()

	return nil
}

// isIgnored checks if the path should be ignored.
func (w *Watcher) isIgnored(path string) bool {
	name := filepath.Base(path)
	for _, pattern := range w.ignorePaths {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// addRecursive adds a directory and all its subdirectories to the watcher.
// Handles locking internally per directory to allow event interleaving.
// It stops at the first directory that inotify has no watch left for.
func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip directories we can't access, but continue walking
			if w.errorHandler != nil {
				w.errorHandler(fmt.Errorf("walking %s: %w", path, err))
			}
			return filepath.SkipDir
		}
		if d.IsDir() {
			// Check ignores (safe without lock as ignorePaths is immutable)
			if w.isIgnored(path) {
				return filepath.SkipDir
			}

			w.mu.Lock()
			if w.closed {
				w.mu.Unlock()
				return filepath.SkipDir // Stop walking
			}

			if w.watchedPaths[path] {
				w.mu.Unlock()
				return nil
			}

			if err := w.add(path); err != nil {
				w.mu.Unlock()
				if errors.Is(err, ErrNoWatches) {
					// Every later folder would fail the same way.
					return fmt.Errorf("watching %s: %w", path, err)
				}
				// Report error but continue
				if w.errorHandler != nil {
					w.errorHandler(fmt.Errorf("watching %s: %w", path, err))
				}
				return nil
			}
			w.watchedPaths[path] = true
			w.mu.Unlock()
		}
		return nil
	})
}

// Close stops the watcher and releases resources.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.closeDeliveries()
		w.wg.Wait()
		return nil
	}

	w.closed = true
	fsWatcher := w.fsWatcher
	w.mu.Unlock()

	w.closeDeliveries()
	w.debouncer.Cancel()

	var err error
	if fsWatcher != nil {
		err = fsWatcher.Close()
	}

	w.wg.Wait()
	return err
}

// run processes events from fsnotify.
func (w *Watcher) run() {
	defer w.wg.Done()

	for {
		select {
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			if w.errorHandler != nil {
				w.errorHandler(err)
			}
		}
	}
}

// handleEvent processes a single fsnotify event.
func (w *Watcher) handleEvent(fsEvent fsnotify.Event) {
	eventType := eventTypeFromFsnotify(fsEvent.Op)

	// Check if it's a directory
	isDir := false
	if info, err := os.Stat(fsEvent.Name); err == nil {
		isDir = info.IsDir()
	}

	event := Event{
		Path:  fsEvent.Name,
		Type:  eventType,
		IsDir: isDir,
	}

	// Keep recursive watch topology current independent of delivery filtering.
	// A watcher interested only in some paths still needs Create events for
	// new subdirectories, otherwise later writes inside them are invisible.
	if w.recursive && isDir && eventType&Create != 0 {
		// Check ignores
		if w.isIgnored(fsEvent.Name) {
			return
		}

		if err := w.addRecursive(fsEvent.Name); err != nil && w.errorHandler != nil {
			w.errorHandler(fmt.Errorf("watching new directory %s: %w", fsEvent.Name, err))
		}
	}

	// If a watched directory was removed, clean up
	if eventType&Remove != 0 || eventType&Rename != 0 {
		w.mu.Lock()
		w.removeWatchedPathLocked(fsEvent.Name)
		w.mu.Unlock()
	}

	// Filter only user-visible delivery after internal watch maintenance.
	if eventType&All == 0 {
		return
	}
	if w.match != nil && !w.match(event.Path) {
		return
	}

	w.mu.Lock()
	w.pendingEvents = append(w.pendingEvents, event)
	w.mu.Unlock()

	w.triggerPendingEvents()
}

// removeWatchedPathLocked removes path and, in recursive mode, any watched
// descendants. It must be called with w.mu held.
func (w *Watcher) removeWatchedPathLocked(path string) {
	if !w.recursive {
		delete(w.watchedPaths, path)
		return
	}

	sep := string(os.PathSeparator)
	for watched := range w.watchedPaths {
		if watched == path || strings.HasPrefix(watched, path+sep) {
			delete(w.watchedPaths, watched)
		}
	}
}

func (w *Watcher) triggerPendingEvents() {
	w.debouncer.Trigger(func() {
		if !w.beginDelivery() {
			return
		}
		defer w.endDelivery()

		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		toDeliver := w.pendingEvents
		w.pendingEvents = nil
		w.mu.Unlock()

		if len(toDeliver) > 0 && w.handler != nil {
			w.handler(toDeliver)
		}
	})
}

func (w *Watcher) beginDelivery() bool {
	w.deliveryMu.Lock()
	defer w.deliveryMu.Unlock()
	if w.closingDeliveries {
		return false
	}
	w.activeDeliveries++
	return true
}

func (w *Watcher) endDelivery() {
	w.deliveryMu.Lock()
	w.activeDeliveries--
	if w.activeDeliveries == 0 {
		w.deliveryCond.Broadcast()
	}
	w.deliveryMu.Unlock()
}

func (w *Watcher) closeDeliveries() {
	w.deliveryMu.Lock()
	w.closingDeliveries = true
	for w.activeDeliveries > 0 {
		w.deliveryCond.Wait()
	}
	w.deliveryMu.Unlock()
}
