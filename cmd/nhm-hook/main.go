// Command nhm-hook is the entry point of the hooks that report evidence
// to nhm (plan-v8 2.7):
//
//	nhm-hook posttooluse     Claude Code or Codex PostToolUse on Agent Mail's
//	                         register_agent, create_agent_identity or
//	                         macro_start_session (payload on stdin)
//	nhm-hook sessionstart    SessionStart, PreCompact or PostCompact (stdin)
//	nhm-hook reconcile-pane  herdr's pane.agent_detected plugin event
//	nhm-hook agent-status    herdr's pane.agent_status_changed plugin event;
//	                         it also runs settle wake (plan-v8 2.8)
//	nhm-hook mail-sent       Claude Code or Codex PostToolUse on Agent Mail's
//	                         send_message or reply_message (stdin): wake on
//	                         send (plan-v8 2.8)
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

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/hooks"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
)

// The hook's time (plan-v8 2.7: two seconds, fail open). budget is the
// deadline of the work: herdr calls and SQLite statements end there and
// the hook returns cleanly. hardStop is the backstop that exits whatever
// still blocks; it comes later, so the clean stop always ends first.
const (
	budget   = 1500 * time.Millisecond
	hardStop = 1900 * time.Millisecond
)

// maxPayload bounds what a hook reads from stdin.
const maxPayload = 1 << 20

const usage = `Usage: nhm-hook posttooluse|sessionstart|mail-sent|reconcile-pane|agent-status

Agent CLI hooks (posttooluse, sessionstart, mail-sent) read the hook
payload on stdin. herdr plugin event hooks (reconcile-pane, agent-status) read
HERDR_PLUGIN_EVENT_JSON. Every hook exits 0.
`

// busyWait bounds each SQLite statement's wait for another writer. A
// burst of hooks (herdr runs up to 32 plugin commands at once) or a long
// nhm transaction must not hold the agent's turn.
const busyWait = 200 * time.Millisecond

func main() {
	// The backstop: whatever blocks, the hook exits 0 inside two seconds.
	time.AfterFunc(hardStop, func() { os.Exit(0) })
	ui.FixPluginPath()
	os.Exit(safeRun(run, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

type runFunc func(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int

// safeRun runs the hook and turns a panic into exit 0: Claude Code treats
// exit 2 from PostToolUse as a blocking error and shows it to the agent,
// and Go exits 2 on a panic.
func safeRun(f runFunc, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "nhm-hook: panic: %v\n", r)
			code = 0
		}
	}()
	return f(args, stdin, stdout, stderr, getenv)
}

// readPayload reads stdin until EOF or until ctx ends.
func readPayload(ctx context.Context, stdin io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("stdin: %w", ctx.Err())
	}
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
	case "posttooluse", "sessionstart", "mail-sent", "reconcile-pane", "agent-status":
	default:
		fmt.Fprintf(stderr, "nhm-hook: unknown hook %q\n%s", verb, usage)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var payload []byte
	if verb == "posttooluse" || verb == "sessionstart" || verb == "mail-sent" {
		rctx, rcancel := context.WithTimeout(ctx, budget/4)
		data, err := readPayload(rctx, stdin)
		rcancel()
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
			return 0
		}
		payload = data
	}
	env, closeEnv, err := openEnv(ctx, getenv)
	if errors.Is(err, store.ErrNotEnrolled) || errors.Is(err, errNoSocket) || errors.Is(err, store.ErrSchemaOld) {
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
	case "mail-sent":
		rep, err := mailSent(ctx, env, getenv, payload)
		if err != nil {
			fmt.Fprintf(stderr, "nhm-hook %s: %v\n", verb, err)
		}
		debug(stderr, getenv, verb, rep)
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
		if verb == "agent-status" && (ev.AgentStatus == herdr.StatusIdle || ev.AgentStatus == herdr.StatusDone) {
			pane := ev.PaneID
			if pane == "" {
				pane = getenv("HERDR_PANE_ID")
			}
			d, err := settle(ctx, env, getenv, pane, ev.AgentStatus)
			if err != nil {
				fmt.Fprintf(stderr, "nhm-hook %s: wake: %v\n", verb, err)
			}
			debug(stderr, getenv, verb, d)
		}
	}
	return 0
}

