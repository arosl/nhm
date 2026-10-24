package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// nhm add from one project's checkout into a session whose workspace
// holds another project's agents only warns before it creates anything;
// the same add, once the workspace holds an agent of the project, does
// not (nhm-8ze).
func TestAddWarnsOfAnotherProjectsWorkspace(t *testing.T) {
	s, inst, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	add := func() (map[string]any, string) {
		t.Helper()
		code, out, errOut := run("add", "swarm", "--cc=1", "--project", project, "--prompt", "Read AGENTS.md.", "--allow-unguarded",
			"--dry-run", "--socket", s.SocketPath, "--json")
		if code != 0 {
			t.Fatalf("add = %d %s %s", code, out, errOut)
		}
		// ntm's AddResponse, nhm's result and warnings under _nhm.
		nhm := decodeSpawnResponse(t, out)["_nhm"].(map[string]any)
		warnings, _ := nhm["warnings"].([]any)
		for _, w := range warnings {
			if text, _ := w.(string); strings.Contains(text, "holds agents of other projects only") {
				return nhm["result"].(map[string]any), text
			}
		}
		return nhm["result"].(map[string]any), ""
	}

	// The session's workspace w1 holds IcyKnoll and BlueLake of /p only.
	res, mixed := add()
	root, _ := res["working_dir"].(string)
	if root == "" || !strings.Contains(mixed, "workspace w1 holds agents of other projects only (/p), not of "+root+":") {
		t.Fatalf("add into /p's workspace: result %v, warning %q", res, mixed)
	}

	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "GreenPike", MailProjectKey: root,
		ProjectID: "proj-2", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t9",
			PaneID: "w1:p9", HerdrAlias: "greenpike", Kind: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, mixed = add(); mixed != "" {
		t.Fatalf("add beside its own project's agent warned: %q", mixed)
	}
}
