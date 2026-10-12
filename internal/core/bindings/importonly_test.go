package bindings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// acfs-9hkh.80: the Bash keys an agent spawned with --cwd in a subfolder
// of a work tree by the subfolder; nhm's project for the pane is the work
// tree. With ImportRequest.ProjectAt the import also tries the folder's
// own key and binds the agent there, where the evidence is. The work
// tree's key still comes first when both have evidence.
func TestImportTriesTheFoldersOwnKey(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	projectOf := func(context.Context, string) (string, string, error) { return root, "proj-root", nil }
	projectAt := func(_ context.Context, dir string) (string, string, error) {
		return dir, "proj-" + filepath.Base(dir), nil
	}
	keep := func(t *testing.T, acfs, key string) {
		t.Helper()
		token := BashTokenFile(acfs, key, "IcyKnoll")
		if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pane := paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", tabLabel: "IcyKnoll", cwd: sub}

	t.Run("without ProjectAt the agent is skipped", func(t *testing.T) {
		env, _ := testEnv(t, session(pane))
		acfs := t.TempDir()
		keep(t, acfs, sub)
		rep, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, DryRun: true})
		if err != nil || len(rep.Items) != 1 || rep.Items[0].Action != ActionSkipped || rep.Items[0].ProjectKey != root {
			t.Fatalf("= %+v %v", rep.Items, err)
		}
	})
	t.Run("with ProjectAt it is imported under the folder's key", func(t *testing.T) {
		env, _ := testEnv(t, session(pane))
		acfs := t.TempDir()
		keep(t, acfs, sub)
		rep, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, ProjectAt: projectAt})
		if err != nil || rep.Imported != 1 || rep.Items[0].ProjectKey != sub || rep.Items[0].MailName != "IcyKnoll" {
			t.Fatalf("= %+v %v", rep.Items, err)
		}
		b, err := env.Bindings.ActiveByPane(ctx, "w1:p1")
		if err != nil || b.MailProjectKey != sub || b.ProjectID != "proj-sub" {
			t.Fatalf("binding = %+v %v", b, err)
		}
	})
	t.Run("evidence under both keys: the work tree's wins", func(t *testing.T) {
		env, _ := testEnv(t, session(pane))
		acfs := t.TempDir()
		keep(t, acfs, sub)
		keep(t, acfs, root)
		rep, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, DryRun: true, ProjectAt: projectAt})
		if err != nil || rep.Items[0].Action != ActionWouldImport || rep.Items[0].ProjectKey != root {
			t.Fatalf("= %+v %v", rep.Items, err)
		}
	})
}

// A takeover's real pass decides on the snapshot its dry check returned
// (acfs-9hkh.85): herdr losing the pane in between changes nothing.
func TestImportDecidesOnTheGivenSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	projectOf := func(context.Context, string) (string, string, error) { return root, "proj-root", nil }
	pane := paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", tabLabel: "IcyKnoll", cwd: root}
	h := session(pane)
	env, _ := testEnv(t, h)
	acfs := t.TempDir()
	token := BashTokenFile(acfs, root, "IcyKnoll")
	if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, DryRun: true})
	if err != nil || check.Snapshot == nil || len(check.Items) != 1 || check.Items[0].Action != ActionWouldImport {
		t.Fatalf("check = %+v %v", check, err)
	}
	h.snap = herdr.SessionSnapshot{} // the pane exited after the check
	fresh, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, DryRun: true})
	if err != nil || len(fresh.Items) != 0 {
		t.Fatalf("a fresh read still sees the pane: %+v %v", fresh.Items, err)
	}
	real, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, Snapshot: check.Snapshot})
	if err != nil || real.Imported != 1 || real.Items[0].MailName != "IcyKnoll" || real.Snapshot != check.Snapshot {
		t.Fatalf("real = %+v %v", real, err)
	}
}

// An import cut off after it created a binding leaves no identity file;
// the next run writes it while the Bash spawn evidence holds, and not
// after the Bash retired the agent (it removes the token and the file).
func TestImportWritesAMissingIdentityFileOnlyWhileTheBashEvidenceHolds(t *testing.T) {
	real := t.TempDir()
	h := session(paneSpec{pane: "w1:p1", kind: "claude", alias: "icyknoll", tabLabel: "IcyKnoll", cwd: real})
	env, home := testEnv(t, h)
	ctx := context.Background()
	projectOf := func(context.Context, string) (string, string, error) { return real, "proj-x", nil }
	acfs := t.TempDir()
	token := BashTokenFile(acfs, real, "IcyKnoll")
	if err := os.MkdirAll(filepath.Dir(token), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := ImportRequest{AcfsHome: acfs}
	if rep, err := ImportOnly(ctx, env, projectOf, req); err != nil || rep.Imported != 1 {
		t.Fatalf("import = %+v %v", rep, err)
	}
	path, err := agentmail.IdentityPath(home, real, "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	rep, err := ImportOnly(ctx, env, projectOf, req)
	if err != nil || len(rep.Items) != 1 || rep.Items[0].Action != ActionBound || !strings.Contains(rep.Items[0].Reason, "wrote") {
		t.Fatalf("repair run = %+v %v", rep, err)
	}
	if rec, ok, err := agentmail.ReadIdentity(home, real, "w1:p1"); !ok || err != nil || rec.Name != "IcyKnoll" {
		t.Fatalf("identity file = %+v %v %v", rec, ok, err)
	}
	// A dry run writes nothing, and a file that exists stays as it is.
	if rep, err := ImportOnly(ctx, env, projectOf, ImportRequest{AcfsHome: acfs, DryRun: true}); err != nil || rep.Items[0].Reason != "" {
		t.Fatalf("dry run = %+v %v", rep, err)
	}

	// The Bash retires the agent: the token and the file go.
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if rep, err := ImportOnly(ctx, env, projectOf, req); err != nil || rep.Items[0].Reason != "" {
		t.Fatalf("run after the retire = %+v %v", rep, err)
	}
	if _, ok, err := agentmail.ReadIdentity(home, real, "w1:p1"); ok || err != nil {
		t.Fatalf("the identity file came back after the retire (%v)", err)
	}
}
