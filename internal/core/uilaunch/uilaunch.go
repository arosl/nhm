// Package uilaunch records the launches of nhm's board, so that nhm finds
// its board again by identity, never by label (plan-v4 section 7,
// "Finding the board again").
//
// One record per workspace, <instance>/ui/board-<workspace>.json, read
// and written under the workspace lock:
//
//  1. Open (the prefix+d action) verifies the record. A live board is
//     focused. A launch that has not registered yet, within StartGrace,
//     is not opened twice. Anything else is dropped, and Open records a
//     new token ("opening") before it asks herdr for the tab.
//  2. Register (the board, at its start) finds the record by its token
//     and adds its process identity: pid, the pid's start time, its pane.
//  3. Verify says a record is live only when its pane is in the workspace
//     and the pane's shell is the registered process (pid and start
//     time). A pane label or tab name is never evidence.
//  4. Unregister (the board, at its end) removes the record if it is
//     still the board's own.
package uilaunch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// Dir is the instance directory's UI state.
const Dir = "ui"

// Record states.
const (
	Opening    = "opening"
	Registered = "registered"
)

// Verify results.
const (
	// Live: the record's pane holds the registered board process.
	Live = "live"
	// Starting: a launch that has not registered yet, within StartGrace.
	Starting = "starting"
	// Gone: the board is not there (closed, restarted, never started).
	Gone = "gone"
)

// StartGrace is how long a launch may take to register before nhm
// counts it as failed and may open another board.
const StartGrace = 15 * time.Second

// TokenEnv carries the launch token into the board's pane.
const TokenEnv = "NHM_BOARD_TOKEN"

// PluginID is nhm's herdr plugin (internal/ui's PluginID; core does not
// import the UI).
const PluginID = "nhm"

// Errors.
var (
	// ErrSuperseded: the record holds another launch (a newer one, or
	// none): this board is not the workspace's registered board.
	ErrSuperseded = errors.New("uilaunch: this board's launch is no longer the workspace's board")
	// ErrWrongInstance: the board was launched for another nhm instance.
	ErrWrongInstance = errors.New("uilaunch: the board was launched for another herdr instance")
	// ErrNoWorkspace: herdr has no such workspace.
	ErrNoWorkspace = errors.New("uilaunch: herdr has no such workspace")
)

// Launch is one board launch: the record in its workspace's file.
type Launch struct {
	Token       string    `json:"token"`
	InstanceID  string    `json:"instance_id"`
	WorkspaceID string    `json:"workspace_id"`
	State       string    `json:"state"`
	OpenedAt    time.Time `json:"opened_at"`
	// PaneID is the pane plugin.pane.open answered with, or the one the
	// board found for itself; "" while it is not known.
	PaneID string `json:"pane_id,omitempty"`
	// PID and PIDStart are the board process, from its registration;
	// PIDStart is /proc/<pid>/stat's starttime (0: not readable).
	PID          int        `json:"pid,omitempty"`
	PIDStart     uint64     `json:"pid_start,omitempty"`
	RegisteredAt *time.Time `json:"registered_at,omitempty"`
}

// Herdr is what verification needs; *herdr.Client has it.
type Herdr interface {
	Snapshot(ctx context.Context) (herdr.SessionSnapshot, error)
	PaneProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
}

// Opener opens and focuses the board's tab.
type Opener interface {
	Herdr
	PluginPaneOpen(ctx context.Context, p herdr.PluginPaneOpenParams) (*herdr.PluginPane, error)
	PluginPaneFocus(ctx context.Context, paneID string) error
}

// Env is an instance and its herdr server.
type Env struct {
	InstanceDir string
	InstanceID  string
	Now         func() time.Time
	// ProcStart reads a process's start time; nil reads /proc.
	ProcStart func(pid int) (uint64, error)
	// LockWait bounds the wait for the workspace lock (default 5 s).
	LockWait time.Duration
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e Env) procStart(pid int) (uint64, error) {
	if e.ProcStart != nil {
		return e.ProcStart(pid)
	}
	return ProcStart(pid)
}