// wakeEnv is the wake side of an opened instance. Settle reads an inbox,
// so it gets an Agent Mail client; mail-sent reads none.
func wakeEnv(env hooks.Env, getenv func(string) string, withMail bool) (wake.Env, error) {
	h := herdr.New(getenv("HERDR_SOCKET_PATH"))
	w := wake.Env{DB: env.DB, Bindings: env.Bindings, Herdr: h, Config: wake.DefaultConfig(), Now: env.Now,
		Inode: func() uint64 { return wait.SocketInode(h.SocketPath) }}
	if withMail {
		mail, err := agentmail.NewClient(env.ConfigHome)
		if err != nil {
			return w, err
		}
		w.Mail = mail
	}
	return w, nil
}

// asOwner runs fn under the shared owner lock when nhm owns the
// instance. Under the Bash, its own wake runs; the hook does nothing.
func asOwner(ctx context.Context, env hooks.Env, fn func() error) (string, error) {
	wait := env.LockWait
	if wait <= 0 {
		wait = time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	skipped := ""
	err := core.WithOwnerShared(lctx, env.InstanceDir, func(o core.Owner) error {
		if o.Owner != core.OwnerNhm {
			skipped = "the instance is owned by " + o.Owner
			return nil
		}
		return fn()
	})
	if errors.Is(err, core.ErrOwnerLockTimeout) {
		return "owner lock busy; maintenance delivers the wake", nil
	}
	return skipped, err
}

// mailSentReport is what mail-sent did.
type mailSentReport struct {
	Skipped string       `json:"skipped,omitempty"`
	Wake    *wake.Report `json:"wake,omitempty"`
}

// mailSent handles the PostToolUse hook of send_message and
// reply_message: it records the recipients' pending ids and nudges each
// idle or done one, inside the hook's budget.
func mailSent(ctx context.Context, env hooks.Env, getenv func(string) string, payload []byte) (mailSentReport, error) {
	p, err := hooks.ParseAgentPayload(payload)
	if err != nil {
		return mailSentReport{}, err
	}
	if p.HookEventName != "PostToolUse" || wake.SentTool(p.ToolName) == "" {
		return mailSentReport{Skipped: "not an Agent Mail send"}, nil
	}
	sent, err := wake.ParseSent(p.ToolInput, p.ToolResponse)
	if err != nil {
		// A refused send delivered nothing.
		return mailSentReport{Skipped: err.Error()}, nil
	}
	w, err := wakeEnv(env, getenv, false)
	if err != nil {
		return mailSentReport{}, err
	}
	var rep wake.Report
	skipped, err := asOwner(ctx, env, func() error {
		var err error
		rep, err = wake.MailSent(ctx, w, sent)
		return err
	})
	if skipped != "" {
		return mailSentReport{Skipped: skipped}, err
	}
	return mailSentReport{Wake: &rep}, err
}

// settle runs settle wake for a pane that became idle or done.
func settle(ctx context.Context, env hooks.Env, getenv func(string) string, pane string, status herdr.AgentStatus) (any, error) {
	if pane == "" {
		return map[string]string{"skipped": "the event names no pane"}, nil
	}
	w, err := wakeEnv(env, getenv, true)
	if err != nil {
		return nil, err
	}
	var d wake.Decision
	skipped, err := asOwner(ctx, env, func() error {
		var err error
		d, err = wake.Settle(ctx, w, pane, status)
		return err
	})
	if skipped != "" {
		return map[string]string{"skipped": skipped}, err
	}
	return d, err
}

var errNoSocket = errors.New("no HERDR_SOCKET_PATH: not in a herdr pane")

// openEnv opens the instance enrolled for the hook's herdr socket. It
// never migrates the database: an older schema waits for the next nhm
// command.
func openEnv(ctx context.Context, getenv func(string) string) (hooks.Env, func(), error) {
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
	db, err := store.OpenForHook(ctx, inst.Dir, busyWait)
	if err != nil {
		return hooks.Env{}, nil, err
	}
	env := hooks.Env{DB: db, Bindings: bindings.NewStore(db), Herdr: herdr.New(socket), InstanceDir: inst.Dir,
		Getenv: getenv, Now: time.Now, LockWait: 300 * time.Millisecond, Settle: 500 * time.Millisecond}
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
