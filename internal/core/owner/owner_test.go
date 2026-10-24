package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

type fakeEvents struct {
	events map[string][]Event // agent -> events
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

type fakeMail struct {
	unread map[string][]int64
	down   map[string]bool
	// tokens is each identity's current registration token in Agent
	// Mail; an identity it lacks is not found.
	tokens  map[string]string
	retired map[string]bool
}

func (m fakeMail) Call(_ context.Context, tool string, args, out any) error {
	a := args.(map[string]any)
	name := a["agent_name"].(string)
	if m.down[name] {
		return errors.New("connection refused")
	}
	switch tool {
	case "whois":
		if _, ok := m.tokens[name]; !ok {
			return &agentmail.ToolError{Tool: tool, Message: `{"error":{"type":"AGENT_NOT_FOUND"}}`}
		}
		retired := "null"
		if m.retired[name] {
			retired = `"2026-10-12T06:00:00Z"`
		}
		return json.Unmarshal([]byte(`{"name":"`+name+`","retired_at":`+retired+`}`), out)
	case "unretire_agent":
		if m.retired[name] {
			return errors.New("unretire_agent on a retired identity would bring it back")
		}
		if a["registration_token"] != m.tokens[name] {
			return &agentmail.ToolError{Tool: tool, Message: `{"error":{"type":"AUTHENTICATION_REQUIRED","data":{"reason":"token_mismatch"}}}`}
		}
		return nil
	case "fetch_inbox":
	default:
		return errors.New("unexpected tool " + tool)
	}
	if a["mark_read"] != false {
		return errors.New("the inbox read would consume it")
	}
	var list []map[string]any
	for _, id := range m.unread[name] {
		list = append(list, map[string]any{"id": id})
	}
	data, _ := json.Marshal(list)
	return json.Unmarshal(data, out)
}

type world struct {
	env    Env
	events fakeEvents
	keep   []string
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
	w := &world{events: fakeEvents{events: map[string][]Event{}}, keep: []string{"GreenCastle"}}
	w.env = Env{DB: db, Bindings: bindings.NewStore(db), InstanceDir: inst.Dir, AcfsHome: t.TempDir(),
		Mail: fakeMail{unread: map[string][]int64{"IcyKnoll": {5, 12}}, down: map[string]bool{},
			tokens: map[string]string{"IcyKnoll": "tok-icy"}, retired: map[string]bool{}},
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

// bashFile writes one file of the Bash's state, under state/ of the ACFS
// home.
func (w *world) bashFile(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(w.env.AcfsHome, "state", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// export is the Bash's state, as its files: IcyKnoll's token and wake
// state, and a token whose project nobody records.
func (w *world) export(t *testing.T) {
	t.Helper()
	h := ProjectHash("/p")
	w.bashFile(t, "tokens/"+h+"/.project", "/p\n")
	w.bashFile(t, "tokens/"+h+"/IcyKnoll", "tok-icy\n")
	w.bashFile(t, "tokens/0123456789abcdef/GhostOwl", "tok-ghost\n")
	w.bashFile(t, "wake/"+h+"/project", "/p\n")
	w.bashFile(t, "wake/"+h+"/IcyKnoll.json", `{"cursor": 10, "last_wake": 1760000000, "overdue": []}`)
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

// nhm-te0: the takeover reads the Bash's files itself; no acfs command
// runs (Env has none).
func TestTakeover(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.export(t)

	// A dry run checks everything and writes nothing.
	rep, err := Takeover(ctx, w.env, true)
	if err != nil || rep.Owner.Owner != core.OwnerBash || len(rep.Tokens) != 2 || rep.Wake[0].Handled != 1 || rep.Wake[0].New != 1 {
		t.Fatalf("dry run = %+v %v", rep, err)
	}
	if strings.Join(rep.Keep, ",") != "GreenCastle" {
		t.Fatalf("keep = %v, want nhm's keep list", rep.Keep)
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
	// A second switch to nhm is a refusal, like the dry run's.
	if _, err := Takeover(ctx, w.env, false); !errors.Is(err, ErrRefused) || !errors.Is(err, core.ErrAlreadyOwner) {
		t.Fatalf("a second takeover = %v", err)
	}
	if _, err := Takeover(ctx, w.env, true); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "nhm owns the instance already") {
		t.Fatalf("a dry run when nhm owns = %v", err)
	}
	// It deletes nothing of the Bash's.
	if _, err := os.Stat(filepath.Join(w.env.AcfsHome, "state", "tokens", ProjectHash("/p"), "IcyKnoll")); err != nil {
		t.Fatalf("the Bash's token file: %v", err)
	}
}

// nhm-te0: ReadState reads the files `acfs agents export` read, as it
// read them.
func TestReadState(t *testing.T) {
	w := newWorld(t)
	if st, notes, err := ReadState(w.env.AcfsHome); err != nil || len(st.Tokens) != 0 || len(st.Wake) != 0 || len(notes) != 0 {
		t.Fatalf("no state directory = %+v %v %v, want nothing kept", st, notes, err)
	}
	if _, _, err := ReadState(""); err == nil {
		t.Fatal("no ACFS home: read the current directory")
	}

	w.export(t)
	h, wh := ProjectHash("/p"), ProjectHash("/w")
	// A token directory without .project takes the wake directory's
	// project; with neither, the token has only its hash.
	w.bashFile(t, "tokens/"+wh+"/RedFox", "tok-red\n")
	w.bashFile(t, "wake/"+wh+"/project", "/w\n")
	// What the Bash would not have written is not its: hidden files, names
	// it never gives, other files in a wake directory, a directory in a
	// token directory.
	w.bashFile(t, "tokens/"+h+"/.hidden", "x\n")
	w.bashFile(t, "tokens/"+h+"/Not-A-Name", "x\n")
	w.bashFile(t, "tokens/"+h+"/sub/BlueLake", "x\n")
	w.bashFile(t, "tokens/.old/BlueLake", "x\n")
	w.bashFile(t, "wake/"+h+"/notes.txt", "x\n")
	// A wake file that is not a wake state is left out, with a note.
	w.bashFile(t, "wake/"+wh+"/RedFox.json", `{"cursor": 1, "last_wake": 2}`)
	w.bashFile(t, "wake/"+wh+"/OldFox.json", `{"cursor": 1.5, "last_wake": 2, "overdue": []}`)

	st, notes, err := ReadState(w.env.AcfsHome)
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for _, tr := range st.Tokens {
		tokens = append(tokens, fmt.Sprintf("%s %s %s %s", tr.Name, projectOf(tr.ProjectKey), tr.ProjectHash, filepath.Base(tr.File)))
	}
	want := []string{"GhostOwl  0123456789abcdef GhostOwl", "IcyKnoll /p " + h + " IcyKnoll", "RedFox /w " + wh + " RedFox"}
	sort.Strings(tokens)
	if strings.Join(tokens, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tokens =\n%s\nwant\n%s", strings.Join(tokens, "\n"), strings.Join(want, "\n"))
	}
	if len(st.Wake) != 1 || st.Wake[0].Name != "IcyKnoll" || projectOf(st.Wake[0].ProjectKey) != "/p" || st.Wake[0].ProjectHash != h ||
		*st.Wake[0].State.Cursor != 10 || *st.Wake[0].State.LastWake != 1760000000 {
		t.Fatalf("wake = %+v", st.Wake)
	}
	if len(notes) != 2 || !strings.Contains(strings.Join(notes, "\n"), "RedFox.json is not a wake state; left out") ||
		!strings.Contains(strings.Join(notes, "\n"), "OldFox.json is not a wake state") {
		t.Fatalf("notes = %q", notes)
	}

	// The takeover reports the notes, and goes on.
	w.env.Exclude = []string{"GhostOwl", "RedFox"}
	rep, err := Takeover(context.Background(), w.env, true)
	if err != nil || !strings.Contains(strings.Join(rep.Notes, "\n"), "RedFox.json is not a wake state") {
		t.Fatalf("takeover notes = %q", rep.Notes)
	}

	// A state directory that cannot be read fails: no token is missed.
	tokensDir := filepath.Join(w.env.AcfsHome, "state", "tokens")
	if err := os.Chmod(tokensDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tokensDir, 0o700) })
	if _, err := os.ReadDir(tokensDir); err == nil {
		t.Skip("the directory stays readable (running as root)")
	}
	if _, _, err := ReadState(w.env.AcfsHome); err == nil {
		t.Fatal("an unreadable tokens directory read as no tokens")
	}
	if _, err := Takeover(context.Background(), w.env, false); err == nil || errors.Is(err, ErrRefused) ||
		!strings.Contains(err.Error(), "the Bash's state cannot be read") {
		t.Fatalf("takeover = %v (a failure, not a refusal)", err)
	}
	if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash {
		t.Fatalf("owner = %+v", o)
	}
}

// acfs-9hkh.92: the Bash keeps the token of the spawn, and an agent that
// registered itself again since then has a newer one. Takeover checks each
// token with Agent Mail: a stale one is reported, never kept.
func TestTakeoverChecksEachTokenWithAgentMail(t *testing.T) {
	ctx := context.Background()
	tokenItem := func(rep Report) Item {
		for _, it := range rep.Tokens {
			if it.Name == "IcyKnoll" {
				return it
			}
		}
		t.Fatalf("no IcyKnoll token in %+v", rep.Tokens)
		return Item{}
	}

	// IcyKnoll registered again: Agent Mail holds a newer token.
	w := newWorld(t)
	w.export(t)
	w.env.Mail.(fakeMail).tokens["IcyKnoll"] = "tok-newer"
	rep, err := Takeover(ctx, w.env, true)
	if it := tokenItem(rep); err != nil || it.Action != ActionStale || !strings.Contains(it.Reason, "registered again") {
		t.Fatalf("dry run = %+v %v", it, err)
	}
	if len(rep.Notes) == 0 || !strings.Contains(strings.Join(rep.Notes, "\n"), "IcyKnoll (/p) has no current registration token") {
		t.Fatalf("notes = %q", rep.Notes)
	}
	rep, err = Takeover(ctx, w.env, false)
	if it := tokenItem(rep); err != nil || rep.Owner.Owner != core.OwnerNhm || it.Action != ActionStale {
		t.Fatalf("takeover = %+v %v", rep, err)
	}
	if _, err := store.ReadSecret(w.env.InstanceDir, w.b.ID); !errors.Is(err, store.ErrNoSecret) {
		t.Fatalf("a stale token was kept: %v", err)
	}

	// nhm's registration hook kept the newer token already: it stays.
	w = newWorld(t)
	w.export(t)
	w.env.Mail.(fakeMail).tokens["IcyKnoll"] = "tok-newer"
	if err := store.WriteSecret(w.env.InstanceDir, w.b.ID, "tok-newer"); err != nil {
		t.Fatal(err)
	}
	rep, err = Takeover(ctx, w.env, false)
	if it := tokenItem(rep); err != nil || it.Action != ActionSame || !strings.Contains(it.Reason, "nhm's own is current") {
		t.Fatalf("takeover = %+v %v", it, err)
	}
	if tok, _ := store.ReadSecret(w.env.InstanceDir, w.b.ID); tok != "tok-newer" {
		t.Fatalf("token = %q", tok)
	}

	// A retired identity is not checked (unretire_agent would bring it
	// back), and its token is kept as before.
	w = newWorld(t)
	w.export(t)
	w.env.Mail.(fakeMail).retired["IcyKnoll"] = true
	rep, err = Takeover(ctx, w.env, false)
	if it := tokenItem(rep); err != nil || it.Action != ActionKept || !strings.Contains(it.Reason, "not checked") {
		t.Fatalf("takeover = %+v %v", it, err)
	}

	// Agent Mail has no such identity: nothing is kept.
	w = newWorld(t)
	w.export(t)
	delete(w.env.Mail.(fakeMail).tokens, "IcyKnoll")
	rep, err = Takeover(ctx, w.env, false)
	if it := tokenItem(rep); err != nil || it.Action != ActionStale || !strings.Contains(it.Reason, "no identity IcyKnoll") {
		t.Fatalf("takeover = %+v %v", it, err)
	}
}

// acfs-9hkh.77 F2: unread mail with no delivery event is new, not handled.
func TestTakeoverTreatsEventlessMailAsNew(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.export(t)
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

func TestTakeoverRefusals(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, w *world)
		want  string
	}{
		{"token unreadable", func(t *testing.T, w *world) {
			w.export(t)
			w.bashFile(t, "tokens/"+ProjectHash("/p")+"/IcyKnoll", "")
		}, "cannot be read"},
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
		for _, p := range []string{"/a", "/b"} {
			w.bashFile(t, "tokens/"+ProjectHash(p)+"/.project", p+"\n")
			w.bashFile(t, "tokens/"+ProjectHash(p)+"/GhostOwl", "tok\n")
		}
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
	actions := map[string]string{}
	for _, it := range rep.Tokens {
		actions[it.Name] = it.Action
	}
	if err != nil || actions["IcyKnoll"] != ActionReplaced {
		t.Fatalf("retry = %+v %v", rep.Tokens, err)
	}
	if tok, _ := store.ReadSecret(w.env.InstanceDir, w.b.ID); tok != "tok-icy" {
		t.Fatalf("token = %q", tok)
	}
}
