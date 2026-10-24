package privacy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arosl/nhm/internal/store"
)

// ntm's rules (nhm-2p6.4.16): nothing is blocked outside privacy mode;
// in it, each operation is blocked by its key, all set by default, and
// --allow-persist lifts every block. The global switch makes every
// session private.
func TestCanPersist(t *testing.T) {
	m := New(DefaultConfig())
	for _, op := range []PersistOperation{OpCheckpoint, OpEventLog, OpPromptHistory, OpScrollback, OpExport, OpArchive} {
		if err := m.CanPersist("w1", op); err != nil {
			t.Fatalf("%s outside privacy mode: %v", op, err)
		}
	}
	m.RegisterSession("w1", true, false)
	want := map[PersistOperation]string{
		OpCheckpoint:    "privacy mode: checkpoints are disabled in privacy mode (session: w1)",
		OpEventLog:      "privacy mode: event logging is disabled in privacy mode (session: w1)",
		OpPromptHistory: "privacy mode: prompt history is disabled in privacy mode (session: w1)",
		OpScrollback:    "privacy mode: scrollback capture is disabled in privacy mode (session: w1)",
		OpExport:        "privacy mode: exports require --allow-persist in privacy mode (session: w1)",
		OpArchive:       "privacy mode: exports require --allow-persist in privacy mode (session: w1)",
	}
	for op, msg := range want {
		if err := m.CanPersist("w1", op); err == nil || err.Error() != msg || !IsPrivacyError(err) {
			t.Fatalf("%s = %v", op, err)
		}
	}
	if err := m.CanPersist("w2", OpPromptHistory); err != nil || m.IsPrivacyEnabled("w2") || !m.IsPrivacyEnabled("w1") {
		t.Fatalf("another session = %v", err)
	}
	m.RegisterSession("w1", true, true)
	if err := m.CanPersist("w1", OpExport); err != nil {
		t.Fatalf("--allow-persist = %v", err)
	}
	cfg := DefaultConfig()
	cfg.DisablePromptHistory = false
	m = New(cfg)
	m.RegisterSession("w1", true, false)
	if err := m.CanPersist("w1", OpPromptHistory); err != nil {
		t.Fatalf("history allowed by config = %v", err)
	}
	cfg.Enabled = true
	if err := New(cfg).CanPersist("w9", OpEventLog); err == nil {
		t.Fatal("the global switch blocks nothing")
	}
}

// Record stores a session's settings in the instance's store, where
// another process's manager finds them; a record with neither setting is
// removed, so a new workspace never takes an old one of its id.
func TestRecordAndLookup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, ok, err := Recorded(ctx, db, "w1"); ok || err != nil {
		t.Fatalf("nothing recorded = %v %v", ok, err)
	}
	if err := Record(ctx, db, "w1", true, false); err != nil {
		t.Fatal(err)
	}
	if st, ok, err := Recorded(ctx, db, "w1"); !ok || err != nil || st != (SessionState{PrivacyMode: true}) {
		t.Fatalf("recorded = %+v %v %v", st, ok, err)
	}
	m := New(DefaultConfig()).WithSessionLookup(StoreLookup(db))
	if err := m.CanPersist("w1", OpPromptHistory); err == nil {
		t.Fatal("another process's record is not seen")
	}
	if err := Record(ctx, db, "w2", false, true); err != nil {
		t.Fatal(err)
	}
	if st, ok, _ := Recorded(ctx, db, "w2"); !ok || st != (SessionState{AllowPersist: true}) {
		t.Fatalf("allow-persist alone = %+v %v", st, ok)
	}
	if err := Record(ctx, db, "w1", false, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := Recorded(ctx, db, "w1"); ok {
		t.Fatal("a fresh workspace kept the old record")
	}
	// A recorded answer stays for the manager's lifetime.
	if err := m.CanPersist("w1", OpPromptHistory); err == nil {
		t.Fatal("a private session was downgraded")
	}
}

// [privacy] goes over ntm's defaults; a missing file is the defaults; a
// file that does not parse refuses.
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	if c, err := LoadConfig(filepath.Join(dir, "none.toml")); err != nil || c != DefaultConfig() {
		t.Fatalf("missing = %+v %v", c, err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[spawn]\nstagger_mode = \"none\"\n[privacy]\nenabled = true\ndisable_event_logs = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := DefaultConfig()
	want.Enabled, want.DisableEventLogs = true, false
	if c, err := LoadConfig(path); err != nil || c != want {
		t.Fatalf("set = %+v %v", c, err)
	}
	if err := os.WriteFile(path, []byte("[privacy]\nenabled = \"yes\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("a bad value loads")
	}
}
