package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// paneGetFor answers herdr's pane.get for the given panes, and
// pane_not_found for any other.
func paneGetFor(s *herdrtest.Server, panes ...string) {
	s.Handle("pane.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		for _, id := range panes {
			if id == p.PaneID {
				return map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": id, "tab_id": "w1:t1",
					"workspace_id": "w1", "terminal_id": "t-" + id, "agent_status": "idle", "focused": false, "revision": 1}}, nil
			}
		}
		return nil, &herdrtest.Error{Code: "pane_not_found", Message: "pane " + p.PaneID + " not found"}
	})
}

// nhm operator marks a pane as an operator's and retires its binding;
// --off clears the mark; --list shows the marks (nhm-h85k).
func TestOperatorVerb(t *testing.T) {
	s, inst := cliInstance(t)
	paneGetFor(s, "w1:p1", "w1:p2")
	ctx := context.Background()
	project := t.TempDir()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	bind := func(name string) bindings.Binding {
		b, err := bs.Create(ctx, bindings.New{MailName: name, MailProjectKey: project, ProjectID: "proj-x",
			Provenance: bindings.Adopted, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
				HerdrAlias: strings.ToLower(name), Kind: "claude"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.WriteSecret(inst.Dir, b.ID, "tok-"+name); err != nil {
			t.Fatal(err)
		}
		return b
	}
	// An earlier session of the pane, displaced, and the one now.
	old := bind("BrownHeron")
	if _, err := bs.Displace(ctx, old.ID, old.Generation, "a new session"); err != nil {
		t.Fatal(err)
	}
	b := bind("WindyPanther")

	code, out, errOut := run("operator", "w1:p1", "--socket", s.SocketPath, "--json")
	if code != exitOK {
		t.Fatalf("mark = %d %s %s", code, out, errOut)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	retired, _ := res["retired_bindings"].([]any)
	names := map[string]bool{}
	for _, r := range retired {
		names[r.(map[string]any)["mail_name"].(string)] = true
	}
	if res["action"] != "marked" || res["pane"].(map[string]any)["pane_id"] != "w1:p1" || len(retired) != 2 ||
		!names["WindyPanther"] || !names["BrownHeron"] {
		t.Fatalf("mark result = %v", res)
	}
	for _, id := range []string{b.ID, old.ID} {
		got, err := bs.Get(ctx, id)
		if err != nil || got.Status != bindings.Retired || got.StatusReason != operatorRetireReason {
			t.Fatalf("binding = %+v %v", got, err)
		}
		if _, err := store.ReadSecret(inst.Dir, id); !errors.Is(err, store.ErrNoSecret) {
			t.Fatalf("the token of %s stayed: %v", id, err)
		}
	}
	// The pane takes no binding now.
	if _, err := bs.Create(ctx, bindings.New{MailName: "WindyPanther", MailProjectKey: project, ProjectID: "proj-x",
		Provenance: bindings.Adopted, Occupant: bindings.Occupant{PaneID: "w1:p1", Kind: "claude"}}); !errors.Is(err, bindings.ErrOperatorPane) {
		t.Fatalf("create in the operator's pane: %v", err)
	}
	// Again: the mark stays, and there is no binding to retire.
	code, out, _ = run("operator", "w1:p1", "--socket", s.SocketPath)
	if code != exitOK || !strings.Contains(out, "w1:p1 is an operator's pane: nhm never binds it") || strings.Contains(out, "retired") {
		t.Fatalf("mark again = %d %q", code, out)
	}
	// The calling pane is the default.
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	s.Handle("pane.process_info", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": "w1:p2"}}, nil
	})
	if code, out, errOut := run("operator", "--socket", s.SocketPath); code != exitOK || !strings.HasPrefix(out, "w1:p2 is an operator's pane") {
		t.Fatalf("mark the calling pane = %d %q %q", code, out, errOut)
	}
	// A pane herdr does not have is refused.
	code, _, errOut = run("operator", "w1:p9", "--socket", s.SocketPath)
	if code != exitRefused || !strings.Contains(errOut, "herdr has no pane w1:p9") {
		t.Fatalf("unknown pane = %d %q", code, errOut)
	}

	code, out, _ = run("operator", "--list", "--socket", s.SocketPath, "--json")
	panes := decodeEnvelope(t, out)["result"].(map[string]any)["panes"].([]any)
	if code != exitOK || len(panes) != 2 || panes[0].(map[string]any)["pane_id"] != "w1:p1" || panes[0].(map[string]any)["in_herdr"] != true {
		t.Fatalf("list = %d %s", code, out)
	}
	code, out, _ = run("operator", "--list", "--socket", s.SocketPath)
	if code != exitOK || !strings.Contains(out, "PANE") || !strings.Contains(out, "w1:p2") || !strings.Contains(out, "yes") {
		t.Fatalf("list text = %d %q", code, out)
	}

	code, out, _ = run("operator", "--off", "w1:p1", "--socket", s.SocketPath)
	if code != exitOK || !strings.Contains(out, "w1:p1 is not an operator's pane now") {
		t.Fatalf("off = %d %q", code, out)
	}
	code, out, _ = run("operator", "--off", "w1:p1", "--socket", s.SocketPath, "--json")
	if code != exitOK || decodeEnvelope(t, out)["result"].(map[string]any)["action"] != "not_marked" {
		t.Fatalf("off again = %d %s", code, out)
	}
	if ok, err := bs.IsOperator(ctx, "w1:p1"); err != nil || ok {
		t.Fatalf("w1:p1 still marked: %v %v", ok, err)
	}

	for _, args := range [][]string{{"operator", "a", "b"}, {"operator", "--off", "--list"}, {"operator", "--list", "w1:p1"}, {"operator", "--bogus"}} {
		if code, _, _ := run(append(args, "--socket", s.SocketPath)...); code != exitRefused {
			t.Errorf("%v = %d, want %d", args, code, exitRefused)
		}
	}
	if code, out, _ := run("operator", "--help"); code != exitOK || !strings.Contains(out, "Usage: nhm operator") {
		t.Fatalf("help = %d %q", code, out)
	}
}
