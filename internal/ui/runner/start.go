package runner

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Start writes the job and starts `bin ui run <job file>` for it in a new
// session (setsid): the SIGHUP of the popup's pane does not reach it, and
// it does not end with the popup. Its cwd is the job's Dir, stdin is
// /dev/null, and its output goes to the job's log. Start does not wait
// for the job. A job that does not start gets its result at once, so it
// never looks like it runs.
func Start(bin string, env []string, dir string, j Job) error {
	if err := write(dir, j); err != nil {
		return err
	}
	log, err := os.OpenFile(LogPath(dir, j.ID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return notStarted(dir, j, err)
	}
	cmd := exec.Command(bin, "ui", "run", JobPath(dir, j.ID))
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = j.Dir, env, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = log.Close()
	if err != nil {
		return notStarted(dir, j, err)
	}
	// Reap the child while this process lives; it outlives this process
	// when that ends first.
	go func() { _ = cmd.Wait() }()
	return nil
}

func notStarted(dir string, j Job, cause error) error {
	err := fmt.Errorf("runner: %s did not start: %w; nothing ran", j.Verb, cause)
	now := time.Now().UTC()
	r := Result{ID: j.ID, Verb: j.Verb, Code: CodeError, Error: err.Error(), StartedAt: now, EndedAt: now}
	if claimErr := Claim(dir, j.ID); claimErr == nil {
		_ = Finish(dir, r)
	}
	return err
}
