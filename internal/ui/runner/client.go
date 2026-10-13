package runner

import (
	"time"

	"github.com/arosl/nhm/internal/ui"
)

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

// Start starts a job of the verb with the request and returns its id,
// which is the operation id of a verb that keeps a record. A job that did
// not start has its result already (Result).
func (c Client) Start(verb string, req any) (string, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	j, err := NewJob(verb, c.Context, req, now())
	if err != nil {
		return "", err
	}
	return j.ID, Start(c.Bin, c.Env, c.JobDir, j)
}

// Result reads a job's result; ok is false while it has none.
func (c Client) Result(id string) (Result, bool, error) { return Read(c.JobDir, id) }

// Recent lists the newest jobs, newest first.
func (c Client) Recent(limit int) ([]Entry, error) { return List(c.JobDir, limit) }
