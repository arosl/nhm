package board

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)

// gate is a read that blocks until the test lets it go, and counts the
// reads in flight.
type gate struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    int
	release  chan result
	started  chan struct{}
}

type result struct {
	v   int
	err error
}

func newGate() *gate {
	return &gate{release: make(chan result), started: make(chan struct{}, 16)}
}

// read honours ctx unless hang is set.
func (g *gate) read(hang bool) func(context.Context) (int, error) {
	return func(ctx context.Context) (int, error) {
		g.mu.Lock()
		g.inFlight++
		g.calls++
		g.maxSeen = max(g.maxSeen, g.inFlight)
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			g.inFlight--
			g.mu.Unlock()
		}()
		g.started <- struct{}{}
		if hang {
			r := <-g.release
			return r.v, r.err
		}
		select {
		case r := <-g.release:
			return r.v, r.err
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

func (g *gate) stats() (calls, maxSeen int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls, g.maxSeen
}

// waitFor polls cond (the feed works on its own goroutines).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newTestFeed(g *gate, hang bool, deadline time.Duration) *Feed[int] {
	return NewFeed(FeedConfig[int]{Name: "test", Interval: time.Second, Deadline: deadline, Read: g.read(hang)})
}

// Refreshes during a read leave one refresh pending: five asks give two
// reads, never two at once.
func TestFeedOneInFlightOnePending(t *testing.T) {
	g := newGate()
	f := newTestFeed(g, false, time.Minute)
	defer f.Close()
	f.Refresh()
	<-g.started
	for range 5 {
		f.Refresh()
	}
	if s := f.State(); !s.Reading || !s.Pending {
		t.Fatalf("state = %+v", s)
	}
	g.release <- result{v: 1}
	<-g.started // the pending read
	if s := f.State(); !s.Has || s.Value != 1 || s.Pending {
		t.Fatalf("after the first read: %+v", s)
	}
	g.release <- result{v: 2}
	waitFor(t, "the second value", func() bool { return f.State().Value == 2 && !f.State().Reading })
	if calls, maxSeen := g.stats(); calls != 2 || maxSeen != 1 {
		t.Fatalf("calls %d, at most %d at once", calls, maxSeen)
	}
}

// A failed read keeps the last value and its time; the next good read
// clears the error.
func TestFeedErrorKeepsLastValue(t *testing.T) {
	g := newGate()
	f := newTestFeed(g, false, time.Minute)
	defer f.Close()
	f.Refresh()
	<-g.started
	g.release <- result{v: 7}
	waitFor(t, "a value", func() bool { return f.State().Has && !f.State().Reading })
	at := f.State().At
	f.Refresh()
	<-g.started
	g.release <- result{err: errors.New("herdr: connection refused")}
	waitFor(t, "the error", func() bool { return f.State().Err != nil })
	s := f.State()
	if s.Value != 7 || !s.Has || !s.At.Equal(at) || !strings.Contains(s.Err.Error(), "connection refused") {
		t.Fatalf("state after a failure = %+v", s)
	}
	f.Refresh()
	<-g.started
	g.release <- result{v: 8}
	waitFor(t, "recovery", func() bool { return f.State().Value == 8 })
	if s := f.State(); s.Err != nil {
		t.Fatalf("error after a good read: %v", s.Err)
	}
}

// A read that ignores its deadline: the timeout shows at once, the last
// value stays, no second read starts beside it, and its late value is
// dropped. The pending refresh runs when it ends.
func TestFeedHungReadTimesOut(t *testing.T) {
	g := newGate()
	f := newTestFeed(g, true, 30*time.Millisecond)
	defer f.Close()
	f.Refresh()
	<-g.started
	g.release <- result{v: 1}
	waitFor(t, "a value", func() bool { return f.State().Has && !f.State().Reading })

	f.Refresh()
	<-g.started
	waitFor(t, "the timeout", func() bool { return f.State().Overdue })
	s := f.State()
	if s.Value != 1 || s.Err == nil || !strings.Contains(s.Err.Error(), "no answer within") || !s.Reading {
		t.Fatalf("overdue state = %+v", s)
	}
	f.Refresh()
	f.Tick(time.Now().Add(time.Hour))
	if calls, _ := g.stats(); calls != 2 {
		t.Fatalf("%d reads while one hangs", calls)
	}
	g.release <- result{v: 99} // late
	<-g.started                // the pending refresh
	if s := f.State(); s.Value == 99 {
		t.Fatalf("the late value was taken: %+v", s)
	}
	g.release <- result{v: 3}
	waitFor(t, "the next value", func() bool { return f.State().Value == 3 })
	if _, maxSeen := g.stats(); maxSeen != 1 {
		t.Fatalf("%d reads at once", maxSeen)
	}
}

// Tick reads on the schedule; paused, it reads only on Refresh, and an
// event is noted, not read.
func TestFeedScheduleAndPause(t *testing.T) {
	var calls atomic.Int64
	f := NewFeed(FeedConfig[int]{Name: "test", Interval: time.Second, Deadline: time.Second,
		Read: func(context.Context) (int, error) { return int(calls.Add(1)), nil }})
	defer f.Close()
	idle := func() bool { return !f.State().Reading }
	f.Tick(t0)
	waitFor(t, "the first read", func() bool { return calls.Load() == 1 && idle() })
	f.Tick(time.Now()) // the last read just ended: not due
	if calls.Load() != 1 {
		t.Fatalf("read before the interval: %d", calls.Load())
	}
	f.Tick(time.Now().Add(2 * time.Second))
	waitFor(t, "the scheduled read", func() bool { return calls.Load() == 2 && idle() })

	f.SetPaused(true)
	f.Tick(time.Now().Add(time.Hour))
	f.Invalidate()
	if calls.Load() != 2 || !f.State().Missed {
		t.Fatalf("paused: %d reads, state %+v", calls.Load(), f.State())
	}
	f.Refresh()
	waitFor(t, "the asked read", func() bool { return calls.Load() == 3 && idle() })
	f.SetPaused(false)
	if f.State().Missed {
		t.Fatal("Missed stays after resume")
	}
	f.Invalidate()
	waitFor(t, "the event's read", func() bool { return calls.Load() == 4 && idle() })
}

// Close stops a read in flight and starts no other.
func TestFeedClose(t *testing.T) {
	g := newGate()
	f := newTestFeed(g, false, time.Minute)
	f.Refresh()
	<-g.started
	f.Close()
	waitFor(t, "the read to end", func() bool { return !f.State().Reading })
	f.Refresh()
	f.Tick(time.Now().Add(time.Hour))
	if calls, _ := g.stats(); calls != 1 {
		t.Fatalf("%d reads after Close", calls)
	}
}
