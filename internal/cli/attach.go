package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
)

const attachUsage = `Usage: nhm attach [session] [--socket PATH] [--json]

ntm's attach on herdr: focus the session's workspace (a workspace id or
label), so that every herdr client shows it. Open herdr to see it. Without
a session, it runs nhm list (the sessions), as ntm runs its list. Changes
no agent, so it runs whoever owns this herdr instance.
`

var (
	slotPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	indexPattern  = regexp.MustCompile(`^[0-9]+$`)
	paneIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*:p[0-9A-Za-z]+$`)
)

// AttachResult is attach's result.
type AttachResult struct {
	WorkspaceID string `json:"workspace_id"`
}

func runAttach(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, attachUsage) }
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	switch len(pos) {
	case 0:
		listArgs := []string{}
		if *socket != "" {
			listArgs = append(listArgs, "--socket", *socket)
		}
		if *asJSON {
			listArgs = append(listArgs, "--json")
		}
		return verbRun("list")(listArgs, stdout, stderr)
	case 1:
	default:
		fmt.Fprint(stderr, attachUsage)
		return exitRefused
	}
	env := envelope{Command: "attach"}
	ctx := context.Background()
	h, code, err := focusSession(ctx, *socket, pos[0])
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, code, nil)
	}
	res := AttachResult{WorkspaceID: h}
	env.Result = res
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "focused %s (%s); open herdr to see it\n", pos[0], res.WorkspaceID)
	})
}

// focusSession focuses a session's workspace and returns its id, or the
// exit code and error.
func focusSession(ctx context.Context, socket, session string) (string, int, error) {
	ie, err := openInstance(socket)
	if err != nil {
		return "", exitError, err
	}
	defer ie.db.Close()
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return "", exitRefused, fmt.Errorf("%w (nhm list shows the sessions)", err)
	}
	if err := ie.herdr.WorkspaceFocus(ctx, ws); err != nil {
		return "", exitError, err
	}
	return ws, exitOK, nil
}
