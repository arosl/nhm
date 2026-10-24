package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// sessionsInstance is an instance with two workspaces, alpha (focused)
// and beta, and one agent bound in beta for the project dir.
func sessionsInstance(t *testing.T, dir string) *instanceEnv {
	t.Helper()
	s, inst := cliInstance(t)
	t.Setenv("HERDR_WORKSPACE_ID", "")
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		ws := func(id, label string, focused bool) map[string]any {
			return map[string]any{"workspace_id": id, "number": 1, "label": label, "focused": focused,
				"pane_count": 1, "tab_count": 1, "active_tab_id": id + ":t1", "agent_status": "idle"}
		}
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "agents": []any{},
			"workspaces": []any{ws("w1", "alpha", true), ws("w2", "beta", false)},
			"panes": []any{map[string]any{"pane_id": "w2:p5", "tab_id": "w2:t1", "workspace_id": "w2", "terminal_id": "t5",
				"agent_status": "idle", "focused": false, "revision": 1}},
		}}, nil
	})
	// herdr names the pane it was asked about: no pane has moved.
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID}}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: dir,
		ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w2", TabID: "w2:t1",
			PaneID: "w2:p9", HerdrAlias: "icyknoll", Kind: "claude"}}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ie.db.Close() })
	return ie
}

func TestResolveSessionOnHerdr(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ie := sessionsInstance(t, dir)
	ctx := context.Background()
	var out, errOut bytes.Buffer

	// A prefix, said on no line: the session was named.
	r, err := resolveSession(ctx, ie, "be", false, &out, &errOut)
	if err != nil || r.Session != "beta" || r.WorkspaceID != "w2" || errOut.Len() != 0 {
		t.Fatalf("prefix: %+v, %v, stderr %q", r, err, errOut.String())
	}

	// No name, outside the projects: two sessions and no terminal.
	t.Chdir(t.TempDir())
	if _, err := resolveSession(ctx, ie, "", false, &out, &errOut); err == nil ||
		!strings.HasPrefix(err.Error(), "session name required: 2 sessions are running") ||
		!strings.HasSuffix(err.Error(), "Available: alpha, beta") {
		t.Fatalf("no name: %v", err)
	}

	// The cwd in the bound agent's project picks its workspace, said on
	// stderr; with JSON nothing is said.
	t.Chdir(dir)
	r, err = resolveSession(ctx, ie, "", false, &out, &errOut)
	if err != nil || r.WorkspaceID != "w2" || errOut.String() != "Using session \"beta\" (current directory)\n" {
		t.Fatalf("cwd: %+v, %v, stderr %q", r, err, errOut.String())
	}
	errOut.Reset()
	if r, err = resolveSession(ctx, ie, "", true, &out, &errOut); err != nil || r.WorkspaceID != "w2" || errOut.Len() != 0 {
		t.Fatalf("cwd, JSON: %+v, %v, stderr %q", r, err, errOut.String())
	}

	// The calling pane's live workspace comes first, before
	// HERDR_WORKSPACE_ID (the pane may have moved).
	t.Chdir(t.TempDir())
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	t.Setenv("HERDR_PANE_ID", "w2:p5")
	if r, err = resolveSession(ctx, ie, "", false, &out, &errOut); err != nil || r.WorkspaceID != "w2" || r.Reason != "current herdr workspace" {
		t.Fatalf("current pane: %+v, %v", r, err)
	}
	t.Setenv("HERDR_PANE_ID", "")
	if r, err = resolveSession(ctx, ie, "", false, &out, &errOut); err != nil || r.WorkspaceID != "w1" {
		t.Fatalf("HERDR_WORKSPACE_ID: %+v, %v", r, err)
	}
}
