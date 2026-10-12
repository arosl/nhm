// Command nhm-hook is the entry point of the hooks that report evidence
// to nhm (plan-v8 2.7):
//
//	nhm-hook posttooluse     Claude Code or Codex PostToolUse on Agent Mail's
//	                         register_agent, create_agent_identity or
//	                         macro_start_session (payload on stdin)
//	nhm-hook sessionstart    SessionStart, PreCompact or PostCompact (stdin)
//	nhm-hook reconcile-pane  herdr's pane.agent_detected plugin event
//	nhm-hook agent-status    herdr's pane.agent_status_changed plugin event
//
// A hook fails open: it always exits 0 within its budget, and says what
// went wrong on stderr. It does nothing outside an enrolled herdr
// instance. A SessionStart hook prints one line for the agent's context
// when its pane holds a binding: "You are IcyKnoll (herdr icyknoll)."
// NHM_HOOK_DEBUG=1 prints what the hook did on stderr.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/hooks"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// budget bounds a hook call (plan-v8 2.7: two seconds, fail open).
const budget = 2 * time.Second

// maxPayload bounds what a hook reads from stdin.
const maxPayload = 1 << 20

const usage = `Usage: nhm-hook posttooluse|sessionstart|reconcile-pane|agent-status

Agent CLI hooks (posttooluse, sessionstart) read the hook payload on
stdin. herdr plugin event hooks (reconcile-pane, agent-status) read
HERDR_PLUGIN_EVENT_JSON. Every hook exits 0.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, usage)
		return 0
	}
	verb := args[0]
	switch verb {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "posttooluse", "sessionstart", "reconcile-pane", "agent-status":
	default:
		fmt.Fprintf(stderr, "nhm-hook: unknown hook %q\n%s", verb, usage)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var payload []byte
	if verb == "posttooluse" || verb == "sessionstart" {
		data, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: stdin: %v\n", verb, err)
			return 0
		}
		payload = data
	}
	env, closeEnv, err := openEnv(getenv)
	if errors.Is(err, store.ErrNotEnrolled) || errors.Is(err, errNoSocket) {
		debug(stderr, getenv, verb, map[string]string{"skipped": err.Error()})
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
		return 0
	}
	defer closeEnv()
	switch verb {
	case "posttooluse", "sessionstart":
		p, err := hooks.ParseAgentPayload(payload)
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
			return 0
		}
		res, err := hooks.RunAgentHook(ctx, env, hooks.AgentHook{Payload: p, EventID: eventID(payload)})
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
		}
		debug(stderr, getenv, verb, res)
		if res.Reminder != "" {
			fmt.Fprintln(stdout, res.Reminder)
		}
	default:
		ev, err := hooks.ParsePluginEvent(getenv("HERDR_PLUGIN_EVENT_JSON"))
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
			return 0
		}
		rep, err := hooks.RunPluginEvent(ctx, env, ev)
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
		}
		debug(stderr, getenv, verb, rep)
	}
	return 0
}

var errNoSocket = errors.New("no HERDR_SOCKET_PATH: not in a herdr pane")

// openEnv opens the instance enrolled for the hook's herdr socket.
func openEnv(getenv func(string) string) (hooks.Env, func(), error) {
	socket := getenv("HERDR_SOCKET_PATH")
	if socket == "" {
		return hooks.Env{}, nil, errNoSocket
	}
	root, err := store.StateRoot()
	if err != nil {
		return hooks.Env{}, nil, err
	}
	inst, err := store.Lookup(root, socket)
	if err != nil {
		return hooks.Env{}, nil, err
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		return hooks.Env{}, nil, err
	}
	env := hooks.Env{DB: db, Bindings: bindings.NewStore(db), Herdr: herdr.New(socket), InstanceDir: inst.Dir,
		Getenv: getenv, Now: time.Now}
	return env, func() { _ = db.Close() }, nil
}

// eventID identifies one hook call: the payload's hash and the calling
// process. The same payload from another call of the agent gets another
// id; a retry of the same call does not.
func eventID(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8]) + "-" + strconv.Itoa(os.Getppid())
}

func debug(stderr io.Writer, getenv func(string) string, verb string, v any) {
	if getenv("NHM_HOOK_DEBUG") != "1" {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(stderr, "nhm-hook %s: %s\n", verb, data)
}
