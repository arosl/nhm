package board

import (
	"errors"
	"time"
)

// Result outcomes.
const (
	// OutcomePending: the core has the request and has not answered.
	OutcomePending = "pending"
	// OutcomeDone: the core answered; the targets carry its words.
	OutcomeDone = "done"
	// OutcomeRefused: the core refused before any effect.
	OutcomeRefused = "refused"
	// OutcomeFailed: the core answered with an error.
	OutcomeFailed = "failed"
	// OutcomeUnknown: no answer came. The core's operation record says
	// what happened (recent operations); the board never resubmits.
	OutcomeUnknown = "unknown"
)

// maxResults bounds the board's own list; the core keeps every record.
const maxResults = 100

// ErrSubmitted means a review was submitted already: a second enter, or
// another key on the same review, does not submit it again.
var ErrSubmitted = errors.New("board: this review was submitted already; open a new review to act again")

// TargetResult is one target's outcome, in the core's words.
type TargetResult struct {
	Key    Key    `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Result is one submission.
type Result struct {
	Review      string         `json:"review"`
	OperationID string         `json:"operation_id"`
	Verb        string         `json:"verb"`
	Targets     []TargetResult `json:"targets"`
	Outcome     string         `json:"outcome"`
	Detail      string         `json:"detail,omitempty"`
	Started     time.Time      `json:"started"`
	Ended       time.Time      `json:"ended"`
}

// Results are the board's submissions, newest first. Each review is
// submitted once, whatever key submits it (the submission guard).
type Results struct {
	items []Result
	used  map[string]bool
}

// Begin records a submission before it goes to the core. It refuses a
// review that was submitted already.
func (r *Results) Begin(review, operationID, verb string, targets []Row, now time.Time) error {
	if r.used == nil {
		r.used = map[string]bool{}
	}
	if r.used[review] {
		return ErrSubmitted
	}
	r.used[review] = true
	res := Result{Review: review, OperationID: operationID, Verb: verb, Outcome: OutcomePending, Started: now}
	for _, t := range targets {
		res.Targets = append(res.Targets, TargetResult{Key: t.Key, Name: t.Name, Status: OutcomePending})
	}
	r.items = append([]Result{res}, r.items...)
	if len(r.items) > maxResults {
		r.items = r.items[:maxResults]
	}
	return nil
}

// End records the core's answer. per replaces the targets' outcomes when
// given. It reports false for an operation the board does not know (an
// answer to an old or dropped request): it changes nothing then.
func (r *Results) End(operationID, outcome, detail string, per []TargetResult, now time.Time) bool {
	for i := range r.items {
		it := &r.items[i]
		if it.OperationID != operationID {
			continue
		}
		if it.Outcome != OutcomePending {
			return false
		}
		it.Outcome, it.Detail, it.Ended = outcome, detail, now
		if per != nil {
			it.Targets = per
		} else {
			for j := range it.Targets {
				it.Targets[j].Status = outcome
			}
		}
		return true
	}
	return false
}

// List returns the submissions, newest first.
func (r *Results) List() []Result { return append([]Result(nil), r.items...) }

// Get returns one submission.
func (r *Results) Get(operationID string) (Result, bool) {
	for _, it := range r.items {
		if it.OperationID == operationID {
			return it, true
		}
	}
	return Result{}, false
}

// InFlight counts the submissions without an answer.
func (r *Results) InFlight() int {
	n := 0
	for _, it := range r.items {
		if it.Outcome == OutcomePending {
			n++
		}
	}
	return n
}