// locked runs fn under the workspace lock.
func (e Env) locked(ctx context.Context, workspaceID string, fn func() error) error {
	wait := e.LockWait
	if wait <= 0 {
		wait = 5 * time.Second
	}
	lctx, cancel := context.WithTimeout(ctx, wait)
	unlock, err := store.LockWorkspace(lctx, e.InstanceDir, workspaceID)
	cancel()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (e Env) path(workspaceID string) string {
	return filepath.Join(e.InstanceDir, Dir, "board-"+workspaceID+".json")
}

// read reads a workspace's record; nil when there is none.
func (e Env) read(workspaceID string) (*Launch, error) {
	data, err := os.ReadFile(e.path(workspaceID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("uilaunch: %w", err)
	}
	var l Launch
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("uilaunch: %s: %w", e.path(workspaceID), err)
	}
	return &l, nil
}

func (e Env) write(l Launch) error {
	dir := filepath.Join(e.InstanceDir, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("uilaunch: %w", err)
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(e.path(l.WorkspaceID), append(data, '\n'), 0o600)
}

func (e Env) remove(workspaceID string) error {
	if err := os.Remove(e.path(workspaceID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("uilaunch: %w", err)
	}
	return nil
}

// Status is what Verify found.
type Status struct {
	State  string `json:"state"` // live, starting, gone
	Reason string `json:"reason,omitempty"`
}

// Verify checks a record against herdr. An error means herdr could not
// be asked: the board's state is then uncertain, not gone.
func Verify(ctx context.Context, env Env, h Herdr, l Launch) (Status, error) {
	if l.State == Opening && env.now().Sub(l.OpenedAt) >= StartGrace {
		return Status{State: Gone, Reason: "the launch never registered"}, nil
	}
	switch {
	case l.State != Registered && l.State != Opening, l.State == Registered && (l.PID <= 0 || l.PaneID == ""):
		return Status{State: Gone, Reason: "the record is incomplete"}, nil
	}
	if l.State == Opening && l.PaneID == "" {
		// herdr's answer was lost: nothing to check yet.
		return Status{State: Starting, Reason: "opened, and herdr's answer was lost"}, nil
	}
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("uilaunch: herdr: %w", err)
	}
	inWorkspace := false
	for _, p := range snap.Panes {
		if p.PaneID == l.PaneID {
			if p.WorkspaceID != l.WorkspaceID {
				return Status{State: Gone, Reason: "its pane is in another workspace now"}, nil
			}
			inWorkspace = true
		}
	}
	if !inWorkspace {
		return Status{State: Gone, Reason: "its pane is closed"}, nil
	}
	if l.State == Opening {
		age := env.now().Sub(l.OpenedAt).Round(time.Second)
		return Status{State: Starting, Reason: fmt.Sprintf("opened %s ago and not registered yet", age)}, nil
	}
	info, err := h.PaneProcessInfo(ctx, l.PaneID)
	if err != nil {
		return Status{}, fmt.Errorf("uilaunch: herdr: %w", err)
	}
	if info.ShellPID == nil || *info.ShellPID != l.PID {
		// After a server restart herdr gives the pane back with a plain
		// shell in it: the pane id is the same, the process is not.
		return Status{State: Gone, Reason: "its pane runs another process"}, nil
	}
	if l.PIDStart != 0 {
		start, err := env.procStart(l.PID)
		if err != nil || start != l.PIDStart {
			return Status{State: Gone, Reason: "its process id now names another process"}, nil
		}
	}
	return Status{State: Live}, nil
}

// OpenRequest is a prefix+d in a workspace.
type OpenRequest struct {
	WorkspaceID string
	// Entrypoint is the plugin pane that runs the board (default
	// "board").
	Entrypoint string
	// Env is the board pane's environment (the captured context); Open
	// adds TokenEnv.
	Env map[string]string
	Cwd string
}

// Open actions.
const (
	Focused  = "focused"
	Opened   = "opened"
	Awaiting = "awaiting" // a launch is starting: nhm waits for it, it opens no second board
)

// OpenResult is what Open did.
type OpenResult struct {
	Action string `json:"action"`
	PaneID string `json:"pane_id,omitempty"`
	Token  string `json:"token,omitempty"`
	// Dropped says why an earlier record was dropped; StalePane is its
	// pane when that pane is still open (a restored shell, say): nhm
	// leaves it for the user to close.
	Dropped   string `json:"dropped,omitempty"`
	StalePane string `json:"stale_pane,omitempty"`
	// OpenedAt is when the launch that is starting was opened (Awaiting).
	OpenedAt *time.Time `json:"opened_at,omitempty"`
}

// Open focuses the workspace's live board, or opens a new board tab. It
// never opens a second board while one is live or starting, and when
// herdr cannot say whether the board is live, it refuses.
func Open(ctx context.Context, env Env, h Opener, req OpenRequest) (OpenResult, error) {
	var res OpenResult
	err := env.locked(ctx, req.WorkspaceID, func() error {
		old, err := env.read(req.WorkspaceID)
		if err != nil {
			return err
		}
		if old != nil {
			st, err := Verify(ctx, env, h, *old)
			if err != nil {
				return fmt.Errorf("%w; nhm opens no second board while it cannot check the first", err)
			}
			switch st.State {
			case Live:
				if err := h.PluginPaneFocus(ctx, old.PaneID); err != nil {
					return fmt.Errorf("uilaunch: focus the board: %w", err)
				}
				res = OpenResult{Action: Focused, PaneID: old.PaneID, Token: old.Token}
				return nil
			case Starting:
				// herdr's own answer named the pane: it is this launch's,
				// so it can be focused before the board registers.
				if old.PaneID != "" {
					if err := h.PluginPaneFocus(ctx, old.PaneID); err != nil {
						return fmt.Errorf("uilaunch: focus the starting board: %w", err)
					}
				}
				at := old.OpenedAt
				res = OpenResult{Action: Awaiting, PaneID: old.PaneID, Token: old.Token, OpenedAt: &at}
				return nil
			}
			res.Dropped = st.Reason
			if old.PaneID != "" && paneOpen(ctx, h, old.PaneID) {
				res.StalePane = old.PaneID
			}
		}
		token, err := newToken()
		if err != nil {
			return err
		}
		l := Launch{Token: token, InstanceID: env.InstanceID, WorkspaceID: req.WorkspaceID, State: Opening, OpenedAt: env.now()}
		if err := env.write(l); err != nil {
			return err
		}
		penv := map[string]string{}
		for k, v := range req.Env {
			penv[k] = v
		}
		penv[TokenEnv] = token
		entry := req.Entrypoint
		if entry == "" {
			entry = "board"
		}
		focus := true
		pane, err := h.PluginPaneOpen(ctx, herdr.PluginPaneOpenParams{PluginID: PluginID, Entrypoint: entry, Placement: "tab",
			WorkspaceID: req.WorkspaceID, Cwd: req.Cwd, Env: penv, Focus: &focus})
		if err != nil {
			if herdr.ErrorCode(err) != "" {
				// herdr refused: no pane opened.
				_ = env.remove(req.WorkspaceID)
				return fmt.Errorf("uilaunch: open the board: %w", err)
			}
			// No answer: the tab may be open. The record stays "opening",
			// so a board that did start can still register, and the next
			// prefix+d waits for it up to StartGrace.
			return fmt.Errorf("uilaunch: open the board (uncertain, the next prefix+d checks again): %w", err)
		}
		l.PaneID = pane.Pane.PaneID
		if err := env.write(l); err != nil {
			return err
		}
		res.Action, res.PaneID, res.Token = Opened, l.PaneID, token
		return nil
	})
	return res, err
}

func paneOpen(ctx context.Context, h Herdr, paneID string) bool {
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return false
	}
	for _, p := range snap.Panes {
		if p.PaneID == paneID {
			return true
		}
	}
	return false
}

// Self is the board process that registers.
type Self struct {
	PID int
	// PaneID is the board's pane when it knows it (HERDR_PANE_ID); ""
	// lets Register find the pane whose shell is PID.
	PaneID string
}

// Register records the board process for its token. It refuses a board
// launched for another instance, and a token that is no longer the
// workspace's record (ErrSuperseded). It checks that the workspace
// exists: a board never falls back to the focused workspace.
func Register(ctx context.Context, env Env, h Herdr, workspaceID, token string, self Self) (Launch, error) {
	var out Launch
	err := env.locked(ctx, workspaceID, func() error {
		l, err := env.read(workspaceID)
		if err != nil {
			return err
		}
		if l == nil || l.Token != token {
			return ErrSuperseded
		}
		if l.InstanceID != env.InstanceID {
			return fmt.Errorf("%w (%s, this server is %s)", ErrWrongInstance, l.InstanceID, env.InstanceID)
		}
		if l.State == Registered && l.PID != self.PID {
			return ErrSuperseded
		}
		snap, err := h.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("uilaunch: herdr: %w", err)
		}
		if !hasWorkspace(snap, workspaceID) {
			return fmt.Errorf("%w: %s", ErrNoWorkspace, workspaceID)
		}
		pane := self.PaneID
		if pane == "" {
			if pane, err = findPane(ctx, h, snap, workspaceID, self.PID); err != nil {
				return err
			}
		}
		if pane == "" {
			pane = l.PaneID
		}
		if pane == "" {
			return errors.New("uilaunch: the board cannot find its own pane: no pane of the workspace runs it")
		}
		if l.PaneID != "" && pane != l.PaneID {
			return fmt.Errorf("%w: herdr opened pane %s, the board runs in %s", ErrSuperseded, l.PaneID, pane)
		}
		start, _ := env.procStart(self.PID)
		at := env.now()
		l.State, l.PaneID, l.PID, l.PIDStart, l.RegisteredAt = Registered, pane, self.PID, start, &at
		if err := env.write(*l); err != nil {
			return err
		}
		out = *l
		return nil
	})
	return out, err
}

