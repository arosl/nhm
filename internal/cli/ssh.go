package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// remoteCall runs the whole call on the --ssh host, as the operator ruled
// (2026-10-24): "ssh [-t] -- H nhm <args without --ssh>". nhm's store
// lives beside herdr, so the nhm on H answers, robot flags and help too.
// ok is false when the call names no host (no --ssh, or an empty one, as
// ntm takes an empty --ssh as none). The code is ssh's: the remote nhm's,
// or 255 when ssh fails.
func remoteCall(args []string, stdout, stderr io.Writer) (code int, ok bool) {
	host, rest, found, err := takeSSH(args)
	if err != nil {
		// cobra reports a missing value with pflag's text.
		return 0, false
	}
	if !found || host == "" {
		return 0, false
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintln(stderr, "Error: --ssh: ssh is not on PATH")
		return exitError, true
	}
	sshArgs := []string{}
	if interactive(stdout) {
		sshArgs = append(sshArgs, "-t")
	}
	sshArgs = append(sshArgs, "--", host, remoteCommand(rest))
	cmd := exec.Command(ssh, sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), true
		}
		fmt.Fprintln(stderr, "Error: --ssh:", err)
		return exitError, true
	}
	return exitOK, true
}

// takeSSH takes --ssh H (or --ssh=H) out of args, before any "--". The
// last one wins, as pflag's does.
func takeSSH(args []string) (host string, rest []string, found bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return host, append(rest, args[i:]...), found, nil
		}
		switch {
		case a == "--ssh":
			if i+1 >= len(args) {
				return "", nil, false, errors.New("flag needs an argument: --ssh")
			}
			i++
			host, found = args[i], true
		case strings.HasPrefix(a, "--ssh="):
			host, found = strings.TrimPrefix(a, "--ssh="), true
		default:
			rest = append(rest, a)
		}
	}
	return host, rest, found, nil
}

// remoteCommand is the remote shell's command line: nhm and each argument
// in single quotes, so the remote shell runs exactly these words.
func remoteCommand(args []string) string {
	words := []string{"nhm"}
	for _, a := range args {
		words = append(words, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(words, " ")
}
