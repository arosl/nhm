package spawn

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/privacy"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// startSees is the fake herdr, and notes whether workspace w1's privacy
// record was there when an agent started.
type startSees struct {
	*fakeHerdr
	db   *store.DB
	seen []bool
}

func (s *startSees) AgentStart(ctx context.Context, p herdr.AgentStartParams) (herdr.AgentStarted, error) {
	_, ok, _ := privacy.Recorded(ctx, s.db, "w1")
	s.seen = append(s.seen, ok)
	return s.fakeHerdr.AgentStart(ctx, p)
}

// ntm's --privacy and --allow-persist are recorded for the session before
// any agent starts (nhm-2p6.4.16): a new workspace has them once it is
// made, and never keeps an old record of its id; an existing workspace
// takes them only when one is set; this process's manager knows them.
func TestSpawnRecordsPrivacy(t *testing.T) {
	prev := privacy.GetDefaultManager()
	privacy.SetDefaultManager(privacy.New(privacy.DefaultConfig()))
	t.Cleanup(func() { privacy.SetDefaultManager(prev) })
	ctx := context.Background()

	w := newWorld(t)
	if err := privacy.Record(ctx, w.db, "w1", true, false); err != nil {
		t.Fatal(err)
	}
	if res, err := Spawn(ctx, w.env, req(Want{Kind: "claude", Count: 1})); err != nil || res.WorkspaceID != "w1" {
		t.Fatalf("spawn = %v %v", res.WorkspaceID, err)
	}
	if _, ok, _ := privacy.Recorded(ctx, w.db, "w1"); ok {
		t.Fatal("the new workspace kept an old record of its id")
	}

	w = newWorld(t)
	sees := &startSees{fakeHerdr: w.h, db: w.db}
	w.env.Herdr = sees
	r := req(Want{Kind: "claude", Count: 2})
	r.Privacy = true
	if res, err := Spawn(ctx, w.env, r); err != nil || res.WorkspaceID != "w1" {
		t.Fatalf("spawn --privacy = %v %v", res.WorkspaceID, err)
	}
	if st, ok, _ := privacy.Recorded(ctx, w.db, "w1"); !ok || st != (privacy.SessionState{PrivacyMode: true}) {
		t.Fatalf("recorded = %+v %v", st, ok)
	}
	if len(sees.seen) != 2 || !sees.seen[0] || !sees.seen[1] {
		t.Fatalf("recorded before each start = %v", sees.seen)
	}
	if !privacy.GetDefaultManager().IsPrivacyEnabled("w1") {
		t.Fatal("this process's manager does not know it")
	}

	// The existing workspace: add without the flags keeps the record;
	// with one, it replaces it, as ntm sets the session option.
	r = req(Want{Kind: "codex", Count: 1})
	r.Add = true
	if _, err := Spawn(ctx, w.env, r); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := privacy.Recorded(ctx, w.db, "w1"); st != (privacy.SessionState{PrivacyMode: true}) {
		t.Fatalf("add without flags = %+v", st)
	}
	r.AllowPersist = true
	if _, err := Spawn(ctx, w.env, r); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := privacy.Recorded(ctx, w.db, "w1"); st != (privacy.SessionState{AllowPersist: true}) {
		t.Fatalf("add --allow-persist = %+v", st)
	}
	// The choices enter the fingerprint only when set.
	if fp := fingerprint(req()); fp["privacy"] != nil || fp["allow_persist"] != nil {
		t.Fatalf("fingerprint without = %v", fp)
	}
	if fp := fingerprint(r); fp["allow_persist"] != true {
		t.Fatalf("fingerprint with = %v", fp)
	}
}

// A private session's spawn keeps its prompts out of the store, as their
// digests in the operation's request; its fingerprint is the whole
// request's, so a rerun of the id is a replay. Another session's spawn
// keeps them as they are (nhm-2p6.4.16).
func TestPrivateSpawnStoresPromptDigests(t *testing.T) {
	prev := privacy.GetDefaultManager()
	privacy.SetDefaultManager(privacy.New(privacy.DefaultConfig()))
	t.Cleanup(func() { privacy.SetDefaultManager(prev) })
	ctx := context.Background()
	w := newWorld(t)
	r := req(Want{Kind: "claude", Count: 1})
	r.OperationID, r.Prompt, r.Privacy = "op-priv", "Fix the secret bug.", true
	r.DefaultPrompts = map[string]string{"claude": "Read the private notes."}
	if _, err := Spawn(ctx, w.env, r); err != nil {
		t.Fatal(err)
	}
	op, err := w.db.Operation(ctx, "op-priv")
	if err != nil || strings.Contains(op.RequestJSON, "secret") || strings.Contains(op.RequestJSON, "private notes") ||
		!strings.Contains(op.RequestJSON, `"prompt":"sha256:`) || !strings.Contains(op.RequestJSON, `"default_prompts":"sha256:`) {
		t.Fatalf("stored request = %s %v", op.RequestJSON, err)
	}
	if again, err := Spawn(ctx, w.env, r); err != nil || !again.Replayed {
		t.Fatalf("rerun = %+v %v", again, err)
	}
	w = newWorld(t)
	r.OperationID, r.Privacy = "op-open", false
	r.Session = "open"
	if _, err := Spawn(ctx, w.env, r); err != nil {
		t.Fatal(err)
	}
	if op, err := w.db.Operation(ctx, "op-open"); err != nil || !strings.Contains(op.RequestJSON, "Fix the secret bug.") {
		t.Fatalf("an open session's request = %s %v", op.RequestJSON, err)
	}
}