func hasWorkspace(snap herdr.SessionSnapshot, id string) bool {
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == id {
			return true
		}
	}
	return false
}

// findPane finds the workspace's pane whose shell is pid: a plugin pane's
// shell is the program herdr started in it.
func findPane(ctx context.Context, h Herdr, snap herdr.SessionSnapshot, workspaceID string, pid int) (string, error) {
	for _, p := range snap.Panes {
		if p.WorkspaceID != workspaceID {
			continue
		}
		info, err := h.PaneProcessInfo(ctx, p.PaneID)
		if err != nil {
			return "", fmt.Errorf("uilaunch: herdr: %w", err)
		}
		if info.ShellPID != nil && *info.ShellPID == pid {
			return p.PaneID, nil
		}
	}
	return "", nil
}

// Unregister removes the workspace's record when it is still this
// board's (its token).
func Unregister(ctx context.Context, env Env, workspaceID, token string) error {
	return env.locked(ctx, workspaceID, func() error {
		l, err := env.read(workspaceID)
		if err != nil || l == nil || l.Token != token {
			return err
		}
		return env.remove(workspaceID)
	})
}

// Boards lists the live boards of the instance, by pane id: nhm's own
// panes, which agent counts, targets, adopt, maintain and kill's review
// leave out. A record that cannot be checked is left out of the list
// and named in the error.
func Boards(ctx context.Context, env Env, h Herdr) (map[string]Launch, error) {
	entries, err := os.ReadDir(filepath.Join(env.InstanceDir, Dir))
	if os.IsNotExist(err) {
		return map[string]Launch{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("uilaunch: %w", err)
	}
	out := map[string]Launch{}
	var failed []string
	for _, e := range entries {
		ws, ok := strings.CutPrefix(e.Name(), "board-")
		if !ok {
			continue
		}
		ws, ok = strings.CutSuffix(ws, ".json")
		if !ok || !store.ValidWorkspaceID(ws) {
			continue
		}
		l, err := env.read(ws)
		if err != nil || l == nil {
			if err != nil {
				failed = append(failed, err.Error())
			}
			continue
		}
		st, err := Verify(ctx, env, h, *l)
		if err != nil {
			failed = append(failed, ws+": "+err.Error())
			continue
		}
		if st.State == Live {
			out[l.PaneID] = *l
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return out, errors.New(strings.Join(failed, "; "))
	}
	return out, nil
}

func newToken() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("uilaunch: token: %w", err)
	}
	return "bl-" + hex.EncodeToString(b), nil
}

// ProcStart reads a process's start time, in clock ticks after boot, from
// /proc/<pid>/stat (field 22). A pid that is reused gets a new start
// time.
func ProcStart(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	// Field 2, the command, is in parentheses and may hold spaces: the
	// fields after it start after its last ')'.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("uilaunch: /proc/%d/stat: no command field", pid)
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); field 22 is f[19].
	if len(f) < 20 {
		return 0, fmt.Errorf("uilaunch: /proc/%d/stat: %d fields", pid, len(f)+2)
	}
	return strconv.ParseUint(f[19], 10, 64)
}
