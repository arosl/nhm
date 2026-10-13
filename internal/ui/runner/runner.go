// Package runner runs the UI's mutations in processes of their own, so
// that closing a popup is not cancellation (plan-v4 section 4).
//
// A surface (menu, board, palette) writes a Job: the verb, the exact
// request it reviewed and the operation id it chose. Start starts `nhm ui
// run <job file>` in a new session (setsid), in the captured project
// directory. That process calls the same core function as the CLI verb,
// under nhm's owner lock where the verb takes it, and writes the Result
// beside the job. The surface reads the result, and the core's operation
// record where the verb keeps one; recent operations reads both after the
// surface closed. A job runs once: a second run of its file is refused,
// never replayed.
//
// The files live in <instance dir>/ui/jobs: <id>.job.json,
// <id>.run (the claim), <id>.result.json and <id>.log.
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

// Version is the job format this nhm writes and runs.
const Version = 1

// Verbs. The requests of send, retire, recycle and adopt are the core's
// own (send.Request, lifecycle.RetireRequest, lifecycle.RecycleRequest,
// bindings.AdoptRequest), as the action table's handler built them; the
// others have a request type here. A job's id is its operation id.
const (
	VerbKill      = "kill"
	VerbReap      = "reap"
	VerbSpawn     = "spawn"
	VerbNew       = "new"
	VerbWake      = "wake"
	VerbSend      = "send"
	VerbInterrupt = "interrupt"
	VerbRetire    = "retire"
	VerbRecycle   = "recycle"
	VerbAdopt     = "adopt"
)

// Codes are the CLI verbs' exit codes.
const (
	CodeOK      = 0
	CodeError   = 1
	CodeRefused = 2
)

// Job is one mutation, as the surface reviewed it.
type Job struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Verb    string `json:"verb"`
	// Socket is the herdr server's socket; the job opens the nhm instance
	// enrolled for it.
	Socket string `json:"socket"`
	// Dir is the captured project directory: the job's cwd.
	Dir       string          `json:"dir"`
	Request   json.RawMessage `json:"request"`
	CreatedAt time.Time       `json:"created_at"`
}

// KillRequest closes a workspace (nhm kill --yes). The job's id is the
// kill's operation id.
type KillRequest struct {
	WorkspaceID string `json:"workspace_id"`
	NoBeads     bool   `json:"no_beads,omitempty"`
}

// ReapRequest retires exactly the set a dry run showed
// (lifecycle.ReapRequest.Only). The runner adds the keep list and the
// mail check from nhm's config, as nhm reap does.
type ReapRequest struct {
	WorkspaceID string                 `json:"workspace_id"`
	Idle        time.Duration          `json:"idle_ns"`
	Only        []lifecycle.ReapTarget `json:"only"`
}

// SpawnRequest is nhm spawn, or nhm add, as the wizard's dry run showed
// it. The job's id is the spawn's operation id; the project is the job's
// Dir.
type SpawnRequest struct {
	Session   string       `json:"session"`
	Add       bool         `json:"add,omitempty"`
	Agents    []spawn.Want `json:"agents"`
	Label     string       `json:"label,omitempty"`
	Group     string       `json:"group,omitempty"`
	Prompt    string       `json:"prompt,omitempty"`
	NoKickoff bool         `json:"no_kickoff,omitempty"`
	// Memory puts CM rules in the kickoff; CASS adds CASS context.
	Memory bool `json:"memory,omitempty"`
	CASS   bool `json:"cass,omitempty"`
}

// NewRequest is nhm new: a workspace with the label and the project as
// its cwd.
type NewRequest struct {
	Label   string `json:"label"`
	Project string `json:"project"`
}

// WakeRequest is one manual wake pass for the picked agents.
type WakeRequest struct {
	Recipients []ui.Target `json:"recipients"`
}

// InterruptRequest interrupts the reviewed bindings: binding id to the
// generation the surface saw (wait.InterruptRequest's Expect).
type InterruptRequest struct {
	Expect map[string]int64 `json:"expect"`
}

// Result is what a job's process wrote when it ended.
type Result struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Verb    string `json:"verb"`
	// Code is the CLI verb's exit code: 0 done, 1 an error, 2 refused.
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
	// Result is the core's result, as the verb's --json prints it.
	Result    json.RawMessage `json:"result,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at"`
}

// idPattern is the operation id pattern: an id is a file name here.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ErrRan refuses a job that was run before.
var ErrRan = errors.New("runner: the job ran before; it is not run again")

