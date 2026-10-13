// Package ui opens nhm's popups in herdr (plan-v8 2.1, 2.4).
//
// A plugin action (herdr runs `nhm ui palette`, say) captures the context
// it was invoked in: the herdr socket, the workspace, the pane and its
// directory, from HERDR_PLUGIN_CONTEXT_JSON and herdr's environment. It
// opens the popup with plugin.pane.open and hands that context over in
// NHM_CONTEXT. The popup acts on the captured ids only: a focus change
// later never retargets it, and with two clients attached its header
// names what it acts on.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
)

// PluginID is the id of nhm's herdr plugin.
const PluginID = "nhm"

// Popups are the popup panes the plugin declares, by entrypoint.
var Popups = []string{"palette", "board", "menu", "spawn"}

// ContextEnv is the variable that carries the captured context into a
// popup.
const ContextEnv = "NHM_CONTEXT"

// ContextVersion is the version of the context this nhm writes. A popup
// refuses a context from a newer nhm. A context without a version (nhm
// 0.1.5 and before) is version 0: the same fields, and no targets.
const ContextVersion = 1

// Context is what an action captured when it was invoked, and what one
// surface hands to the next (plan-v4 section 4). It names the nhm
// instance, the workspace, the server-side directory, the origin and the
// selection. A surface acts on these ids only: a focus change never
// retargets it, and nothing falls back to the focused workspace or to the
// plugin's directory.
type Context struct {
	Version int `json:"version"`
	// InstanceID is the nhm instance enrolled for the herdr server at
	// SocketPath, when the action found one.
	InstanceID     string `json:"instance_id,omitempty"`
	SocketPath     string `json:"socket_path"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	// The origin: the tab and pane the action was invoked over, the agent
	// herdr saw there, herdr's invocation source ("pane" for a verb typed
	// in a pane), and Origin, the nhm surface ("board", "menu",
	// "palette") that handed the context on, or "".
	TabID  string `json:"tab_id,omitempty"`
	PaneID string `json:"pane_id,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Source string `json:"invocation_source,omitempty"`
	Origin string `json:"origin,omitempty"`
	// Cwd is the server-side directory the verbs run in: the focused
	// pane's, else the workspace's. Capture keeps only an absolute path to
	// a directory that exists; "" is none.
	Cwd string `json:"cwd,omitempty"`
	// TargetMode and Targets are the selection: see Selection.
	TargetMode string   `json:"target_mode,omitempty"`
	Targets    []Target `json:"targets,omitempty"`
}

// Selection modes.
const (
	// TargetsNotGiven: no selection was given. An action that needs
	// targets opens the agent picker; it never means "all agents".
	TargetsNotGiven = ""
	// TargetsExplicit: Targets is the selection, frozen when it was made.
	// An empty one is refused.
	TargetsExplicit = "explicit"
)

// Target is one frozen target (plan-v4 section 4). A bound agent is its
// binding at the generation the UI saw: the core refuses it once the
// binding has another generation (send's and interrupt's Expect, retire's
// and recycle's ExpectedGeneration). An agent with no binding (an
// adoption candidate) is its pane and its agent session. PaneID and Kind
// of a bound target only describe it.
type Target struct {
	BindingID  string `json:"binding_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	PaneID     string `json:"pane_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Session    string `json:"session,omitempty"`
}

// Bound reports whether the target is a binding.
func (t Target) Bound() bool { return t.BindingID != "" }

// Key is the target's identity: two targets with the same key are the
// same agent at the same generation.
func (t Target) Key() string {
	if t.Bound() {
		return "binding:" + t.BindingID + "@" + strconv.FormatInt(t.Generation, 10)
	}
	return "pane:" + t.PaneID + "#" + t.Session
}

// check reports what makes a target unusable.
func (t Target) check() error {
	switch {
	case t.Bound() && t.Generation < 1:
		return fmt.Errorf("ui: target %s has no generation", t.BindingID)
	case !t.Bound() && t.PaneID == "":
		return errors.New("ui: a target names neither a binding nor a pane")
	}
	return nil
}

// Expect is the bound targets' generations by binding id: the map that
// send and interrupt take to refuse a target that changed.
func Expect(ts []Target) map[string]int64 {
	m := map[string]int64{}
	for _, t := range ts {
		if t.Bound() {
			m[t.BindingID] = t.Generation
		}
	}
	return m
}

