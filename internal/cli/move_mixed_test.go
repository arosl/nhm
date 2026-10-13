package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// nhm move warns, as add and adopt do, when the workspace it moves an
// agent to holds other projects' agents only; a dry run warns too, and a
// workspace with an agent of the project gets no such warning (nhm-8ze).
func TestMoveWarnsOfAnotherProjectsWorkspace(t *testing.T) {
	s, inst, _, project := moveInstance(t)
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	put := func(name, key, pane string) {
		t.Helper()
		if _, err := bs.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: key, ProjectID: "p-" + name,
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w2", TabID: "w2:t" + pane, PaneID: "w2:p" + pane,
				HerdrAlias: strings.ToLower(name), Kind: "claude"}}); err != nil {
			t.Fatal(err)
		}
	}
	mixed := func() string {
		t.Helper()
		code, out, errOut := run("move", "IcyKnoll", "--workspace", "other", "--dry-run", "--socket", s.SocketPath, "--json")
		if code != exitOK {
			t.Fatalf("dry run = %d %s %s", code, out, errOut)
		}
		warnings, _ := decodeEnvelope(t, out)["warnings"].([]any)
		for _, w := range warnings {
			if text, _ := w.(string); strings.Contains(text, "holds agents of other projects only") {
				return text
			}
		}
		return ""
	}
	put("GreenPike", "/other", "7")
	if w := mixed(); !strings.Contains(w, "workspace w2 holds agents of other projects only (/other), not of "+project+":") {
		t.Fatalf("move into /other's workspace: warning %q", w)
	}
	put("TealOwl", project, "8")
	if w := mixed(); w != "" {
		t.Fatalf("move beside its own project's agent warned: %q", w)
	}
}
