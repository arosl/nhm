package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/ui"
)

// nhm doctor --herdr-fit (nhm-qbi.11) checks again, against the running
// herdr, the facts of the X0 fit test (plan-v4 section 8; evidence
// x0.md) that nhm's interactive surfaces rely on. min_herdr_version is a
// lower bound only, so this check is what tells that a newer herdr still
// fits.

const (
	// fitCheckedHerdr is the herdr release that X0 checked.
	fitCheckedHerdr = "0.9.3"
	// fitMinHerdr is the plugin's min_herdr_version.
	fitMinHerdr = "0.9.3"
	// fitEntrypoint is the plugin pane that runs the probe.
	fitEntrypoint = "fit"
	checkSkip     = "skip"
)

// fitEnv is what the fit check uses; tests shorten the waits.
type fitEnv struct {
	h *herdr.Client
	// dir holds the probes' event files.
	dir string
	// wait bounds how long a probe has to start; poll is the interval.
	wait time.Duration
	poll time.Duration
}

// herdrFitChecks runs the fit check against the herdr server at socket.
// Without placements its only effect on the server is one throwaway
// popup. With placements it also opens the probe as a tab, a split and an
// overlay in a throwaway workspace, for the sandbox and CI: a live
// server's clients would see them.
func herdrFitChecks(ctx context.Context, socket string, placements bool) []doctorCheck {
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if socket == "" {
		return []doctorCheck{{Name: "herdr", Result: checkFail, Detail: "no herdr socket",
			Fix: "run nhm inside a herdr pane, or give --socket PATH"}}
	}
	dir, err := os.MkdirTemp("", "nhm-fit-")
	if err != nil {
		return []doctorCheck{{Name: "herdr fit", Result: checkFail, Detail: err.Error()}}
	}
	defer os.RemoveAll(dir)
	return fitEnv{h: herdr.New(socket), dir: dir, wait: 10 * time.Second, poll: 100 * time.Millisecond}.checks(ctx, placements)
}

func (e fitEnv) checks(ctx context.Context, placements bool) []doctorCheck {
	var checks []doctorCheck
	add := func(c doctorCheck) { checks = append(checks, c) }
	pong, err := e.h.Ping(ctx)
	if err != nil {
		add(doctorCheck{Name: "herdr protocol", Result: checkFail, Detail: err.Error(), Fix: "start the herdr server, or check the socket path"})
		return checks
	}
	if pong.Protocol == herdr.Protocol {
		add(doctorCheck{Name: "herdr protocol", Result: checkOK, Detail: fmt.Sprintf("herdr %s speaks protocol %d", pong.Version, pong.Protocol)})
	} else {
		add(doctorCheck{Name: "herdr protocol", Result: checkFail,
			Detail: fmt.Sprintf("herdr %s speaks protocol %d; this nhm speaks %d", pong.Version, pong.Protocol, herdr.Protocol),
			Fix:    "install the nhm release for this herdr, or the herdr release this nhm pins"})
		return checks
	}
	add(fitVersion(pong.Version))

	plugins, err := e.h.PluginList(ctx, ui.PluginID)
	plugin := fitPlugin(plugins, err)
	add(plugin)
	if plugin.Result != checkOK {
		return checks
	}
	add(e.popup(ctx))
	add(doctorCheck{Name: "popup keys", Result: checkSkip,
		Detail: "a popup has no pane id: only an attached client types into it (the sandbox E-step checks it)"})
	if !placements {
		add(doctorCheck{Name: "placements", Result: checkSkip,
			Detail: "--placements opens a tab, a split and an overlay (for the sandbox and CI: a live client sees them)"})
		return checks
	}
	return append(checks, e.placements(ctx)...)
}

// fitVersion compares herdr's version with the plugin's minimum and with
// the release X0 checked.
func fitVersion(v string) doctorCheck {
	c := doctorCheck{Name: "herdr version"}
	switch {
	case compareVersions(v, fitMinHerdr) < 0:
		c.Result, c.Detail = checkFail, fmt.Sprintf("herdr %s is older than nhm's plugin needs (%s)", v, fitMinHerdr)
		c.Fix = "update herdr"
	case compareVersions(v, fitCheckedHerdr) > 0:
		c.Result = checkWarn
		c.Detail = fmt.Sprintf("herdr %s is newer than the release X0 checked (%s): the checks below test it again", v, fitCheckedHerdr)
	default:
		c.Result, c.Detail = checkOK, fmt.Sprintf("herdr %s, the release X0 checked", v)
	}
	return c
}

