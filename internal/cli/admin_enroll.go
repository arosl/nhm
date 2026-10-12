package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

const enrollUsage = `Usage: nhm admin instance enroll [--session NAME] [--socket PATH] [--json]

Enroll a herdr server: give it an instance directory under
~/.local/state/nhm with its database. ACFS's install runs this once per
herdr server. Running it again changes nothing. Hooks and plugin actions
from a server that is not enrolled do nothing.

The socket is --socket, else HERDR_SOCKET_PATH, else herdr's own path for
the session: ~/.config/herdr/herdr.sock for "default", or
~/.config/herdr/sessions/<NAME>/herdr.sock. A server that does not answer
is enrolled anyway, with a warning: the install may run before herdr
starts.
`

// EnrollResult is the JSON result of enroll.
type EnrollResult struct {
	InstanceID string `json:"instance_id"`
	Dir        string `json:"dir"`
	Session    string `json:"session"`
	SocketPath string `json:"socket_path"`
	Created    bool   `json:"created"`
	// HerdrVersion is empty when the server did not answer.
	HerdrVersion  string `json:"herdr_version,omitempty"`
	HerdrProtocol int    `json:"herdr_protocol,omitempty"`
}

// pingWait bounds enroll's check of the herdr server.
const pingWait = 3 * time.Second

func runInstanceEnroll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("admin instance enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, enrollUsage) }
	session := fs.String("session", "default", "the herdr session")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) > 0 || strings.TrimSpace(*session) == "" {
		fmt.Fprint(stderr, enrollUsage)
		return exitRefused
	}
	env := envelope{Command: "admin instance enroll"}
	path := *socket
	if path == "" {
		path = os.Getenv("HERDR_SOCKET_PATH")
	}
	if path == "" {
		if path, err = defaultSocket(*session); err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitError, nil)
		}
	}
	root, err := store.StateRoot()
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	inst, created, err := store.Enroll(root, path, *session, time.Now())
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	res := EnrollResult{InstanceID: inst.ID, Dir: inst.Dir, Session: inst.Session, SocketPath: inst.SocketPath, Created: created}
	ctx, cancel := context.WithTimeout(context.Background(), pingWait)
	defer cancel()
	if pong, err := herdr.New(inst.SocketPath).Ping(ctx); err != nil {
		env.Warnings = append(env.Warnings, fmt.Sprintf("herdr does not answer on %s (%v); enrolled anyway", inst.SocketPath, err))
	} else {
		res.HerdrVersion, res.HerdrProtocol = pong.Version, pong.Protocol
		if pong.Protocol != herdr.Protocol {
			env.Warnings = append(env.Warnings, fmt.Sprintf("herdr %s speaks protocol %d; this nhm speaks %d", pong.Version, pong.Protocol, herdr.Protocol))
		}
	}
	env.Result = res
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		verb := "enrolled"
		if !created {
			verb = "already enrolled"
		}
		fmt.Fprintf(w, "%s %s: session %s, socket %s\n", verb, inst.ID, inst.Session, inst.SocketPath)
	})
}

// defaultSocket is herdr's own socket path for a session (herdr 0.9.3):
// $XDG_CONFIG_HOME/herdr/herdr.sock for the default session, and
// $XDG_CONFIG_HOME/herdr/sessions/<name>/herdr.sock for a named one.
func defaultSocket(session string) (string, error) {
	if strings.ContainsAny(session, "/\x00") || session == "." || session == ".." {
		return "", fmt.Errorf("invalid herdr session name %q", session)
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", errors.New("no home directory and no XDG_CONFIG_HOME: give --socket")
		}
		dir = filepath.Join(home, ".config")
	}
	if session == "default" {
		return filepath.Join(dir, "herdr", "herdr.sock"), nil
	}
	return filepath.Join(dir, "herdr", "sessions", session, "herdr.sock"), nil
}