// Errors of a context.
var (
	// ErrNoContext means neither herdr nor the caller named a herdr socket.
	ErrNoContext = errors.New("ui: no herdr context: run it from herdr (a plugin action or a pane), or give --socket")
	// ErrEmptySelection: the selection was given and is empty.
	ErrEmptySelection = errors.New("ui: the selection is empty: pick at least one agent")
	// ErrNewerContext: a newer nhm wrote the context.
	ErrNewerContext = errors.New("ui: the context comes from a newer nhm: close this and open it again")
)

// Capture reads an action's context: HERDR_PLUGIN_CONTEXT_JSON when herdr
// ran the action, else the pane's own HERDR_* variables (a verb typed in
// a pane). getenv is os.Getenv in production. It has no selection; the
// caller fills InstanceID.
func Capture(getenv func(string) string) (Context, error) {
	c := Context{Version: ContextVersion, SocketPath: getenv("HERDR_SOCKET_PATH")}
	if raw := getenv("HERDR_PLUGIN_CONTEXT_JSON"); raw != "" {
		var pc herdr.InvocationContext
		if err := json.Unmarshal([]byte(raw), &pc); err != nil {
			return Context{}, fmt.Errorf("ui: HERDR_PLUGIN_CONTEXT_JSON: %w", err)
		}
		c.WorkspaceID, c.WorkspaceLabel, c.TabID = pc.WorkspaceID, pc.WorkspaceLabel, pc.TabID
		c.PaneID, c.Agent, c.Source = pc.FocusedPaneID, pc.FocusedPaneAgent, pc.InvocationSource
		// herdr runs the action in the plugin's directory: the pane's or
		// the workspace's directory, never the process's own.
		c.Cwd = firstDir(pc.FocusedPaneCwd, pc.WorkspaceCwd)
	} else {
		c.WorkspaceID, c.TabID, c.PaneID = getenv("HERDR_WORKSPACE_ID"), getenv("HERDR_TAB_ID"), getenv("HERDR_PANE_ID")
		wd, _ := os.Getwd()
		c.Cwd = firstDir(wd)
		c.Source = "pane"
	}
	if c.SocketPath == "" {
		return Context{}, ErrNoContext
	}
	return c, nil
}

// firstDir is the first of dirs that is an absolute path to a directory,
// or "".
func firstDir(dirs ...string) string {
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			continue
		}
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return filepath.Clean(d)
		}
	}
	return ""
}

// FromPopup reads the context a popup was opened with; ok is false when
// it was not opened by an action (a verb run by hand). A context that
// fails Check is an error.
func FromPopup(getenv func(string) string) (Context, bool, error) {
	raw := getenv(ContextEnv)
	if raw == "" {
		return Context{}, false, nil
	}
	var c Context
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Context{}, false, fmt.Errorf("ui: %s: %w", ContextEnv, err)
	}
	if err := c.Check(); err != nil {
		return Context{}, false, fmt.Errorf("ui: %s: %w", ContextEnv, err)
	}
	return c, true, nil
}

// Check refuses a context this nhm cannot act on safely: one from a newer
// nhm, a relative directory, an unknown selection mode, targets without
// the explicit mode, or a target that is incomplete or given twice.
func (c Context) Check() error {
	if c.Version > ContextVersion {
		return fmt.Errorf("%w (version %d; this nhm reads %d)", ErrNewerContext, c.Version, ContextVersion)
	}
	if c.Version < 0 {
		return fmt.Errorf("ui: context version %d", c.Version)
	}
	if c.Cwd != "" && !filepath.IsAbs(c.Cwd) {
		return fmt.Errorf("ui: the directory %q is not absolute", c.Cwd)
	}
	switch c.TargetMode {
	case TargetsNotGiven:
		if len(c.Targets) > 0 {
			return errors.New("ui: targets without target_mode explicit")
		}
	case TargetsExplicit:
	default:
		return fmt.Errorf("ui: unknown target_mode %q", c.TargetMode)
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if err := t.check(); err != nil {
			return err
		}
		if seen[t.Key()] {
			return fmt.Errorf("ui: target %s given twice", t.Key())
		}
		seen[t.Key()] = true
	}
	return nil
}

// CheckInstance refuses a context captured for another nhm instance than
// id, the one enrolled for SocketPath now: its workspace and targets
// belong to that one. A context without an instance (version 0, or a
// server that was not enrolled) passes.
func (c Context) CheckInstance(id string) error {
	if c.InstanceID != "" && c.InstanceID != id {
		return fmt.Errorf("ui: the context belongs to nhm instance %s, and this herdr server's is %s now: close this and open it again", c.InstanceID, firstNonEmpty(id, "none"))
	}
	return nil
}

