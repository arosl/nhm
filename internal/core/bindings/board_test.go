package bindings

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// writeBoard records a board launch in a pane: registered, as
// uilaunch.Register leaves it (the board process, pid, is the pane's
// shell), or opening (pid 0), as uilaunch.Open leaves it once herdr
// answered with the pane, at openedAt.
func writeBoard(t *testing.T, instanceDir, workspaceID, paneID string, pid int, openedAt time.Time) {
	t.Helper()
	state := uilaunch.Registered
	if pid == 0 {
		state = uilaunch.Opening
	}
	data, err := json.Marshal(uilaunch.Launch{Token: "bl-test", InstanceID: "inst-000000000001", WorkspaceID: workspaceID,
		State: state, OpenedAt: openedAt, PaneID: paneID, PID: pid})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(instanceDir, uilaunch.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "board-"+workspaceID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// shellHerdr adds each pane's shell pid to fakeHerdr's process answer.
type shellHerdr struct {
	*fakeHerdr
	shells map[string]int
}

func (s shellHerdr) PaneProcessInfo(ctx context.Context, pane string) (herdr.ProcessInfo, error) {
	info, err := s.fakeHerdr.PaneProcessInfo(ctx, pane)
	if pid, ok := s.shells[pane]; ok {
		info.ShellPID = &pid
	}
	return info, err
}

// herdr names an agent in nhm's live board pane, and Agent Mail's
// identity file names the pane too. Adopt refuses the pane and the import
// skips it, so the board never gets a binding and never becomes a target
// (plan-v4 section 7). A record that cannot be checked refuses too, and
// so does a board that is starting. Once the board is gone (its pane's
// shell is another process), the pane is an agent's again.
func TestBoardPaneIsNeverBound(t *testing.T) {
	project := t.TempDir()
	real, _ := filepath.EvalSymlinks(project)
	fake := session(paneSpec{pane: "w1:p1", kind: "claude", tabLabel: "nhm", cwd: real})
	h := shellHerdr{fakeHerdr: fake, shells: map[string]int{"w1:p1": 4100}}
	env, home := testEnv(t, fake)
	env.Herdr, env.InstanceDir = h, t.TempDir()
	writeBoard(t, env.InstanceDir, "w1", "w1:p1", 4100, t0)
	if _, err := agentmail.WriteIdentity(home, real, "w1:p1", "GreenCastle", t0); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	projectOf := func(context.Context, string) (string, string, error) { return real, "proj-x", nil }
	req := AdoptRequest{MailName: "IcyKnoll", PaneID: "w1:p1", ProjectKey: real, ProjectID: "proj-x"}

	res, err := Adopt(ctx, env, req)
	if !errors.Is(err, ErrBoardPane) {
		t.Fatalf("adopt of the board = %+v, %v", res, err)
	}
	if op, err := env.DB.Operation(ctx, res.OperationID); err != nil || op.Status != "refused" {
		t.Fatalf("operation = %+v, %v", op, err)
	}
	rep, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: t.TempDir()})
	if err != nil || rep.Imported != 0 || len(rep.Items) != 1 || rep.Items[0].Action != ActionSkipped ||
		rep.Items[0].Reason != "the pane is nhm's board, not an agent" {
		t.Fatalf("import = %+v, %v", rep, err)
	}
	if _, err := env.Bindings.ActiveByPane(ctx, "w1:p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the board pane has a binding: %v", err)
	}
	if len(fake.renamed) != 0 || len(fake.paneLabels) != 0 {
		t.Fatalf("the board pane was renamed: %v %v", fake.renamed, fake.paneLabels)
	}

	// A record that cannot be read: nhm cannot tell, so it binds nothing.
	if err := os.WriteFile(filepath.Join(env.InstanceDir, uilaunch.Dir, "board-w1.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(ctx, env, req); err == nil || errors.Is(err, ErrBoardPane) || !strings.Contains(err.Error(), "cannot check") {
		t.Fatalf("adopt with a broken record = %v", err)
	}
	rep, err = ImportOnly(ctx, env, projectOf, ImportRequest{DryRun: true, AcfsHome: t.TempDir()})
	if err != nil || len(rep.Items) != 1 || !strings.HasPrefix(rep.Items[0].Reason, "cannot check that the pane is not nhm's board") {
		t.Fatalf("import with a broken record = %+v, %v", rep, err)
	}

	// A board that is starting, in the pane herdr named in its answer to
	// nhm's open, is refused too.
	writeBoard(t, env.InstanceDir, "w1", "w1:p1", 0, env.Now())
	if _, err := Adopt(ctx, env, req); !errors.Is(err, ErrBoardPane) {
		t.Fatalf("adopt of a starting board = %v", err)
	}

	// The board ended; herdr's pane now runs another shell: adopt binds.
	writeBoard(t, env.InstanceDir, "w1", "w1:p1", 4100, t0)
	h.shells["w1:p1"] = 4200
	if res, err := Adopt(ctx, env, req); err != nil || !res.Created {
		t.Fatalf("adopt after the board = %+v, %v", res, err)
	}
}
