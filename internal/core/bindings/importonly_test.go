package bindings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/agentmail"
)

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
