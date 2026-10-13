package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
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
	// One result per job: the verb's, or the one a signal writes.
	var once sync.Once
	finish := func(r runner.Result) (err error) {
		err = errors.New("the job's result was written before")
		once.Do(func() { err = runner.Finish(dir, r) })
		return err
	}
	// A signal that stops the job after its claim ends it with an unknown
	// outcome: some of its effects may have happened.
	// The handler ends with runJob (in tests it runs in process).
	sigs, ended := make(chan os.Signal, 1), make(chan struct{})
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer func() {
		signal.Stop(sigs)
		close(ended)
	}()
	go func(r runner.Result) {
		var s os.Signal
		select {
		case s = <-sigs:
		case <-ended:
			return
		}
		r.Code, r.Unknown, r.EndedAt = exitError, true, time.Now().UTC()
		r.Error = fmt.Sprintf("%s stopped on %v after its claim; its outcome is unknown: recent operations shows its record", j.Verb, s)
		if err := finish(r); err == nil {
			fmt.Fprintf(stderr, "nhm ui run: %s\n", r.Error)
		}
		os.Exit(exitError)
	}(res)
	v, code, unknown, err := runJobVerb(j)
	res.Code, res.Unknown = code, unknown
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
	if err := finish(res); err != nil {
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
// instance of the job's socket. A panic in the verb ends the job with an
// error and an unknown outcome (some effects may have happened), so its
// result is still written and never reads as "nothing ran".
func runJobVerb(j runner.Job) (v any, code int, unknown bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, code, unknown = nil, exitError, true
			err = fmt.Errorf("%s stopped on an internal error: %v; its outcome is unknown: recent operations shows its record", j.Verb, r)
		}
	}()
	f, ok := jobVerbs[j.Verb]
	if !ok {
		return nil, exitRefused, false, fmt.Errorf("this nhm runs no %q job", j.Verb)
	}
	// Verbs find their project from the cwd, as when typed in the pane.
	if err := os.Chdir(j.Dir); err != nil {
		return nil, exitRefused, false, fmt.Errorf("the job's directory: %w", err)
	}
	ie, err := openInstance(j.Socket)
	if err != nil {
		return nil, exitError, false, err
	}
	defer ie.db.Close()
	v, code, err = f(context.Background(), ie, j)
	return v, code, false, err
}
