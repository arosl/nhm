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

	"github.com/arosl/nhm/internal/core/bindings"
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
// read-only verbs and forms are used: health, version, compatibility
// status, admin recover inspect, feedback list, ensemble status, status,
// attention, quota's report without its store (runQuotaView), limits
// --dry-run, read and mail inbox --keep-unread. Doctor's checks and the
// capabilities come as nhm's own data and text.
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
		Version:       verbText(verbRun("version")),
		Compatibility: verbText(runCompatibility, "status"),
		Recover:       verbText(runRecoverInspect, withSocket()...),
		Feedback:      verbText(runFeedback, withSocket("list")...),
		Status:        verbText(runStatus, withSocket()...),
		Attention:     verbText(runAttention, withSocket()...),
		// quota's report, which stores nothing here, then a limits pass
		// that records nothing (no seen lines, no mail).
		Usage: func(ctx context.Context) (string, error) {
			q, qerr := verbText(runQuotaView, withSocket()...)(ctx)
			l, lerr := verbText(runLimits, withSocket("--dry-run")...)(ctx)
			if strings.TrimSpace(l) == "" && lerr == nil {
				l = "no agent is at a limit\n"
			}
			return "quota\n\n" + q + "\nlimits (a dry run: nothing is recorded)\n\n" + l, errors.Join(qerr, lerr)
		},
		Read: func(ctx context.Context, t ui.Target) (views.Screen, error) {
			name, pane, err := frozenTarget(sock, t)
			if err != nil {
				return views.Screen{Name: name, Pane: pane}, err
			}
			text, err := verbText(runRead, withSocket(pane, "--source", "recent", "--lines", "500")...)(ctx)
			return views.Screen{Name: name, Pane: pane, Text: text}, err
		},
		Inbox: func(ctx context.Context, t ui.Target) (string, error) {
			if !t.Bound() {
				return "", errors.New("an agent without a binding has no Agent Mail inbox here")
			}
			b, err := frozenBinding(sock, t)
			if err != nil {
				return "", err
			}
			return verbText(runMail, withSocket(inboxArgs(b)...)...)(ctx)
		},
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

// frozenBinding is the binding a bound target names, while it still holds
// the target's generation (plan-v4 section 4: a replaced target stays
// visibly invalid and is never swapped for the agent now in its place). A
// binding that moved keeps its generation and is followed.
func frozenBinding(sock string, t ui.Target) (bindings.Binding, error) {
	ie, err := openInstance(sock)
	if err != nil {
		return bindings.Binding{}, err
	}
	defer ie.db.Close()
	b, err := bindings.NewStore(ie.db).Get(context.Background(), t.BindingID)
	switch {
	case errors.Is(err, bindings.ErrNotFound):
		return b, fmt.Errorf("the agent you picked is gone: no binding %s", t.BindingID)
	case err != nil:
		return b, err
	case b.Status != bindings.Active || b.Generation != t.Generation:
		return b, fmt.Errorf("the agent you picked is gone: %s is %s at generation %d, not generation %d", b.MailName, b.Status, b.Generation, t.Generation)
	}
	return b, nil
}

// frozenTarget is the name and pane to read for a target: a bound one's
// binding (frozenBinding), else the pane it names.
func frozenTarget(sock string, t ui.Target) (string, string, error) {
	if !t.Bound() {
		return t.Kind, t.PaneID, nil
	}
	b, err := frozenBinding(sock, t)
	if err != nil {
		return b.MailName, t.PaneID, err
	}
	return b.MailName, b.PaneID, nil
}

// inboxArgs is nhm mail inbox for a binding's agent. --keep-unread always:
// a view marks no mail read (plan-v4 section 4).
func inboxArgs(b bindings.Binding) []string {
	return []string{"inbox", "--agent", b.MailName, "--project", b.MailProjectKey, "--keep-unread"}
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
