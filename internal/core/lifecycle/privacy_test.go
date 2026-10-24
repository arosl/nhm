package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/privacy"
	"github.com/arosl/nhm/internal/store"
)

// A private session's conversation is not copied, as ntm makes no archive
// in privacy mode: Preserve refuses with the privacy error and writes
// nothing; --allow-persist lets it copy (nhm-2p6.4.16).
func TestPreserveSkipsAPrivateSession(t *testing.T) {
	prev := privacy.GetDefaultManager()
	m := privacy.New(privacy.DefaultConfig())
	privacy.SetDefaultManager(m)
	t.Cleanup(func() { privacy.SetDefaultManager(prev) })
	ctx := context.Background()
	instanceDir := t.TempDir()
	db, err := store.Open(instanceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	original := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(original, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := bindings.Binding{ID: "bnd-1", Generation: 1, Kind: "claude", MailName: "IcyKnoll", MailProjectKey: "/p", WorkspaceID: "w1"}
	m.RegisterSession("w1", true, false)
	_, err = Preserve(ctx, db, instanceDir, b, original, "retire", time.Now())
	if !privacy.IsPrivacyError(err) {
		t.Fatalf("private = %v", err)
	}
	count := func() (n int) {
		t.Helper()
		if err := db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM transcripts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "transcripts")); !os.IsNotExist(err) || count() != 0 {
		t.Fatalf("a private session's transcript was written: %v, %d rows", err, count())
	}
	m.RegisterSession("w1", true, true)
	if p, err := Preserve(ctx, db, instanceDir, b, original, "retire", time.Now()); err != nil || count() != 1 || p.Bytes != 3 {
		t.Fatalf("--allow-persist = %+v %v", p, err)
	}
}
