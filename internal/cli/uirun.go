package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/ui/runner"
)

// jobFunc runs one verb of a UI job: it decodes the job's request, calls
// the core through the same helper as the CLI verb, and returns the
// core's result and the verb's exit code.
type jobFunc func(ctx context.Context, ie *instanceEnv, j runner.Job) (any, int, error)

// jobVerbs are the verbs nhm ui run knows. Each registers in its own
// file (uirun_<verb>.go).
var jobVerbs = map[string]jobFunc{}

func registerJob(verb string, f jobFunc) {
	if _, dup := jobVerbs[verb]; dup {
		panic("cli: job verb registered twice: " + verb)
	}
	jobVerbs[verb] = f
}

// runJob is `nhm ui run <job file>`, which runner.Start starts in a
// session of its own: it claims the job (a job runs once), runs it in
// the job's directory and writes its result beside it.
func runJob(path string, stdout, stderr io.Writer) int {
	j, err := runner.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "nhm ui run: %v\n", err)
		return exitRefused
	}
	dir := filepath.Dir(path)
	if err := runner.Claim(dir, j.ID); err != nil {
		fmt.Fprintf(stderr, "nhm ui run: %s: %v\n", j.ID, err)
		return exitRefused
	}
	// A job started another way than runner.Start still outlives the
	// pane that started it.
	signal.Ignore(syscall.SIGHUP)
	res := runner.Result{ID: j.ID, Verb: j.Verb, StartedAt: time.Now().UTC()}
	v, code, err := runJobVerb(j)
	res.Code = code
	if err != nil {
		res.Error = err.Error()
	}
	if v != nil {
		data, merr := json.Marshal(v)
		if merr != nil && err == nil {
			res.Error = "the result cannot be written: " + merr.Error()
		}
		res.Result = data
	}
	res.EndedAt = time.Now().UTC()
	if err := runner.Finish(dir, res); err != nil {
		fmt.Fprintf(stderr, "nhm ui run: %s: %v\n", j.ID, err)
		return exitError
	}
	fmt.Fprintf(stdout, "nhm ui run: %s %s ended with code %d\n", j.Verb, j.ID, code)
	if res.Error != "" {
		fmt.Fprintf(stderr, "nhm ui run: %s\n", res.Error)
	}
	return code
}

// runJobVerb runs the job's verb in the job's directory, with the nhm
// instance of the job's socket. A panic ends the job with an error, so
// its result is still written.
func runJobVerb(j runner.Job) (v any, code int, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, code, err = nil, exitError, fmt.Errorf("%s stopped on an internal error: %v", j.Verb, r)
		}
	}()
	f, ok := jobVerbs[j.Verb]
	if !ok {
		return nil, exitRefused, fmt.Errorf("this nhm runs no %q job", j.Verb)
	}
	// Verbs find their project from the cwd, as when typed in the pane.
	if err := os.Chdir(j.Dir); err != nil {
		return nil, exitRefused, fmt.Errorf("the job's directory: %w", err)
	}
	ie, err := openInstance(j.Socket)
	if err != nil {
		return nil, exitError, err
	}
	defer ie.db.Close()
	return f(context.Background(), ie, j)
}
