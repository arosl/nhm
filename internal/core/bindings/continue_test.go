package bindings

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestContinueKeepsTheGeneration(t *testing.T) {
	_, s := openStore(t)
	ctx := context.Background()
	n := newBinding("IcyKnoll", "/p", "w1", "w1:t1", "w1:p1")
	n.NativeSession = &NativeSession{Source: "herdr:claude", Value: "sess-a"}
	n.ForegroundPID, n.ForegroundStart = 500, 5000
	b, err := s.Create(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	got, err := s.Continue(ctx, b.ID, 1, Continuation{CompactedAt: &at, Evidence: []Evidence{{Kind: "sessionstart", Detail: "compaction"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 1 || got.CompactedAt == nil || !got.CompactedAt.Equal(at) || got.ResumedAt != nil ||
		got.NativeSession.Value != "sess-a" || got.ForegroundPID != 500 || len(got.Evidence) != 1 || got.Slot != "0.1" {
		t.Fatalf("after compaction = %+v", got)
	}
	// A restart in place: new process and session; the compaction time
	// stays.
	got, err = s.Continue(ctx, b.ID, 1, Continuation{ResumedAt: &at, NativeSession: &NativeSession{Value: "sess-b"},
		ForegroundPID: 700, ForegroundStart: 7000})
	if err != nil {
		t.Fatal(err)
	}
	if got.CompactedAt == nil || got.ResumedAt == nil || got.NativeSession.Value != "sess-b" || got.ForegroundPID != 700 || got.ForegroundStart != 7000 {
		t.Fatalf("after restart = %+v", got)
	}
	for i := 0; i < 2*maxEvidence; i++ {
		if got, err = s.Continue(ctx, b.ID, 1, Continuation{Evidence: []Evidence{{Kind: "sessionstart", Detail: "compaction"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got.Evidence) != maxEvidence {
		t.Fatalf("evidence grew to %d", len(got.Evidence))
	}
	if _, err := s.Continue(ctx, b.ID, 2, Continuation{}); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong generation: %v", err)
	}
	if _, err := s.Displace(ctx, b.ID, 1, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Continue(ctx, b.ID, 1, Continuation{}); !errors.Is(err, ErrStale) {
		t.Fatalf("displaced binding continued: %v", err)
	}
}
