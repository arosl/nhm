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
	"github.com/arosl/nhm/internal/herdr"
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
		ImportBindings: func(context.Context, bool, *herdr.SessionSnapshot) (bindings.ImportReport, error) {
			return bindings.ImportReport{Imported: 1}, nil
		},
		Keep: func() ([]string, error) { return w.keep, nil },
		Now:  func() time.Time { return time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC) },
		// export's GhostOwl token names no project, so no binding takes it;
		// these tests leave it behind on purpose (acfs-9hkh.80).
		Exclude: []string{"GhostOwl"},
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
	if len(rep.Excluded) != 1 || rep.Excluded[0] != "GhostOwl" {
		t.Fatalf("excluded = %v", rep.Excluded)
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

// movingEvents is fakeEvents whose tail moves when mail is delivered.
type movingEvents struct {
	fakeEvents
	tail *int64
}

func (m movingEvents) Tail(context.Context, string, string) (int64, error) { return *m.tail, nil }

// acfs-9hkh.77 F1: mail delivered while handback brings the wake rows up
// to date stays after the Bash's cursor: the tail is read first.
func TestHandbackReadsTheTailFirst(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.export(t, "greencastle")
	if _, err := Takeover(ctx, w.env, false); err != nil {
		t.Fatal(err)
	}
	tail := int64(20)
	w.env.Events = movingEvents{fakeEvents: w.events, tail: &tail}
	// The inbox read happens before message 30 arrives (cursor 25), so it
	// records no row for it; then the message is delivered.
	w.env.RefreshWake = func(context.Context, bindings.Binding) error {
		tail = 25
		return nil
	}
	if _, err := Handback(ctx, w.env, false); err != nil {
		t.Fatal(err)
	}
	if c := *w.bash.imported.Wake[0].State.Cursor; c != 20 {
		t.Fatalf("the Bash's cursor = %d, want 20 (before message 30)", c)
	}
}

// acfs-9hkh.77 F2: unread mail with no delivery event is new, not handled.
func TestTakeoverTreatsEventlessMailAsNew(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.export(t, "greencastle")
	w.env.Mail.(fakeMail).unread["IcyKnoll"] = []int64{5, 12, 40}
	rep, err := Takeover(ctx, w.env, false)
	if err != nil || rep.Wake[0].Handled != 1 || rep.Wake[0].New != 2 {
		t.Fatalf("takeover = %+v %v", rep.Wake, err)
	}
	if _, ok := wakeRow(t, w, "40"); ok {
		t.Fatal("message 40 (no event) was marked as already woken for")
	}
	if a, ok := wakeRow(t, w, "5"); !ok || !a {
		t.Fatal("message 5 (event before the cursor) was not marked")
	}
}

// acfs-9hkh.77 F3, F4: a stale token directory is swept; a switch to the
// current owner is a refusal, like the dry run's.
func TestHandbackSweepsAndSecondSwitchRefuses(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.export(t, "greencastle")
	if _, err := Takeover(ctx, w.env, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Takeover(ctx, w.env, false); !errors.Is(err, ErrRefused) || !errors.Is(err, core.ErrAlreadyOwner) {
		t.Fatalf("takeover --yes when nhm owns = %v", err)
	}
	stale := filepath.Join(w.env.InstanceDir, ".handback.stale")
	fresh := filepath.Join(w.env.InstanceDir, ".handback.live")
	for _, d := range []string{stale, fresh} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "token-x"), []byte("tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := w.env.now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, w.env.now(), w.env.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Handback(ctx, w.env, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale directory is still there: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a live handback's directory was removed: %v", err)
	}
	if _, err := Handback(ctx, w.env, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Handback(ctx, w.env, false); !errors.Is(err, ErrRefused) || !errors.Is(err, core.ErrAlreadyOwner) {
		t.Fatalf("handback --yes when the Bash owns = %v", err)
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

// acfs-9hkh.80: a takeover that would leave one of the Bash's agents
// behind refuses, in the dry run and for real, and the Bash stays the
// owner: a token no binding takes, or an agent the import skipped.
// --exclude names an agent to leave behind (any of its names, any case).
func TestTakeoverRefusesWhatItLeavesBehind(t *testing.T) {
	ctx := context.Background()
	skipped := bindings.ImportReport{Items: []bindings.ImportItem{{PaneID: "w1:p2", Kind: "claude", HerdrName: "rainybasin",
		Action: bindings.ActionSkipped, Reason: "no project: give --project or run inside a git repository"}}}
	// GhostOwl's own pane, skipped too: its token is in the export.
	skippedGhost := bindings.ImportReport{Items: []bindings.ImportItem{{PaneID: "w1:p3", Kind: "codex", HerdrName: "ghostowl",
		Action: bindings.ActionSkipped, Reason: "no verified evidence"}}}
	for _, c := range []struct {
		name     string
		exclude  []string
		imp      *bindings.ImportReport
		want     string // the refusal names this; "" means the takeover passes
		excluded []string
	}{
		{"a token without a binding", nil, nil, "the token of GhostOwl is not imported", nil},
		{"a skipped agent", []string{"GhostOwl"}, &skipped, "agent rainybasin (pane w1:p2) is not imported", nil},
		{"both, excluded by herdr name and mail name", []string{"RAINYBASIN", "ghostowl"}, &skipped, "", []string{"rainybasin", "GhostOwl"}},
		{"excluded by pane id, reported by name", []string{"GhostOwl", "w1:p2"}, &skipped, "", []string{"rainybasin", "GhostOwl"}},
		{"a skipped pane and its token: one agent, listed once", []string{"GhostOwl"}, &skippedGhost, "", []string{"ghostowl"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.export(t)
			w.env.Exclude = c.exclude
			if c.imp != nil {
				imp := *c.imp
				w.env.ImportBindings = func(context.Context, bool, *herdr.SessionSnapshot) (bindings.ImportReport, error) { return imp, nil }
			}
			for _, dry := range []bool{true, false} {
				rep, err := Takeover(ctx, w.env, dry)
				if c.want == "" {
					if err != nil {
						t.Fatalf("dry %v: %v", dry, err)
					}
					if strings.Join(rep.Excluded, ",") != strings.Join(c.excluded, ",") || len(rep.Notes) != len(c.excluded) {
						t.Fatalf("dry %v: excluded %v, notes %v", dry, rep.Excluded, rep.Notes)
					}
					continue
				}
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "--exclude") {
					t.Fatalf("dry %v: = %v", dry, err)
				}
				if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash || o.Generation != 0 {
					t.Fatalf("dry %v: owner = %+v", dry, o)
				}
				if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); err == nil {
					t.Fatalf("dry %v: a refused takeover kept a token", dry)
				}
			}
		})
	}
}

// Review of a1b9425 (acfs-9hkh.85): the owner lock holds the Bash, not
// herdr. The real pass decides on the herdr state the dry check saw, so a
// pane that exits in between changes nothing; an import that still
// decides otherwise refuses, and the Bash stays the owner.
func TestTakeoverAppliesTheCheckedPlan(t *testing.T) {
	ctx := context.Background()
	checked := herdr.SessionSnapshot{Agents: []herdr.AgentInfo{{PaneID: "w1:p2", Agent: "claude"}}}
	// decide is the import: it binds IcyKnoll while its pane is in the
	// snapshot it decides on; herdr itself has lost the pane by the real
	// pass.
	decide := func(snap *herdr.SessionSnapshot, dry bool, calls *[]*herdr.SessionSnapshot) bindings.ImportReport {
		*calls = append(*calls, snap)
		if snap == nil {
			if len(*calls) > 1 {
				// A fresh read after the check: the pane exited.
				return bindings.ImportReport{Items: []bindings.ImportItem{{PaneID: "w1:p2", Kind: "claude",
					Action: bindings.ActionSkipped, Reason: "the pane is gone"}}}
			}
			snap = &checked
		}
		action := bindings.ActionImported
		if dry {
			action = bindings.ActionWouldImport
		}
		return bindings.ImportReport{Snapshot: snap, Items: []bindings.ImportItem{{PaneID: snap.Agents[0].PaneID, Kind: "claude",
			MailName: "IcyKnoll", ProjectKey: "/data/projects/x", Action: action}}}
	}

	t.Run("the real pass reuses the check's snapshot", func(t *testing.T) {
		w := newWorld(t)
		w.export(t)
		var calls []*herdr.SessionSnapshot
		w.env.ImportBindings = func(_ context.Context, dry bool, snap *herdr.SessionSnapshot) (bindings.ImportReport, error) {
			return decide(snap, dry, &calls), nil
		}
		if _, err := Takeover(ctx, w.env, false); err != nil {
			t.Fatalf("= %v", err)
		}
		if len(calls) != 2 || calls[0] != nil || calls[1] != &checked {
			t.Fatalf("import snapshots = %v, want a fresh read, then the check's", calls)
		}
		if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerNhm {
			t.Fatalf("owner = %+v", o)
		}
	})

	t.Run("an import that decides otherwise refuses, the Bash stays the owner", func(t *testing.T) {
		w := newWorld(t)
		w.export(t)
		var calls []*herdr.SessionSnapshot
		w.env.ImportBindings = func(_ context.Context, dry bool, _ *herdr.SessionSnapshot) (bindings.ImportReport, error) {
			// An import that ignores the snapshot it is given.
			return decide(nil, dry, &calls), nil
		}
		// The skip now refuses as a left-behind agent, before the plan
		// check; either refusal keeps the Bash the owner.
		_, err := Takeover(ctx, w.env, false)
		if err == nil || !strings.Contains(err.Error(), "w1:p2") {
			t.Fatalf("= %v", err)
		}
		if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash || o.Generation != 0 {
			t.Fatalf("owner = %+v", o)
		}
		if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); err == nil {
			t.Fatal("a changed import still kept a token")
		}
	})

	t.Run("samePlan names each pane that changed", func(t *testing.T) {
		check := &bindings.ImportReport{Items: []bindings.ImportItem{
			{PaneID: "w1:p2", MailName: "IcyKnoll", Action: bindings.ActionWouldImport},
			{PaneID: "w1:p3", MailName: "RedFox", Action: bindings.ActionBound}}}
		same := &bindings.ImportReport{Items: []bindings.ImportItem{
			{PaneID: "w1:p2", MailName: "IcyKnoll", Action: bindings.ActionImported},
			{PaneID: "w1:p3", MailName: "RedFox", Action: bindings.ActionBound}}}
		if err := samePlan(check, same); err != nil {
			t.Fatalf("would_import then imported: %v", err)
		}
		other := &bindings.ImportReport{Items: []bindings.ImportItem{
			{PaneID: "w1:p2", MailName: "BlueLake", Action: bindings.ActionImported}}}
		err := samePlan(check, other)
		if err == nil || !strings.Contains(err.Error(), "pane w1:p2") || !strings.Contains(err.Error(), "pane w1:p3: checked") {
			t.Fatalf("= %v", err)
		}
	})
}

