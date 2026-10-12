package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// cliFacts is a quiet host: no reservations, nothing dirty, no beads.
type cliFacts struct{ assigned []lifecycle.Bead }

func (cliFacts) Reservations(context.Context, string) ([]lifecycle.Reservation, error) {
	return nil, nil
}
func (cliFacts) ReservedEver(context.Context, string, string) ([]string, error) { return nil, nil }
func (cliFacts) DirtyFiles(context.Context, string) ([]string, error)           { return nil, nil }
func (cliFacts) Worktrees(context.Context, string) ([]string, error)            { return nil, nil }
func (cliFacts) WorktreeState(context.Context, string) (bool, bool, error)      { return false, true, nil }
func (cliFacts) RemoveWorktree(context.Context, string, string) error           { return nil }
func (f cliFacts) Assigned(context.Context, string, string) ([]lifecycle.Bead, error) {
	return f.assigned, nil
}
func (cliFacts) Closed(context.Context, string, string) ([]string, error) { return nil, nil }

func TestLifecycleVerbs(t *testing.T) {
	s, inst, _ := waitInstance(t, func(pane string) map[string]any {
		if pane == "w1:p1" {
			return agentMap(pane, "claude", "idle", 4, 4)
		}
		return agentMap(pane, "codex", "working", 7, 0)
	})
	t.Setenv("HOME", t.TempDir())
	old := lifecycleFacts
	lifecycleFacts = cliFacts{}
	t.Cleanup(func() { lifecycleFacts = old })
	var closed []string
	s.Handle("pane.close", func(r herdrtest.Request) (any, *herdrtest.Error) {
		closed = append(closed, string(r.Params))
		return map[string]any{"type": "ok"}, nil
	})
	sock := s.SocketPath

	// A dry run under the Bash: the checks and the actions.
	code, out, errOut := run("retire", "IcyKnoll", "--dry-run", "--socket", sock)
	if code != 0 || !strings.Contains(out, "would close pane w1:p1") || !strings.Contains(out, "would retire IcyKnoll") {
		t.Fatalf("dry run = %d %s %s", code, out, errOut)
	}
	// BlueLake works: refused, exit 2.
	code, _, errOut = run("retire", "BlueLake", "--dry-run", "--socket", sock)
	if code != 2 || !strings.Contains(errOut, "BlueLake is working") {
		t.Fatalf("working = %d %s", code, errOut)
	}
	// A real retire needs nhm as the owner.
	if code, _, _ = run("retire", "IcyKnoll", "--socket", sock); code != 2 || len(closed) != 0 {
		t.Fatalf("under the Bash = %d", code)
	}
	// restore does nothing under the Bash, and says so.
	code, out, _ = run("admin", "restore", "--socket", sock)
	if code != 0 || !strings.Contains(out, "owned by acfs-bash") {
		t.Fatalf("restore = %d %s", code, out)
	}
	ownNhm(t, inst)
	code, out, errOut = run("retire", "IcyKnoll", "--keep-identity", "--socket", sock, "--json")
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	if code != 0 || res["done"] != true || len(closed) != 1 {
		t.Fatalf("retire = %d %s %s", code, out, errOut)
	}
	// inspect: BlueLake is live (unverified by the stub's process facts)
	// or an orphan; IcyKnoll is gone from the lists.
	code, out, errOut = run("admin", "recover", "inspect", "--socket", sock, "--json")
	in := decodeEnvelope(t, out)["result"].(map[string]any)
	if code != 0 || strings.Contains(out, "IcyKnoll") || len(in["live"].([]any))+len(in["orphans"].([]any)) != 1 {
		t.Fatalf("inspect = %d %s %s", code, out, errOut)
	}
	for _, args := range [][]string{
		{"retire"}, {"recycle"}, {"kill"}, {"admin", "recover", "resume", "x"},
		{"admin", "export", "--project", "/p"}, {"admin", "import"},
	} {
		if code, _, _ := run(append(args, "--socket", sock)...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
}