// NewJob builds a job of a verb in the captured context, with a new
// operation id.
func NewJob(verb string, c ui.Context, req any, now time.Time) (Job, error) {
	id, err := store.NewOperationID(now)
	if err != nil {
		return Job{}, err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return Job{}, fmt.Errorf("runner: %s request: %w", verb, err)
	}
	return Job{Version: Version, ID: id, Verb: verb, Socket: c.SocketPath, Dir: c.Cwd, Request: data, CreatedAt: now.UTC()}, nil
}

// Check reports what makes a job unusable.
func (j Job) Check() error {
	switch {
	case j.Version != Version:
		return fmt.Errorf("runner: job version %d; this nhm runs version %d", j.Version, Version)
	case !idPattern.MatchString(j.ID):
		return fmt.Errorf("runner: invalid job id %q", j.ID)
	case j.Verb == "":
		return errors.New("runner: a job without a verb")
	case j.Socket == "":
		return errors.New("runner: a job without a herdr socket")
	case j.Dir == "":
		return errors.New("runner: the job has no project directory: open the surface from a pane in the project")
	case !filepath.IsAbs(j.Dir):
		return fmt.Errorf("runner: the job's directory %q is not absolute", j.Dir)
	case !json.Valid(j.Request):
		return errors.New("runner: the job's request is not JSON")
	}
	return nil
}

// Decode reads the job's request.
func (j Job) Decode(v any) error {
	dec := json.NewDecoder(strings.NewReader(string(j.Request)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("runner: %s request: %w", j.Verb, err)
	}
	return nil
}

// JobDir is where an instance keeps its jobs.
func JobDir(instanceDir string) string { return filepath.Join(instanceDir, "ui", "jobs") }

// JobPath, ResultPath, LogPath and claimPath are a job's files.
func JobPath(dir, id string) string    { return filepath.Join(dir, id+".job.json") }
func ResultPath(dir, id string) string { return filepath.Join(dir, id+".result.json") }
func LogPath(dir, id string) string    { return filepath.Join(dir, id+".log") }
func claimPath(dir, id string) string  { return filepath.Join(dir, id+".run") }

// write writes the job file; a job with the same id is refused.
func write(dir string, j Job) error {
	if err := j.Check(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(JobPath(dir, j.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("runner: job %s: %w", j.ID, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("runner: job %s: %w", j.ID, err)
	}
	return f.Close()
}

// Load reads a job file (nhm ui run).
func Load(path string) (Job, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Job{}, fmt.Errorf("runner: %w", err)
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return Job{}, fmt.Errorf("runner: %s: %w", path, err)
	}
	if err := j.Check(); err != nil {
		return Job{}, err
	}
	if filepath.Base(path) != j.ID+".job.json" {
		return Job{}, fmt.Errorf("runner: %s does not hold job %s", path, j.ID)
	}
	return j, nil
}

// Claim marks the job as run, once: a job claimed before gets ErrRan.
func Claim(dir, id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("runner: invalid job id %q", id)
	}
	f, err := os.OpenFile(claimPath(dir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return ErrRan
	}
	if err != nil {
		return fmt.Errorf("runner: claim %s: %w", id, err)
	}
	_, err = fmt.Fprintf(f, "%d\n", os.Getpid())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Finish writes a job's result, atomically.
func Finish(dir string, r Result) error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("runner: invalid job id %q", r.ID)
	}
	r.Version = Version
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(ResultPath(dir, r.ID), append(data, '\n'), 0o600)
}

// Read reads a job's result; ok is false while there is none (the job
// runs, or its process ended before it could write one).
func Read(dir, id string) (r Result, ok bool, err error) {
	if !idPattern.MatchString(id) {
		return Result{}, false, fmt.Errorf("runner: invalid job id %q", id)
	}
	data, err := os.ReadFile(ResultPath(dir, id))
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("runner: %w", err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, false, fmt.Errorf("runner: result %s: %w", id, err)
	}
	return r, true, nil
}

// Entry is a job as recent operations lists it.
type Entry struct {
	Job Job
	// Claimed is true once nhm ui run took the job.
	Claimed bool
	// Result is nil while the job has none.
	Result *Result
}

// List lists the newest jobs, newest first; limit <= 0 is 20. A file
// that cannot be read is skipped.
func List(dir string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 20
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.job.json"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, p := range names {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(data, &j) != nil || j.Check() != nil {
			continue
		}
		e := Entry{Job: j}
		if _, err := os.Stat(claimPath(dir, j.ID)); err == nil {
			e.Claimed = true
		}
		if r, ok, err := Read(dir, j.ID); err == nil && ok {
			e.Result = &r
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(a, b int) bool {
		if !out[a].Job.CreatedAt.Equal(out[b].Job.CreatedAt) {
			return out[a].Job.CreatedAt.After(out[b].Job.CreatedAt)
		}
		return out[a].Job.ID > out[b].Job.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
