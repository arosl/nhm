package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
)

// resolveSession is ntm's session resolution (core/session) on this
// instance: name, or without one the calling pane's workspace, the cwd,
// the only session, the picker, else an error that lists the sessions.
// An inferred session is said on stderr, except with JSON. A cancelled
// picker returns an empty Session and no error: the verb does nothing and
// exits 0.
func resolveSession(ctx context.Context, ie *instanceEnv, name string, asJSON bool, stdout, stderr io.Writer) (session.Resolution, error) {
	cwd, _ := os.Getwd()
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	ss, current, err := herdrSessions(ctx, ie, name == "")
	if err != nil {
		return session.Resolution{}, err
	}
	o := session.Options{JSON: asJSON, Current: current, Cwd: cwd, Interactive: interactive(stdout)}
	r, err := session.Resolve(ctx, name, func(context.Context) ([]session.Session, error) { return ss, nil }, o)
	if err != nil {
		return r, err
	}
	r.ExplainIfInferred(stderr, asJSON)
	return r, nil
}

// herdrSessions lists herdr's workspaces as ntm's sessions and, with
// withCurrent, the calling pane's workspace: the live workspace of the
// caller's pane (it may have moved), else HERDR_WORKSPACE_ID. A named
// session needs no current one, and so no herdr call for the pane.
func herdrSessions(ctx context.Context, ie *instanceEnv, withCurrent bool) ([]session.Session, string, error) {
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return nil, "", err
	}
	active, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, "", err
	}
	dirs := map[string][]string{}
	seen := map[[2]string]bool{}
	for _, b := range active {
		k := [2]string{b.WorkspaceID, b.MailProjectKey}
		if b.WorkspaceID == "" || b.MailProjectKey == "" || seen[k] {
			continue
		}
		seen[k] = true
		dirs[b.WorkspaceID] = append(dirs[b.WorkspaceID], b.MailProjectKey)
	}
	out := make([]session.Session, 0, len(snap.Workspaces))
	for _, w := range snap.Workspaces {
		name := w.Label
		if name == "" {
			name = w.WorkspaceID
		}
		out = append(out, session.Session{Name: name, WorkspaceID: w.WorkspaceID, Attached: w.Focused, ProjectDirs: dirs[w.WorkspaceID]})
	}
	if !withCurrent {
		return out, "", nil
	}
	current := os.Getenv("HERDR_WORKSPACE_ID")
	if pane := ie.callerPane(ctx); pane != "" {
		for _, p := range snap.Panes {
			if p.PaneID == pane {
				current = p.WorkspaceID
				break
			}
		}
	}
	return out, current, nil
}

// interactive reports whether a picker can run: stdout and stdin are both
// terminals (ntm's IsInteractive and IsInteractiveStdin).
func interactive(stdout io.Writer) bool {
	f, ok := stdout.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
}