// WithTargets is c handed on by the surface origin with an explicit,
// frozen selection; an empty ts is a selection too, which Selection then
// refuses.
func (c Context) WithTargets(origin string, ts []Target) Context {
	c.Origin = origin
	c.TargetMode = TargetsExplicit
	c.Targets = nil
	if len(ts) > 0 {
		c.Targets = append([]Target{}, ts...)
	}
	return c
}

// Selection returns the selection. pick is true when none was given: the
// action opens the agent picker. An explicitly empty selection is
// ErrEmptySelection; neither ever means all agents.
func (c Context) Selection() (ts []Target, pick bool, err error) {
	if c.TargetMode != TargetsExplicit {
		return nil, true, nil
	}
	if len(c.Targets) == 0 {
		return nil, false, ErrEmptySelection
	}
	return append([]Target{}, c.Targets...), false, nil
}

// Header is the line a popup shows on top: what it acts on.
func (c Context) Header() string {
	ws := firstNonEmpty(c.WorkspaceLabel, c.WorkspaceID, "no workspace")
	head := "workspace " + ws
	if c.PaneID != "" {
		head += " · pane " + c.PaneID
	}
	if c.TargetMode == TargetsExplicit {
		head += fmt.Sprintf(" · %d picked", len(c.Targets))
	}
	return head
}

// Opener is what Open needs from herdr; *herdr.Client has it.
type Opener interface {
	PluginPaneOpen(ctx context.Context, p herdr.PluginPaneOpenParams) (*herdr.PluginPane, error)
}

// ErrBusy means herdr shows another popup, Settings or Copy mode, where
// no popup opens (herdr shows one popup at a time; checked on 0.9.3).
var ErrBusy = errors.New("ui: herdr already shows a popup, Settings or Copy mode; close it and try again")

// Open opens one of the plugin's popups with the captured context. A popup
// opens over the active pane: herdr refuses a target pane for it.
func Open(ctx context.Context, h Opener, entrypoint string, c Context) error {
	known := false
	for _, p := range Popups {
		known = known || p == entrypoint
	}
	if !known {
		return fmt.Errorf("ui: no popup %q", entrypoint)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = h.PluginPaneOpen(ctx, herdr.PluginPaneOpenParams{PluginID: PluginID, Entrypoint: entrypoint,
		Placement: "popup", Env: map[string]string{ContextEnv: string(data)}})
	if herdr.ErrorCode(err) == herdr.CodeUIBusy {
		return ErrBusy
	}
	return err
}

// Beside returns the path of a file that ships beside the running nhm
// binary in its release (nhm-palette, the plugin), or "" when there is
// none.
func Beside(name string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	p := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// userBinDirs are where ACFS installs the tools nhm runs (cm, cass, br,
// dcg, claude, codex), relative to HOME.
var userBinDirs = []string{".local/bin", ".cargo/bin", ".bun/bin"}

// PluginPath returns the PATH a process herdr's plugin started should
// use, and whether it differs from PATH. herdr runs plugin actions,
// popups and event hooks with the herdr server's environment, whose PATH
// may lack the user's tool directories: then cm and cass are "not
// installed" and a send loses its memory block (nhm-sbq). Outside a
// plugin context, or when every directory is there, PATH stays. isDir is
// a stat in production.
func PluginPath(getenv func(string) string, isDir func(string) bool) (string, bool) {
	path, home := getenv("PATH"), getenv("HOME")
	if home == "" || getenv("HERDR_PLUGIN_CONTEXT_JSON") == "" && getenv("HERDR_PLUGIN_EVENT_JSON") == "" && getenv(ContextEnv) == "" {
		return path, false
	}
	have := map[string]bool{}
	for _, d := range filepath.SplitList(path) {
		have[filepath.Clean(d)] = true
	}
	var add []string
	for _, rel := range userBinDirs {
		if d := filepath.Join(home, rel); !have[d] && isDir(d) {
			add = append(add, d)
		}
	}
	if len(add) == 0 {
		return path, false
	}
	if path != "" {
		add = append(add, path)
	}
	return strings.Join(add, string(filepath.ListSeparator)), true
}

// FixPluginPath sets PATH to PluginPath's result for this process and
// its children.
func FixPluginPath() {
	isDir := func(d string) bool { st, err := os.Stat(d); return err == nil && st.IsDir() }
	if p, changed := PluginPath(os.Getenv, isDir); changed {
		_ = os.Setenv("PATH", p)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
