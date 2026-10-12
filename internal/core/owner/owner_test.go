package owner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

type fakeBash struct {
	export   []byte
	imported State
	tokens   map[string]string // name -> token, read at import time
	fail     error
}

func (f *fakeBash) Export(context.Context) ([]byte, error) { return f.export, nil }

func (f *fakeBash) Import(_ context.Context, file string) error {
	if f.fail != nil {
		return f.fail
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if f.imported, err = ParseState(data); err != nil {
		return err
	}
	f.tokens = map[string]string{}
	for _, t := range f.imported.Tokens {
		tok, err := readToken(t.File)
		if err != nil {
			return err
		}
		f.tokens[t.Name] = tok
	}
	return nil
}

type fakeEvents struct {
	events map[string][]Event // agent -> events
	tail   int64
}

func (f fakeEvents) After(_ context.Context, _, agent string, cursor int64) ([]Event, error) {
	var out []Event
	for _, e := range f.events[agent] {
		if e.Cursor > cursor {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f fakeEvents) Tail(context.Context, string, string) (int64, error) { return f.tail, nil }

type fakeMail struct {
	unread map[string][]int64
	down   map[string]bool
}

func (m fakeMail) Call(_ context.Context, tool string, args, out any) error {
	if tool != "fetch_inbox" {
		return errors.New("unexpected tool " + tool)
	}
	a := args.(map[string]any)
	if m.down[a["agent_name"].(string)] {
		return errors.New("connection refused")
	}
	if a["mark_read"] != false {
		return errors.New("the inbox read would consume it")
	}
	var list []map[string]any
	for _, id := range m.unread[a["agent_name"].(string)] {
		list = append(list, map[string]any{"id": id})
	}
	data, _ := json.Marshal(list)
	return json.Unmarshal(data, out)
}

type world struct {
	env    Env
	bash   *fakeBash
	events fakeEvents
	keep   []string
	dir    string
	b      bindings.Binding
}

func newWorld(t *testing.T) *world {
	t.Helper()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &world{bash: &fakeBash{}, events: fakeEvents{events: map[string][]Event{}, tail: 99}, keep: []string{"GreenCastle"}, dir: t.TempDir()}
	w.env = Env{DB: db, Bindings: bindings.NewStore(db), InstanceDir: inst.Dir, Bash: w.bash,
		Mail: fakeMail{unread: map[string][]int64{"IcyKnoll": {5, 12}}, down: map[string]bool{}},
		ImportBindings: func(context.Context, bool) (bindings.ImportReport, error) {
			return bindings.ImportReport{Imported: 1}, nil
		},
		Keep: func() ([]string, error) { return w.keep, nil },
		Now:  func() time.Time { return time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC) },
	}
	w.env.Events = w.events
	w.b, err = w.env.Bindings.Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Imported, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "icyknoll", Kind: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// export is the Bash's state: IcyKnoll's token and wake state, and a
// token whose project nobody records.
func (w *world) export(t *testing.T, keep ...string) {
	t.Helper()
	icy := filepath.Join(w.dir, "IcyKnoll")
	ghost := filepath.Join(w.dir, "GhostOwl")
	for f, tok := range map[string]string{icy: "tok-icy\n", ghost: "tok-ghost\n"} {
		if err := os.WriteFile(f, []byte(tok), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := "/p"
	cursor, last := int64(10), int64(1760000000)
	st := State{Format: StateFormat, Version: StateVersion, ExportedAt: "2026-10-12T07:00:00Z",
		Tokens: []TokenRef{{ProjectKey: &p, ProjectHash: ProjectHash(p), Name: "IcyKnoll", File: icy},
			{ProjectHash: "0123456789abcdef", Name: "GhostOwl", File: ghost}},
		Wake: []WakeEntry{{ProjectKey: &p, ProjectHash: ProjectHash(p), Name: "IcyKnoll",
			State: WakeState{Cursor: &cursor, LastWake: &last, Overdue: []int64{}}}},
		Keep: keep}
	if st.Keep == nil {
		st.Keep = []string{}
	}
	data, _ := json.Marshal(st)
	w.bash.export = data
	// Message 5 reached IcyKnoll before the Bash's cursor, 12 after it.
	w.events.events["IcyKnoll"] = []Event{{Cursor: 8, MessageID: 5, Kind: "to"}, {Cursor: 15, MessageID: 12, Kind: "cc"}}
}

func wakeRow(t *testing.T, w *world, id string) (announced bool, ok bool) {
	t.Helper()
	var at *string
	err := w.env.DB.SQL().QueryRow(`SELECT announced_at FROM wake WHERE binding_id = ? AND message_id = ?`, w.b.ID, id).Scan(&at)
	if err != nil {
		return false, false
	}
	return at != nil, true
}

func TestTakeoverAndHandback(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.export(t, "greencastle")

	// A dry run checks everything and writes nothing.
	rep, err := Takeover(ctx, w.env, true)
	if err != nil || rep.Owner.Owner != core.OwnerBash || len(rep.Tokens) != 2 || rep.Wake[0].Handled != 1 || rep.Wake[0].New != 1 {
		t.Fatalf("dry run = %+v %v", rep, err)
	}
	if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); err == nil {
		t.Fatal("the dry run kept a token")
	}

	rep, err = Takeover(ctx, w.env, false)
	if err != nil || rep.Owner.Owner != core.OwnerNhm || rep.Owner.Generation != 1 {
		t.Fatalf("takeover = %+v %v", rep, err)
	}
	actions := map[string]string{}
	for _, it := range rep.Tokens {
		actions[it.Name] = it.Action
	}
	if actions["IcyKnoll"] != ActionKept || actions["GhostOwl"] != ActionACFSOnly {
		t.Fatalf("tokens = %+v", rep.Tokens)
	}
	if tok, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); err != nil || tok != "tok-icy" {
		t.Fatalf("token = %q %v", tok, err)
	}
	// The mail the Bash woke for is announced; the newer one stays for
	// nhm's wake.
	if a, ok := wakeRow(t, w, "5"); !ok || !a {
		t.Fatalf("message 5: row %v, announced %v", ok, a)
	}
	if _, ok := wakeRow(t, w, "12"); ok {
		t.Fatal("message 12 was marked")
	}
	if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerNhm {
		t.Fatalf("owner.toml = %+v", o)
	}
	if _, err := Takeover(ctx, w.env, false); !errors.Is(err, ErrRefused) {
		t.Fatalf("a second takeover = %v", err)
	}

	// Handback: message 12 is pending in nhm, so the Bash's cursor sits
	// just before its delivery event.
	if _, err := w.env.DB.SQL().Exec(`INSERT INTO wake (binding_id, message_id, first_seen_at) VALUES (?, '12', 'x')`, w.b.ID); err != nil {
		t.Fatal(err)
	}
	var refreshed []string
	w.env.RefreshWake = func(_ context.Context, b bindings.Binding) error {
		refreshed = append(refreshed, b.MailName)
		return nil
	}
	rep, err = Handback(ctx, w.env, false)
	if err != nil || rep.Owner.Owner != core.OwnerBash || rep.Owner.Generation != 2 || len(refreshed) != 1 {
		t.Fatalf("handback = %+v %v, refreshed %v", rep, err, refreshed)
	}
	im := w.bash.imported
	if len(im.Wake) != 1 || *im.Wake[0].State.Cursor != 14 || w.bash.tokens["IcyKnoll"] != "tok-icy" ||
		len(im.Keep) != 1 || im.Keep[0] != "GreenCastle" {
		t.Fatalf("the Bash got %+v, tokens %v", im, w.bash.tokens)
	}
	// The token files went with the import.
	if left, _ := filepath.Glob(filepath.Join(w.env.InstanceDir, ".handback.*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func TestTakeoverRefusals(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, w *world)
		want  string
	}{
		{"keep list not covered", func(t *testing.T, w *world) { w.export(t, "RedStone") }, "keep list lacks RedStone"},
		{"token unreadable", func(t *testing.T, w *world) {
			w.export(t)
			if err := os.WriteFile(filepath.Join(w.dir, "IcyKnoll"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "cannot be read"},
		{"not the Bash's format", func(t *testing.T, w *world) { w.bash.export = []byte(`{"format":"x","version":1}`) }, "not acfs-agents-state"},
		{"a bad entry", func(t *testing.T, w *world) {
			w.bash.export = []byte(`{"format":"acfs-agents-state","version":1,"tokens":[{"name":"x y","file":"f"}],"wake":[],"keep":[]}`)
		}, "tokens[0]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			c.setup(t, w)
			if _, err := Takeover(ctx, w.env, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("= %v", err)
			}
			if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash || o.Generation != 0 {
				t.Fatalf("owner = %+v", o)
			}
			if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); err == nil {
				t.Fatal("a refused takeover kept a token")
			}
		})
	}
}

// A takeover that fails on a later entry writes no token it planned for
// an earlier one; a retry then imports the Bash's token, also over one
// nhm kept before.
func TestTakeoverWritesNothingOnFailure(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.export(t)
	w.env.Mail.(fakeMail).down["IcyKnoll"] = true
	_, err := Takeover(ctx, w.env, false)
	if err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "inbox of IcyKnoll") {
		t.Fatalf("= %v (an outage is a failure, not a refusal)", err)
	}
	if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); !errors.Is(err, store.ErrNoSecret) {
		t.Fatalf("a token was written: %v", err)
	}
	if err := store.WriteSecret(w.env.InstanceDir, w.b.ID, "tok-stale"); err != nil {
		t.Fatal(err)
	}
	w.env.Mail.(fakeMail).down["IcyKnoll"] = false
	rep, err := Takeover(ctx, w.env, false)
	if err != nil || rep.Tokens[0].Action != ActionReplaced {
		t.Fatalf("retry = %+v %v", rep.Tokens, err)
	}
	if tok, _ := store.ReadSecret(w.env.InstanceDir, w.b.ID); tok != "tok-icy" {
		t.Fatalf("token = %q", tok)
	}
}

func TestHandbackRefusals(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		row  string
		want string
	}{
		{"pending without an event", `INSERT INTO wake (binding_id, message_id, first_seen_at) VALUES (?, '77', 'x')`, "no delivery event"},
		{"nudge in flight", `INSERT INTO wake (binding_id, message_id, first_seen_at, last_outcome) VALUES (?, '6', 'x', 'attempt_started')`, "in flight"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.export(t)
			if _, err := Takeover(ctx, w.env, false); err != nil {
				t.Fatal(err)
			}
			if _, err := w.env.DB.SQL().Exec(c.row, w.b.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := Handback(ctx, w.env, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("= %v", err)
			}
			if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerNhm {
				t.Fatalf("owner = %+v", o)
			}
		})
	}
	// The Bash refusing the state keeps nhm the owner.
	w := newWorld(t)
	w.export(t)
	if _, err := Takeover(ctx, w.env, false); err != nil {
		t.Fatal(err)
	}
	w.bash.fail = errors.New("agents.toml does not keep GreenCastle")
	if _, err := Handback(ctx, w.env, false); err == nil || !strings.Contains(err.Error(), "refused the state") {
		t.Fatalf("= %v", err)
	}
	if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerNhm {
		t.Fatalf("owner = %+v", o)
	}
	// A token nhm cannot read would be lost: refused.
	w.bash.fail = nil
	secret := filepath.Join(w.env.InstanceDir, "secrets", w.b.ID)
	if err := os.Chmod(secret, 0); err != nil {
		t.Skipf("cannot make the token unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })
	if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); err == nil {
		t.Skip("the token stays readable (running as root)")
	}
	if _, err := Handback(ctx, w.env, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "would lose it") {
		t.Fatalf("unreadable token = %v", err)
	}
}
