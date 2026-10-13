package runner

import (
	"context"
	"time"

	"github.com/arosl/nhm/internal/ui"
)

// WaitPoll is how often Wait reads a job's result.
const WaitPoll = 200 * time.Millisecond

// Client starts a surface's jobs and reads how they ended: the nhm
// binary and its environment, the instance's job directory, and the
// context the surface captured (its herdr socket and project directory).
type Client struct {
	Bin     string
	Env     []string
	JobDir  string
	Context ui.Context
	// Now is time.Now when nil.
	Now func() time.Time
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Start starts a job of the verb with the request and returns its id,
// which is the operation id of a verb that keeps a record. A job that did
// not start has its result already (Result).
func (c Client) Start(verb string, req any) (string, error) {
	j, err := NewJob(verb, c.Context, req, c.now())
	if err != nil {
		return "", err
	}
	return j.ID, Start(c.Bin, c.Env, c.JobDir, j)
}

// StartAs starts a job under id: the operation id a surface's guard gave
// its review. An id that a job has already is refused.
func (c Client) StartAs(verb, id string, req any) error {
	j, err := NewJob(verb, c.Context, req, c.now())
	if err != nil {
		return err
	}
	j.ID = id
	return Start(c.Bin, c.Env, c.JobDir, j)
}

// Run starts a job under id and waits for its result. ctx ends the wait,
// never the job: it runs on, and recent operations shows how it ended.
func (c Client) Run(ctx context.Context, verb, id string, req any) (Result, error) {
	if err := c.StartAs(verb, id, req); err != nil {
		return Result{}, err
	}
	return c.Wait(ctx, id)
}

// Wait waits for a job's result.
func (c Client) Wait(ctx context.Context, id string) (Result, error) {
	t := time.NewTicker(WaitPoll)
	defer t.Stop()
	for {
		r, ok, err := c.Result(id)
		if err != nil {
			return Result{}, err
		}
		if ok {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-t.C:
		}
	}
}

// Result reads a job's result; ok is false while it has none.
func (c Client) Result(id string) (Result, bool, error) { return Read(c.JobDir, id) }

// Recent lists the newest jobs, newest first.
func (c Client) Recent(limit int) ([]Entry, error) { return List(c.JobDir, limit) }
