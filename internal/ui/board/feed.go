// Package board is the board's data model (plan-v4 section 7, D5 "copy
// UI, own data model"): its sources, rows, selection, marks and results,
// in plain Go. The bubbletea program renders it and feeds it keys; it
// never blocks in its update loop, because every read runs here, in its
// own goroutine, and reports back through Changed.
//
// Sources run on nhm's snapshot service in process, not on the CLI:
// agent state every second, mail and beads every 15 seconds, each on its
// own schedule (Feed). The board only reads: it builds snapshots and
// peeks at inboxes, so its refreshes record nothing (plan-v4 section 4).
package board

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Feed reads one source on its own schedule. At most one read is in
// flight; each read has a deadline; a refresh asked for during a read
// waits behind it, and only one waits. A feed keeps the last value it
// read and when, and the last error and when: a failed or slow read never
// drops what an earlier read confirmed, and never shows as zero.
//
// A read that passes its deadline and does not return still counts as in
// flight: the feed records the timeout at once, starts no second read
// beside it, and drops its value when it returns late.
type Feed[T any] struct {
	name     string
	interval time.Duration
	deadline time.Duration
	read     func(context.Context) (T, error)
	now      func() time.Time
	changed  func()

	base   context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	st       State[T]
	lastDone time.Time
	closed   bool
}

// State is what a feed knows.
type State[T any] struct {
	Source string
	// Value is the last value read; Has says that there is one.
	Value T
	Has   bool
	// At is when the read that gave Value started.
	At time.Time
	// Err is the last read's error, nil when it worked; ErrAt is when
	// the feed recorded it.
	Err   error
	ErrAt time.Time
	// Reading: a read is in flight, since ReadingSince. Overdue: it passed
	// its deadline and has not returned.
	Reading      bool
	ReadingSince time.Time
	Overdue      bool
	// Pending: a refresh waits behind the read in flight.
	Pending bool
	// Paused: the feed reads only when asked (r), not on its schedule or
	// on events. Missed: an event came while it was paused.
	Paused bool
	Missed bool
	// Reads counts the reads started.
	Reads uint64
}

// Health is a State without its value.
type Health struct {
	Source       string    `json:"source"`
	Has          bool      `json:"has_value"`
	At           time.Time `json:"at"`
	Err          string    `json:"error,omitempty"`
	ErrAt        time.Time `json:"error_at"`
	Reading      bool      `json:"reading"`
	ReadingSince time.Time `json:"reading_since"`
	Overdue      bool      `json:"overdue"`
	Pending      bool      `json:"pending"`
	Paused       bool      `json:"paused"`
	Missed       bool      `json:"missed"`
	Reads        uint64    `json:"reads"`
}

// FeedConfig makes a feed.
type FeedConfig[T any] struct {
	Name     string
	Interval time.Duration
	Deadline time.Duration
	Read     func(context.Context) (T, error)
	// Now is time.Now in production.
	Now func() time.Time
	// Changed is called after every change of the feed's state, from the
	// feed's goroutines; it must not block.
	Changed func()
}

// NewFeed makes a feed; it reads nothing until Tick or Refresh.
func NewFeed[T any](c FeedConfig[T]) *Feed[T] {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	changed := c.Changed
	if changed == nil {
		changed = func() {}
	}
	base, cancel := context.WithCancel(context.Background())
	return &Feed[T]{name: c.Name, interval: c.Interval, deadline: c.Deadline, read: c.Read, now: now, changed: changed,
		base: base, cancel: cancel, st: State[T]{Source: c.Name}}
}

// Name is the source's name.
func (f *Feed[T]) Name() string { return f.name }

// State returns what the feed knows now.
func (f *Feed[T]) State() State[T] {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

// Health returns the feed's state without its value.
func (f *Feed[T]) Health() Health {
	s := f.State()
	h := Health{Source: s.Source, Has: s.Has, At: s.At, ErrAt: s.ErrAt, Reading: s.Reading, ReadingSince: s.ReadingSince,
		Overdue: s.Overdue, Pending: s.Pending, Paused: s.Paused, Missed: s.Missed, Reads: s.Reads}
	if s.Err != nil {
		h.Err = s.Err.Error()
	}
	return h
}

// Tick starts a read when one is due: the feed is not paused, no read is
// in flight, and the last read ended an interval ago (or there was none).
func (f *Feed[T]) Tick(now time.Time) {
	f.mu.Lock()
	start := !f.closed && !f.st.Paused && !f.st.Reading && (f.st.Reads == 0 || now.Sub(f.lastDone) >= f.interval)
	if start {
		f.startLocked()
	}
	f.mu.Unlock()
	if start {
		f.changed()
	}
}

// Refresh asks for a read now (r): it starts one, or, while one is in
// flight, leaves one refresh pending behind it. It reads even while the
// feed is paused.
func (f *Feed[T]) Refresh() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	if f.st.Reading {
		f.st.Pending = true
	} else {
		f.startLocked()
	}
	f.mu.Unlock()
	f.changed()
}

// Invalidate is an event: the source changed. It refreshes, unless the
// feed is paused; then it only notes that it missed one.
func (f *Feed[T]) Invalidate() {
	f.mu.Lock()
	paused := f.st.Paused
	if paused && !f.st.Missed {
		f.st.Missed = true
		f.mu.Unlock()
		f.changed()
		return
	}
	f.mu.Unlock()
	if !paused {
		f.Refresh()
	}
}

// SetPaused stops (p) or resumes the feed's own schedule and its events.
func (f *Feed[T]) SetPaused(p bool) {
	f.mu.Lock()
	f.st.Paused = p
	if !p {
		f.st.Missed = false
	}
	f.mu.Unlock()
	f.changed()
}

// Close stops the feed: it cancels a read in flight and starts no other.
func (f *Feed[T]) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.cancel()
}

// startLocked starts a read; f.mu is held.
func (f *Feed[T]) startLocked() {
	started := f.now()
	f.st.Reading, f.st.ReadingSince, f.st.Overdue, f.st.Pending = true, started, false, false
	f.st.Reads++
	go f.run(started)
}

type outcome[T any] struct {
	v   T
	err error
}

func (f *Feed[T]) run(started time.Time) {
	ctx, cancel := context.WithTimeout(f.base, f.deadline)
	defer cancel()
	done := make(chan outcome[T], 1)
	go func() {
		v, err := f.read(ctx)
		done <- outcome[T]{v, err}
	}()
	select {
	case o := <-done:
		f.finish(started, o, false)
	case <-ctx.Done():
		f.overdue()
		f.finish(started, <-done, true)
	}
}

// overdue records that the read in flight passed its deadline.
func (f *Feed[T]) overdue() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.st.Overdue = true
	f.st.Err = fmt.Errorf("%s: no answer within %s", f.name, f.deadline)
	f.st.ErrAt = f.now()
	f.mu.Unlock()
	f.changed()
}

// finish ends a read. A late read's value is dropped: its timeout is
// already the feed's last word.
func (f *Feed[T]) finish(started time.Time, o outcome[T], late bool) {
	f.mu.Lock()
	if f.closed {
		f.st.Reading = false
		f.mu.Unlock()
		return
	}
	f.st.Reading, f.st.Overdue = false, false
	f.lastDone = f.now()
	switch {
	case late:
	case o.err != nil:
		f.st.Err, f.st.ErrAt = fmt.Errorf("%s: %w", f.name, o.err), f.lastDone
	default:
		f.st.Value, f.st.Has, f.st.At, f.st.Err = o.v, true, started, nil
	}
	if f.st.Pending {
		f.startLocked()
	}
	f.mu.Unlock()
	f.changed()
}
