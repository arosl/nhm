package cm

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// Privacy runs `cm privacy <args>` in dir and passes its output through,
// as `ntm memory privacy` does: its words (status, enable [agents...],
// disable, allow <agent>, deny <agent>) and --json are the caller's. An
// error is the exec's or cm's exit.
func Privacy(ctx context.Context, binary, dir string, stdout, stderr io.Writer, args ...string) error {
	if binary == "" {
		binary = "cm"
	}
	cmd := exec.CommandContext(ctx, binary, append([]string{"privacy"}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}