// compareVersions compares dotted numeric versions ("0.9.3"); a
// non-numeric part counts as 0, and a pre-release suffix is ignored.
func compareVersions(a, b string) int {
	part := func(v string, i int) int {
		v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
		parts := strings.Split(v, ".")
		if i >= len(parts) {
			return 0
		}
		n, _ := strconv.Atoi(parts[i])
		return n
	}
	for i := range 3 {
		if x, y := part(a, i), part(b, i); x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// fitPlugin checks that nhm's plugin is linked and enabled and declares
// the probe's pane.
func fitPlugin(plugins []herdr.PluginInfo, err error) doctorCheck {
	c := doctorCheck{Name: "nhm plugin", Result: checkFail}
	if err != nil {
		c.Detail = err.Error()
		return c
	}
	i := slices.IndexFunc(plugins, func(p herdr.PluginInfo) bool { return p.PluginID == ui.PluginID })
	switch {
	case i < 0:
		c.Detail, c.Fix = "herdr has no plugin "+ui.PluginID, "link it: herdr plugin link ~/.local/share/nhm/plugin (ACFS's install does it)"
	case !plugins[i].Enabled:
		c.Detail, c.Fix = "nhm's plugin is disabled", "herdr plugin enable "+ui.PluginID
	case !slices.ContainsFunc(plugins[i].Panes, func(p herdr.PluginSpec) bool { return p.ID == fitEntrypoint }):
		c.Detail = fmt.Sprintf("nhm's plugin %s (%s) has no %q pane: it is older than this nhm", plugins[i].Version, plugins[i].PluginRoot, fitEntrypoint)
		c.Fix = "install this nhm's release, whose plugin directory has it"
	default:
		c.Result, c.Detail = checkOK, fmt.Sprintf("nhm %s, enabled, in %s", plugins[i].Version, plugins[i].PluginRoot)
	}
	return c
}

// fitRun is one probe that the check opened.
type fitRun struct {
	file  string
	nonce string
	pane  string // "" for a popup, which has no pane id
}

// open starts the probe in nhm's fit pane with placement p.
func (e fitEnv) open(ctx context.Context, p herdr.PluginPaneOpenParams) (fitRun, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fitRun{}, err
	}
	r := fitRun{nonce: hex.EncodeToString(b)}
	r.file = filepath.Join(e.dir, r.nonce+".jsonl")
	if err := os.WriteFile(r.file, nil, 0o600); err != nil {
		return r, err
	}
	no := false
	p.PluginID, p.Entrypoint, p.Focus = ui.PluginID, fitEntrypoint, &no
	p.Env = map[string]string{fitOutEnv: r.file, fitNonceEnv: r.nonce}
	pp, err := e.h.PluginPaneOpen(ctx, p)
	if pp != nil {
		r.pane = pp.Pane.PaneID
	}
	return r, err
}

// events reads what the probe wrote so far. A file it cannot read has no
// events yet: the caller's wait then ends with "did not start".
func (r fitRun) events() []fitEvent {
	f, err := os.Open(r.file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []fitEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev fitEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Nonce == r.nonce {
			out = append(out, ev)
		}
	}
	return out
}

// until polls the probe's events until done holds or the wait ends.
func (e fitEnv) until(ctx context.Context, r fitRun, d time.Duration, done func([]fitEvent) bool) ([]fitEvent, bool) {
	end := time.Now().Add(d)
	for {
		ev := r.events()
		if done(ev) {
			return ev, true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			return ev, false
		}
		time.Sleep(e.poll)
	}
}

// lastOf returns the newest event of a kind.
func lastOf(ev []fitEvent, kind string) (fitEvent, bool) {
	for i := len(ev) - 1; i >= 0; i-- {
		if ev[i].Event == kind {
			return ev[i], true
		}
	}
	return fitEvent{}, false
}

func started(ev []fitEvent) bool {
	_, s := lastOf(ev, "start")
	_, z := lastOf(ev, "size")
	return s && z
}

func exited(ev []fitEvent) bool {
	_, ok := lastOf(ev, "exit")
	return ok
}

// gone reports whether a process no longer exists.
func gone(pid int) bool {
	return pid > 0 && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// popup opens the throwaway popup, sees the probe start in it, and closes
// it with popup.close. It never closes a popup it did not open: when one
// is open already (ui_busy), it skips.
func (e fitEnv) popup(ctx context.Context) doctorCheck {
	c := doctorCheck{Name: "popup", Result: checkFail}
	r, err := e.open(ctx, herdr.PluginPaneOpenParams{Placement: "popup"})
	if herdr.ErrorCode(err) == herdr.CodeUIBusy {
		c.Result, c.Detail = checkSkip, "a popup is open already, and herdr shows one at a time; run this outside a popup"
		return c
	}
	if err != nil {
		c.Detail = "herdr did not open it: " + err.Error()
		return c
	}
	ev, ok := e.until(ctx, r, e.wait, started)
	if !ok {
		// The popup herdr opened is ours: one is open at a time.
		_ = e.h.PopupClose(ctx)
		c.Detail = fmt.Sprintf("the probe did not start in %s", e.wait)
		c.Fix = "herdr plugin log list --plugin nhm says why"
		return c
	}
	start, _ := lastOf(ev, "start")
	size, _ := lastOf(ev, "size")
	if err := e.h.PopupClose(ctx); err != nil {
		c.Detail = "popup.close: " + err.Error()
		return c
	}
	if _, ok := e.until(ctx, r, min(5*time.Second, e.wait), func(ev []fitEvent) bool { return exited(ev) || gone(start.PID) }); !ok {
		c.Detail = fmt.Sprintf("the probe (pid %d) still runs after popup.close", start.PID)
		return c
	}
	c.Result = checkOK
	c.Detail = fmt.Sprintf("a bubbletea program ran in a popup at %dx%d (%s, TERM=%s) and closed with popup.close",
		size.Width, size.Height, start.Profile, start.Term)
	return c
}

// placements opens the probe as a tab, a split and an overlay in a
// throwaway workspace. Each must start with a size, take keys through
// pane.send_keys, and close when the program ends. The tab also gives the
// kill guard's fact: a plugin pane's shell is its foreground process
// group, so nhm kill sees it as an idle shell (x0.md).
func (e fitEnv) placements(ctx context.Context) []doctorCheck {
	yes := true
	ws, err := e.h.WorkspaceCreate(ctx, herdr.WorkspaceCreateParams{Label: "nhm-fit", Focus: &yes})
	if err != nil || ws.Workspace == nil {
		return []doctorCheck{{Name: "placements", Result: checkFail, Detail: fmt.Sprintf("a throwaway workspace: %v", err)}}
	}
	wsID := ws.Workspace.WorkspaceID
	defer func() { _ = e.h.WorkspaceClose(context.WithoutCancel(ctx), wsID) }()
	var checks []doctorCheck
	for _, p := range []herdr.PluginPaneOpenParams{
		{Placement: "tab", WorkspaceID: wsID},
		{Placement: "split", TargetPaneID: ws.RootPane.PaneID, Direction: "right"},
		// An overlay covers the active pane: the new workspace's, which
		// has the focus.
		{Placement: "overlay"},
	} {
		c, pane := e.placement(ctx, p)
		var guard *doctorCheck
		if p.Placement == "tab" && pane != "" {
			g := e.killGuard(ctx, pane)
			guard = &g
		}
		// herdr closes a plugin pane when its program ends (x0.md).
		if closed := e.closeProbe(ctx, pane); c.Result == checkOK && closed {
			c.Detail += "; its pane closed when the program ended"
		} else if c.Result == checkOK {
			c.Result = checkWarn
			c.Detail += "; its pane stayed after the program ended, and nhm closed it"
		}
		checks = append(checks, c)
		if guard != nil {
			checks = append(checks, *guard)
		}
	}
	return checks
}

// placement runs the probe at one placement; it returns the check and the
// pane id, while the probe still runs (killGuard reads it).
func (e fitEnv) placement(ctx context.Context, p herdr.PluginPaneOpenParams) (doctorCheck, string) {
	c := doctorCheck{Name: p.Placement, Result: checkFail}
	r, err := e.open(ctx, p)
	if err != nil {
		c.Detail = "herdr did not open it: " + err.Error()
		return c, r.pane
	}
	if r.pane == "" {
		c.Detail = "herdr opened it without a pane id"
		return c, ""
	}
	ev, ok := e.until(ctx, r, e.wait, started)
	if !ok {
		c.Detail = fmt.Sprintf("the probe did not start in %s", e.wait)
		return c, r.pane
	}
	size, _ := lastOf(ev, "size")
	// One key per call, each seen before the next: esc then space in one
	// write is alt+space to any terminal program, as a person typing
	// alt+space would send it.
	var sent, want []string
	var got []fitEvent
	for i, k := range fitKeys {
		if err := e.h.PaneSendKeys(ctx, r.pane, k.herdr); err != nil {
			c.Detail = "pane.send_keys: " + err.Error()
			return c, r.pane
		}
		sent, want = append(sent, k.herdr), append(want, k.tea)
		got, _ = e.until(ctx, r, e.wait, func(ev []fitEvent) bool { return len(keysOf(ev)) > i })
	}
	if !slices.Equal(keysOf(got), want) {
		c.Detail = fmt.Sprintf("sent keys %q, the probe saw %q, not %q", sent, keysOf(got), want)
		return c, r.pane
	}
	c.Result = checkOK
	c.Detail = fmt.Sprintf("%s ran at %dx%d; keys %s arrived", r.pane, size.Width, size.Height, strings.Join(sent, ", "))
	return c, r.pane
}

// fitKeys are the keys a placement must pass: herdr's name, and what
// bubbletea calls the key it reads.
var fitKeys = []struct{ herdr, tea string }{{"a", "a"}, {"esc", "esc"}, {"space", " "}}

func keysOf(ev []fitEvent) []string {
	var out []string
	for _, e := range ev {
		if e.Event == "key" {
			out = append(out, e.Key)
		}
	}
	return out
}

// killGuard reads a plugin pane's processes as nhm kill's guard does
// (lifecycle's uncheckedPanes): the guard passes a pane whose shell is its
// foreground process group.
func (e fitEnv) killGuard(ctx context.Context, pane string) doctorCheck {
	c := doctorCheck{Name: "kill guard", Result: checkFail}
	pi, err := e.h.PaneProcessInfo(ctx, pane)
	switch {
	case err != nil:
		c.Detail = "pane.process_info: " + err.Error()
	case pi.ShellPID == nil || pi.ForegroundProcessGroupID == nil:
		c.Detail = fmt.Sprintf("%s has no shell pid or foreground group", pane)
	case *pi.ShellPID != *pi.ForegroundProcessGroupID:
		c.Detail = fmt.Sprintf("%s's shell (%d) is not its foreground group (%d): nhm kill would refuse a workspace with a board tab",
			pane, *pi.ShellPID, *pi.ForegroundProcessGroupID)
	default:
		c.Result = checkOK
		c.Detail = fmt.Sprintf("a plugin pane's program is its shell (pid %d): nhm kill sees an idle shell and closes it with the workspace", *pi.ShellPID)
	}
	return c
}

// closeProbe ends a probe with q and reports whether herdr closed its
// pane then; a pane that stays is closed with plugin.pane.close.
func (e fitEnv) closeProbe(ctx context.Context, pane string) bool {
	if pane == "" {
		return false
	}
	_ = e.h.PaneSendKeys(ctx, pane, "q")
	end := time.Now().Add(min(3*time.Second, e.wait))
	for {
		snap, err := e.h.Snapshot(ctx)
		if err == nil && !slices.ContainsFunc(snap.Panes, func(p herdr.PaneInfo) bool { return p.PaneID == pane }) {
			return true
		}
		if time.Now().After(end) || ctx.Err() != nil {
			break
		}
		time.Sleep(e.poll)
	}
	_ = e.h.PluginPaneClose(context.WithoutCancel(ctx), pane)
	return false
}
