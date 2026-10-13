package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/runner"
)

// recycleSelfStart starts the recycle's job; tests replace it, so that no
// test runs its own binary as a job.
var recycleSelfStart = func(c runner.Client, req lifecycle.RecycleRequest) (string, error) {
	return c.Start(runner.VerbRecycle, req)
}

// selfRecycle is nhm recycle --self's result: the job's operation id and
// the agent it recycles.
type selfRecycle struct {
	OperationID string `json:"operation_id"`
	MailName    string `json:"mail_name"`
	BindingID   string `json:"binding_id"`
	PaneID      string `json:"pane_id"`
	Generation  int64  `json:"generation"`
	WaitTurn    string `json:"wait_turn"`
}

// recycleSelf is nhm recycle --self (nhm-abn): an agent asks for its own
// fresh session as the last step of its turn. Recycle refuses to run in
// the agent's pane, because stopping the agent would stop it, so the
// recycle runs as a job of its own (the UI runner's: a session of its
// own, out of the pane) that waits for the agent to end its turn. The
// request is the calling pane's binding at its generation now; recycle's
// static checks run first, so a bad -t or a missing bead is refused
// while the agent can still read the answer.
func recycleSelf(req lifecycle.RecycleRequest, socket string, asJSON bool, stdout, stderr io.Writer) int {
	env := envelope{Command: "recycle"}
	ctx := context.Background()
	ie, err := openInstance(socket)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	pane := ie.callerPane(ctx)
	if pane == "" {
		return output(stdout, stderr, asJSON, env, errors.New("--self recycles the calling pane's agent: run it in the agent's herdr pane"),
			exitRefused, nil)
	}
	b, err := bindings.NewStore(ie.db).ActiveByPane(ctx, pane)
	if errors.Is(err, bindings.ErrNotFound) {
		return output(stdout, stderr, asJSON, env, fmt.Errorf("pane %s has no bound agent: nothing to recycle", pane), exitRefused, nil)
	}
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	req.Target, req.ExpectedGeneration = b.ID, b.Generation
	lenv, err := ie.lifecycleEnv()
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	code, err := underOwner(ctx, ie, func() error {
		_, err := lifecycle.CheckRecycle(ctx, lenv, req)
		return err
	})
	if err != nil {
		if code == exitError {
			code = exitRefused
		}
		return output(stdout, stderr, asJSON, env, err, code, nil)
	}
	c := ui.Context{Version: ui.ContextVersion, SocketPath: ie.herdr.SocketPath, Cwd: b.MailProjectKey}
	client := runner.Client{Bin: self(), Env: outOfPaneEnv(ie.herdr.SocketPath), JobDir: runner.JobDir(ie.inst.Dir), Context: c}
	id, err := recycleSelfStart(client, req)
	if err != nil {
		return output(stdout, stderr, asJSON, env, err, exitError, nil)
	}
	res := selfRecycle{OperationID: id, MailName: b.MailName, BindingID: b.ID, PaneID: b.PaneID, Generation: b.Generation,
		WaitTurn: req.WaitTurn.String()}
	env.OperationID, env.Result = id, res
	return output(stdout, stderr, asJSON, env, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "%s's recycle runs as operation %s once its turn ends (it waits up to %s): end your turn now.\n",
			b.MailName, id, req.WaitTurn)
		fmt.Fprintln(w, "The menu's recent operations show how it ended.")
	})
}

// outOfPaneEnv is this process's environment for a job that runs out of
// the calling pane: no HERDR_PANE_ID or HERDR_TAB_ID, so the job is no
// caller in the pane it acts on, and the socket made explicit.
func outOfPaneEnv(socket string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_SOCKET_PATH":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HERDR_SOCKET_PATH="+socket)
}