// Review of c6f599a: a refused --yes writes nothing, not even the import's
// bindings (the import runs dry first); --exclude by pane id covers that
// agent's token; a bare name in two projects is ambiguous, and
// /project:name names one of them.
func TestTakeoverExcludeIsScopedAndRefusesBeforeWriting(t *testing.T) {
	ctx := context.Background()

	t.Run("a refused --yes never runs the writing import", func(t *testing.T) {
		w := newWorld(t)
		w.export(t)
		w.env.Exclude = nil // GhostOwl's token refuses
		var calls []bool
		w.env.ImportBindings = func(_ context.Context, dry bool, _ *herdr.SessionSnapshot) (bindings.ImportReport, error) {
			calls = append(calls, dry)
			return bindings.ImportReport{}, nil
		}
		if _, err := Takeover(ctx, w.env, false); !errors.Is(err, ErrRefused) {
			t.Fatalf("= %v", err)
		}
		if len(calls) != 1 || !calls[0] {
			t.Fatalf("import calls (dry?) = %v, want one dry run", calls)
		}
	})

	t.Run("a pane id covers that agent's token", func(t *testing.T) {
		w := newWorld(t)
		w.export(t)
		w.env.Exclude = []string{"w1:p3"}
		w.env.ImportBindings = func(context.Context, bool, *herdr.SessionSnapshot) (bindings.ImportReport, error) {
			return bindings.ImportReport{Items: []bindings.ImportItem{{PaneID: "w1:p3", Kind: "codex", HerdrName: "ghostowl",
				Action: bindings.ActionSkipped, Reason: "no verified evidence"}}}, nil
		}
		rep, err := Takeover(ctx, w.env, false)
		if err != nil || strings.Join(rep.Excluded, ",") != "ghostowl" {
			t.Fatalf("= %v %v", rep.Excluded, err)
		}
	})

	// Two GhostOwls, in /a and /b, neither bound.
	twoProjects := func(t *testing.T, w *world) {
		t.Helper()
		var toks []TokenRef
		for _, p := range []string{"/a", "/b"} {
			p := p
			f := filepath.Join(w.dir, "GhostOwl"+strings.TrimPrefix(p, "/"))
			if err := os.WriteFile(f, []byte("tok\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			toks = append(toks, TokenRef{ProjectKey: &p, ProjectHash: ProjectHash(p), Name: "GhostOwl", File: f})
		}
		st := State{Format: StateFormat, Version: StateVersion, ExportedAt: "2026-10-12T07:00:00Z",
			Tokens: toks, Wake: []WakeEntry{}, Keep: []string{}}
		data, _ := json.Marshal(st)
		w.bash.export = data
	}
	for _, c := range []struct {
		exclude  []string
		want     string
		excluded string
	}{
		{[]string{"GhostOwl"}, "--exclude GhostOwl names agents in 2 projects: give one of /a:GhostOwl, /b:GhostOwl", ""},
		{[]string{"/a:GhostOwl"}, "the token of GhostOwl is not imported", ""},
		{[]string{"/a:GhostOwl", "/b:ghostowl"}, "", "/a:GhostOwl,/b:GhostOwl"},
		// A skipped pane with no project takes its token by name only
		// while one project has it.
		{[]string{"w1:p3"}, "the token of GhostOwl is not imported", ""},
		{[]string{"w1:p3", "/a:GhostOwl", "/b:GhostOwl"}, "", "ghostowl,/a:GhostOwl,/b:GhostOwl"},
	} {
		t.Run("two projects: "+strings.Join(c.exclude, " "), func(t *testing.T) {
			w := newWorld(t)
			twoProjects(t, w)
			w.env.Exclude = c.exclude
			if strings.Contains(strings.Join(c.exclude, " "), "w1:p3") {
				w.env.ImportBindings = func(context.Context, bool, *herdr.SessionSnapshot) (bindings.ImportReport, error) {
					return bindings.ImportReport{Items: []bindings.ImportItem{{PaneID: "w1:p3", Kind: "codex", HerdrName: "ghostowl",
						Action: bindings.ActionSkipped, Reason: "no project"}}}, nil
				}
			}
			rep, err := Takeover(ctx, w.env, true)
			if c.want != "" {
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("= %v", err)
				}
				return
			}
			if err != nil || strings.Join(rep.Excluded, ",") != c.excluded {
				t.Fatalf("= %v %v", rep.Excluded, err)
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
