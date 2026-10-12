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

	"github.com/arosl/nhm/internal/herdr"
)

// PluginID is the id of nhm's herdr plugin.
const PluginID = "nhm"

// Popups are the popup panes the plugin declares, by entrypoint.
var Popups = []string{"palette", "board", "menu", "spawn"}

// ContextEnv is the variable that carries the captured context into a
// popup.
const ContextEnv = "NHM_CONTEXT"

// Context is what an action captured when it was invoked.
type Context struct {
	SocketPath     string `json:"socket_path"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	TabID          string `json:"tab_id,omitempty"`
	PaneID         string `json:"pane_id,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Source         string `json:"invocation_source,omitempty"`
}

// ErrNoContext means neither herdr nor the caller named a herdr socket.
var ErrNoContext = errors.New("ui: no herdr context: run it from herdr (a plugin action or a pane), or give --socket")

// Capture reads an action's context: HERDR_PLUGIN_CONTEXT_JSON when herdr
// ran the action, else the pane's own HERDR_* variables (a verb typed in
// a pane). getenv is os.Getenv in production.
func Capture(getenv func(string) string) (Context, error) {
	c := Context{SocketPath: getenv("HERDR_SOCKET_PATH")}
	if raw := getenv("HERDR_PLUGIN_CONTEXT_JSON"); raw != "" {
		var pc herdr.InvocationContext
		if err := json.Unmarshal([]byte(raw), &pc); err != nil {
			return Context{}, fmt.Errorf("ui: HERDR_PLUGIN_CONTEXT_JSON: %w", err)
		}
		c.WorkspaceID, c.WorkspaceLabel, c.TabID = pc.WorkspaceID, pc.WorkspaceLabel, pc.TabID
		c.PaneID, c.Cwd, c.Agent, c.Source = pc.FocusedPaneID, firstNonEmpty(pc.FocusedPaneCwd, pc.WorkspaceCwd), pc.FocusedPaneAgent, pc.InvocationSource
	} else {
		c.WorkspaceID, c.TabID, c.PaneID = getenv("HERDR_WORKSPACE_ID"), getenv("HERDR_TAB_ID"), getenv("HERDR_PANE_ID")
		c.Cwd, _ = os.Getwd()
		c.Source = "pane"
	}
	if c.SocketPath == "" {
		return Context{}, ErrNoContext
	}
	return c, nil
}

// FromPopup reads the context a popup was opened with; ok is false when
// it was not opened by an action (a verb run by hand).
func FromPopup(getenv func(string) string) (Context, bool, error) {
	raw := getenv(ContextEnv)
	if raw == "" {
		return Context{}, false, nil
	}
	var c Context
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Context{}, false, fmt.Errorf("ui: %s: %w", ContextEnv, err)
	}
	return c, true, nil
}

// Header is the line a popup shows on top: what it acts on.
func (c Context) Header() string {
	ws := firstNonEmpty(c.WorkspaceLabel, c.WorkspaceID, "no workspace")
	if c.PaneID == "" {
		return "workspace " + ws
	}
	return fmt.Sprintf("workspace %s · pane %s", ws, c.PaneID)
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
