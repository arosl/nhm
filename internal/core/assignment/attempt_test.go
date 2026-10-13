package assignment

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// attemptRecorder records the Attempt of each dispatch and answers with the
// next scripted outcome.
type attemptRecorder struct {
	mu       sync.Mutex
	attempts []int
	outcomes []error
}

func (r *attemptRecorder) Dispatch(_ context.Context, req DispatchRequest) (DispatchReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, req.Attempt)
	var err error
	if len(r.outcomes) > 0 {
		err, r.outcomes = r.outcomes[0], r.outcomes[1:]
	}
	if err != nil {
		return DispatchReceipt{Duration: time.Millisecond}, err
	}
	return DispatchReceipt{DeliveryID: "delivery", Duration: time.Millisecond}, nil
}

// A known-unsent failure leaves the row pending; the retry of the same key
// is attempt 2, so an adapter that derives its transport operation from
// the key and the attempt sends under a new operation, never a replay of
// the refused one (nhm-i85, port-boundary-v2 attempt contract).
func TestAtomicDispatchAttemptAdvancesOnlyAfterGuaranteedNoActuation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dispatcher := &attemptRecorder{outcomes: []error{GuaranteeNoActuation(errors.New("refused before typing")), nil}}
	store := NewStore("attempt-known-unsent")
	coordinator := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher)
	req := atomicTestRequest("attempt-key", "bnd-0000000000000042")

	if _, err := coordinator.Execute(t.Context(), req); err == nil || errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("first Execute error=%v, want a known-unsent failure", err)
	}
	if got := store.Get(req.BeadID); got == nil || got.DispatchState != DispatchPending || got.DispatchAttempts != 1 {
		t.Fatalf("after the refusal: %+v, want pending at attempt 1", got)
	}
	result, err := coordinator.Execute(t.Context(), req)
	if err != nil || !result.Sent {
		t.Fatalf("retry result=%+v error=%v, want sent", result, err)
	}
	if want := []int{1, 2}; !slices.Equal(dispatcher.attempts, want) {
		t.Fatalf("dispatch attempts=%v, want %v", dispatcher.attempts, want)
	}
	// A sent row replays; it never dispatches a third time.
	replay, err := coordinator.Execute(t.Context(), req)
	if err != nil || !replay.Replayed || len(dispatcher.attempts) != 2 {
		t.Fatalf("replay=%+v error=%v attempts=%v", replay, err, dispatcher.attempts)
	}
}

// An unknown outcome keeps the row sending at its attempt; no retry of the
// key reaches the transport again.
func TestAtomicDispatchAttemptUnknownOutcomeAllowsNoNewAttempt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dispatcher := &attemptRecorder{outcomes: []error{errors.New("reply lost after the prompt")}}
	store := NewStore("attempt-unknown")
	coordinator := NewAtomicCoordinator(store, &atomicClaimLedger{}, nil, dispatcher)
	req := atomicTestRequest("attempt-unknown-key", "bnd-0000000000000043")

	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("first Execute error=%v, want ErrDispatchOutcomeUnknown", err)
	}
	if _, err := coordinator.Execute(t.Context(), req); !errors.Is(err, ErrDispatchOutcomeUnknown) {
		t.Fatalf("retry error=%v, want ErrDispatchOutcomeUnknown", err)
	}
	if got := store.Get(req.BeadID); got == nil || got.DispatchState != DispatchSending || got.DispatchAttempts != 1 {
		t.Fatalf("stored=%+v, want sending at attempt 1", got)
	}
	if want := []int{1}; !slices.Equal(dispatcher.attempts, want) {
		t.Fatalf("dispatch attempts=%v, want %v", dispatcher.attempts, want)
	}
}
