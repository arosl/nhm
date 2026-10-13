package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/views"
)

// viewsEnv is what the menu's read-only views (internal/ui/views) read,
// for the herdr server of the popup's context. The menu's wiring passes it
// to views.New.
//
// Each text loader runs a CLI verb in process and returns what it prints,
// so a view shows what the verb shows, and no subprocess runs. Only
// read-only verbs are used: health, version, compatibility status, admin
// recover inspect, feedback list and ensemble status. Doctor's checks and
// the capabilities come as nhm's own data and text.
func viewsEnv(c ui.Context) views.Env {
	sock := c.SocketPath
	if sock == "" {
		sock = os.Getenv("HERDR_SOCKET_PATH")
	}
	withSocket := func(args ...string) []string {
		if sock == "" {
			return args
		}
		return append(args, "--socket", sock)
	}
	env := views.Env{
		Doctor: func(ctx context.Context) ([]views.Check, error) {
			var out []views.Check
			for _, ch := range doctorChecks(ctx, sock, true) {
				out = append(out, views.Check{Name: ch.Name, Result: ch.Result, Detail: ch.Detail, Fix: ch.Fix})
			}
			return out, nil
		},
		Health: verbText(runHealth, withSocket()...),
		// nhm capabilities' data. Its table pads every form to the
		// longest (about 220 columns), so the view puts the answer first.
		Capabilities: func(context.Context) (string, error) {
			var b strings.Builder
			fmt.Fprintf(&b, "nhm %s, herdr protocol %d, ntm %.8s\n\n", Version, herdr.Protocol, NTMCommit)
			for _, c := range capabilities() {
				if c.Available {
					fmt.Fprintf(&b, "yes  %s\n", c.Form)
				} else {
					fmt.Fprintf(&b, "no   %s\n     %s\n", c.Form, c.Reason)
				}
			}
			return b.String(), nil
		},
		Version:       verbText(runVersion),
		Compatibility: verbText(runCompatibility, "status"),
		Recover:       verbText(runRecoverInspect, withSocket()...),
		Feedback:      verbText(runFeedback, withSocket("list")...),
		RunStatus: func(ctx context.Context, id string) (string, error) {
			return verbText(runEnsemble, withSocket("status", id)...)(ctx)
		},
	}
	if root, err := store.StateRoot(); err == nil && sock != "" {
		if inst, err := store.Lookup(root, sock); err == nil {
			env.RunsDir = filepath.Join(inst.Dir, store.RunsDir)
		}
	}
	return env
}

// verbText runs a CLI verb in process with args. Its text is what the
// verb prints on stdout, then its warnings on stderr. A verb that fails
// gives an error, from the first line of its stderr; the text stays when
// it printed some (health prints what it read and fails a check).
func verbText(run func([]string, io.Writer, io.Writer) int, args ...string) views.Text {
	return func(context.Context) (string, error) {
		var out, errOut bytes.Buffer
		code := run(args, &out, &errOut)
		text, notes := out.String(), strings.TrimSpace(errOut.String())
		if code == exitOK {
			if notes != "" {
				text = strings.TrimRight(text, "\n") + "\n\n" + notes + "\n"
			}
			return text, nil
		}
		msg, _, _ := strings.Cut(notes, "\n")
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		return text, errors.New(msg)
	}
}
