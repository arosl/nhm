package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/pflag"
)

// runOverSSH runs the whole call on host, as the operator ruled
// (2026-10-24): "ssh [-t] -- H nhm <args without --ssh>". nhm's store
// lives beside herdr, so the nhm on H answers for H's agents. The code is
// ssh's: the remote nhm's, or 255 when ssh fails.
func runOverSSH(host string, rest []string, stdout, stderr io.Writer) int {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintln(stderr, "Error: --ssh: ssh is not on PATH")
		return exitError
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
			return ee.ExitCode()
		}
		fmt.Fprintln(stderr, "Error: --ssh:", err)
		return exitError
	}
	return exitOK
}

// remoteRobotCall hands a robot call (its first word, --ssh and the other
// root flags aside, is a --robot-* flag: robotCall) to the --ssh host
// before the robot grammar parses it. ok is false for any other call, and
// when the call names no host or an empty one, as ntm takes it. Every
// other call goes through the root, which hands it over after parsing
// (rootOptions.apply).
func remoteRobotCall(args []string, stdout, stderr io.Writer) (code int, ok bool) {
	host, rest, found, err := takeSSH(args)
	if err != nil || !found || host == "" {
		return 0, false
	}
	if _, _, robot := robotCall(rest); !robot {
		return 0, false
	}
	return runOverSSH(host, rest, stdout, stderr), true
}

// takeSSH takes --ssh H (or --ssh=H) out of a robot call's args, before
// any "--". The last one wins, as pflag's does.
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

// flagValues says which flags take a value, so that stripSSH reads the
// words as pflag does: a word after a flag that takes one is its value,
// even when it reads "--ssh".
type flagValues struct {
	long  func(name string) bool
	short func(c byte) bool
}

// pflagValues are the value flags of a parsed command (its own and the
// inherited root flags): in pflag, a flag without NoOptDefVal takes one.
func pflagValues(fs *pflag.FlagSet) flagValues {
	return flagValues{
		long: func(name string) bool {
			f := fs.Lookup(name)
			return f != nil && f.NoOptDefVal == ""
		},
		short: func(c byte) bool {
			f := fs.ShorthandLookup(string(c))
			return f != nil && f.NoOptDefVal == ""
		},
	}
}

// rootValues are the root's value flags only: what a legacy verb's own
// flags take is not known here.
var rootValues = flagValues{
	long: func(name string) bool {
		f, ok := findRootFlag(name)
		return ok && f.value
	},
	short: func(byte) bool { return false },
}

// stripSSH returns args without the --ssh flags that pflag took, before
// any "--": a word after a flag that takes a value is that value, and
// stays.
func stripSSH(args []string, v flagValues) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i:]...)
		case a == "--ssh":
			i++ // and its value
			continue
		case strings.HasPrefix(a, "--ssh="):
			continue
		case strings.HasPrefix(a, "--") && !strings.Contains(a, "=") && v.long(a[2:]):
			out = append(out, a)
			if i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			continue
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			out = append(out, a)
			// A cluster (-ivC): a value flag takes the rest of the word,
			// or the next word when it ends the cluster.
			for j := 1; j < len(a); j++ {
				if v.short(a[j]) {
					if j == len(a)-1 && i+1 < len(args) {
						i++
						out = append(out, args[i])
					}
					break
				}
			}
			continue
		}
		out = append(out, a)
	}
	return out
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
