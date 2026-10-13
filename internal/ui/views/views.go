// Package views holds the read-only views of nhm's menu (plan-v4
// sections 3, 4 and 6): the diagnostics, the mode catalog, the ensembles
// and the feedback. A view reads only: it never changes nhm's state,
// marks mail read or records anything. Its data comes from Env, which the
// menu's wiring builds from the open instance, and from nhm's core for the
// catalogs. Each load runs in a tea.Cmd with a deadline and the id of its
// request, so a late answer is dropped and Update never waits. Text from
// outside nhm is shown with its terminal controls removed.
package views

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/board"
)

// Back asks the menu to close the view and show the level it came from.
type Back struct{}

// Open asks the menu to open a form of a row in the same popup: the
// ensembles view sends Open{Row: "view.ensembles", Action: "stop", Arg:
// the run id}. The menu's form reviews and confirms it; the view runs
// nothing itself.
type Open struct {
	Row    string
	Action string
	Arg    string
}

// View is one view, a tea.Model the menu embeds. The menu sends it a
// tea.WindowSizeMsg first and on each resize, and every key while it is
// open; esc, q, backspace and left send Back.
type View interface {
	tea.Model
	Title() string
}

// Check is one line of nhm doctor.
type Check struct {
	Name   string
	Result string // ok, warn or fail
	Detail string
	Fix    string
}

// Text loads a page of text: what the CLI verb prints.
type Text func(ctx context.Context) (string, error)

// Env is what the views read. A nil loader makes its view say that it is
// not available in this menu.
type Env struct {
	Doctor        func(ctx context.Context) ([]Check, error)
	Health        Text
	Capabilities  Text
	Version       Text
	Compatibility Text
	// Recover is nhm admin recover inspect: the operations with no final
	// outcome, and displaced and orphan bindings.
	Recover  Text
	Feedback Text
	// Status, Attention: nhm status and nhm attention, every workspace.
	Status    Text
	Attention Text
	// Usage is nhm quota, then nhm limits --dry-run (nothing recorded).
	Usage Text
	// Read is one agent's recent screen. It refuses a bound target whose
	// binding no longer holds the target's generation: the view never
	// shows another occupant.
	Read func(ctx context.Context, t ui.Target) (Screen, error)
	// Inbox is one agent's unread mail, read with --keep-unread.
	Inbox func(ctx context.Context, t ui.Target) (string, error)
	// RunsDir is the instance's ensemble runs directory; "" when the menu
	// has no instance.
	RunsDir string
	// RunStatus is nhm ensemble status for one run.
	RunStatus func(ctx context.Context, id string) (string, error)
	// Timeout bounds each load; 0 is DefaultTimeout.
	Timeout time.Duration
	// FollowEvery is how often a following read view reads again; 0 is
	// one second.
	FollowEvery time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Screen is what Read gives: the agent's name and pane, and its text.
type Screen struct {
	Name string
	Pane string
	Text string
}

// DefaultTimeout bounds a load. nhm doctor bounds its own checks to 20 s.
const DefaultTimeout = 25 * time.Second

func (e Env) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return DefaultTimeout
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// IDs are the action table's rows (internal/ui/actions) that New opens.
var IDs = []string{
	"diag.doctor", "diag.health", "diag.capabilities", "diag.version", "diag.compatibility", "diag.recover",
	"view.modes", "view.ensembles", "view.feedback", "view.status", "view.attention", "view.usage",
}

// ForIDs are the rows that NewFor opens: each takes one agent.
var ForIDs = []string{"agent.read", "agent.inbox"}

// NewFor returns the view of a row that takes one agent, for the frozen
// target t, or false for any other row.
func NewFor(id string, env Env, t ui.Target) (View, bool) {
	switch id {
	case "agent.read":
		return newRead(env, t), true
	case "agent.inbox":
		var fn Text
		if env.Inbox != nil {
			fn = func(ctx context.Context) (string, error) { return env.Inbox(ctx, t) }
		}
		p := newPage("inbox", env, cleaned(fn))
		p.empty = "no unread mail"
		return p, true
	}
	return nil, false
}

// New returns the view of a menu row, or false for a row that is not a
// view of this package.
func New(id string, env Env) (View, bool) {
	switch id {
	case "diag.doctor":
		var fn Text
		if env.Doctor != nil {
			fn = func(ctx context.Context) (string, error) {
				checks, err := env.Doctor(ctx)
				return renderChecks(checks), err
			}
		}
		return newPage("doctor", env, fn), true
	case "diag.health":
		return newPage("health", env, cleaned(env.Health)), true
	case "diag.capabilities":
		return newPage("capabilities", env, cleaned(env.Capabilities)), true
	case "diag.version":
		return newPage("version", env, cleaned(env.Version)), true
	case "diag.compatibility":
		return newPage("compatibility", env, cleaned(env.Compatibility)), true
	case "diag.recover":
		p := newPage("unfinished operations", env, cleaned(env.Recover))
		p.empty = "nothing to recover: no bindings and no unfinished operations"
		return p, true
	case "view.feedback":
		return newPage("feedback", env, cleaned(env.Feedback)), true
	case "view.status":
		return newPage("status", env, cleaned(env.Status)), true
	case "view.attention":
		p := newPage("attention", env, cleaned(env.Attention))
		p.empty = "nothing needs a decision"
		return p, true
	case "view.usage":
		return newPage("usage", env, cleaned(env.Usage)), true
	case "view.modes":
		return newModes(env), true
	case "view.ensembles":
		return newEnsembles(env), true
	}
	return nil, false
}

// lastRequest numbers the loads of every view in the process, so an
// answer to a view that was closed never matches a newer one.
var lastRequest atomic.Uint64

func nextRequest() uint64 { return lastRequest.Add(1) }

// loaded is the answer to one load.
type loaded struct {
	id    uint64
	value any
	err   error
}

// load runs fn in a tea.Cmd under a deadline. fn may ignore ctx (a CLI
// verb run in process): the answer then says that the load timed out, and
// fn's late result is dropped. A panic in fn is an error, not a crash.
func load(id uint64, timeout time.Duration, fn func(ctx context.Context) (any, error)) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		done := make(chan loaded, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- loaded{id: id, err: fmt.Errorf("the load failed: %v", r)}
				}
			}()
			v, err := fn(ctx)
			done <- loaded{id: id, value: v, err: err}
		}()
		select {
		case l := <-done:
			return l
		case <-ctx.Done():
			return loaded{id: id, err: fmt.Errorf("no answer in %s", timeout)}
		}
	}
}

// clean makes outside text safe to show: no terminal controls, and tabs
// as spaces (the viewport counts a tab as one column).
func clean(s string) string {
	return strings.ReplaceAll(board.Clean(s), "\t", "    ")
}

// wrap breaks text at width, at a space where it can.
func wrap(s string, width int) string {
	if width < 1 {
		return s
	}
	return ansi.Wrap(s, width, "")
}

// isBack reports whether a key leaves the view (or its detail page).
func isBack(k tea.KeyMsg) bool {
	switch k.String() {
	case "esc", "q", "backspace", "left":
		return true
	}
	return false
}

func backCmd() tea.Msg { return Back{} }
