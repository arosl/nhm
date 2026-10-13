package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/zoom"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/palette"
)

var t0 = time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)

type fakeHerdr struct {
	mu   sync.Mutex
	snap herdr.SessionSnapshot
	text map[string]string
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, nil
}

func (f *fakeHerdr) PaneRead(_ context.Context, p herdr.PaneReadParams) (herdr.PaneRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return herdr.PaneRead{PaneID: p.PaneID, Text: f.text[p.PaneID]}, nil
}

func u64(v uint64) *uint64 { return &v }

// fixture: workspace w1 with IcyKnoll and BlueLake (bound) and an unbound
// codex whose herdr name carries terminal controls.
func fixture(t *testing.T) (*board.Board, *fakeHerdr) {
	b, h, _ := fixtureStore(t)
	return b, h
}

// fixtureStore is fixture with its bindings store.
func fixtureStore(t *testing.T) (*board.Board, *fakeHerdr, *bindings.Store) {
	t.Helper()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr.sock", "default", t0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bs := bindings.NewStore(db)
	for _, b := range []struct{ name, pane string }{{"IcyKnoll", "w1:p1"}, {"BlueLake", "w1:p2"}} {
		if _, err := bs.Create(context.Background(), bindings.New{MailName: b.name, MailProjectKey: "/p", ProjectID: "proj",
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: b.pane,
				HerdrAlias: strings.ToLower(b.name), Kind: "claude"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := &fakeHerdr{text: map[string]string{"w1:p1": "hello from icy\n\x1b]0;evil title\x07\x1b[2Jdone"},
		snap: herdr.SessionSnapshot{Protocol: 22, Version: "0.9.3",
			Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w1", Number: 1, Label: "swarm"}},
			Agents: []herdr.AgentInfo{
				{PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", Agent: "claude", Name: "icyknoll", AgentStatus: herdr.StatusIdle, StateChangeSeq: u64(1)},
				{PaneID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1", Agent: "claude", Name: "bluelake", AgentStatus: herdr.StatusBlocked, StateChangeSeq: u64(1)},
				{PaneID: "w1:p3", TabID: "w1:t1", WorkspaceID: "w1", Agent: "codex", Name: "\x1b]0;pwned\x07x\x1b[31mred", AgentStatus: herdr.StatusWorking, StateChangeSeq: u64(1)},
			}}}
	b, err := board.New(board.Config{WorkspaceID: "w1", Reader: h, Deps: snapshot.Deps{Herdr: h, DB: db}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, h, bs
}

// settle refreshes the sources, waits for them and lets the model sync.
func settle(t *testing.T, m *Model) {
	t.Helper()
	before := map[string]uint64{}
	for _, h := range m.b.Health() {
		before[h.Source] = h.Reads
	}
	m.b.Refresh()
	deadline := time.Now().Add(5 * time.Second)
	for {
		done := true
		for _, h := range m.b.Health() {
			if h.Reading || h.Reads == before[h.Source] {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sources did not settle")
		}
		time.Sleep(2 * time.Millisecond)
	}
	m.Update(changedMsg{})
}

func newModel(t *testing.T, cfg Config) *Model {
	t.Helper()
	if cfg.Board == nil {
		cfg.Board, _ = fixture(t)
	}
	if cfg.Context.WorkspaceID == "" {
		cfg.Context = ui.Context{WorkspaceID: "w1", WorkspaceLabel: "swarm"}
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	settle(t, m)
	return m
}

func press(m *Model, keys ...string) tea.Cmd {
	var last tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		case "ctrl+w":
			msg = tea.KeyMsg{Type: tea.KeyCtrlW}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, last = m.Update(msg)
	}
	return last
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func plain(m *Model) string { return ansi.Strip(m.View()) }

func TestBoardShowsItsWorkspace(t *testing.T) {
	m := newModel(t, Config{})
	v := plain(m)
	for _, want := range []string{"workspace swarm (w1)", "3 agents", "IcyKnoll", "BlueLake", "▲ blocked", "codex"} {
		if !strings.Contains(v, want) {
			t.Fatalf("view lacks %q:\n%s", want, v)
		}
	}
	// No mail source: the cells say unknown, never 0.
	if strings.Contains(v, "✉ 0") {
		t.Fatalf("a zero inbox shown:\n%s", v)
	}
	if !strings.Contains(v, " ?") {
		t.Fatalf("unknown mail is not shown as ?:\n%s", v)
	}
	// An unbound agent with no name is its kind in its pane.
	if got := rowName(board.Row{Agent: &snapshot.Agent{PaneID: "w1:p9", Kind: "codex"}}); got != "codex in w1:p9" {
		t.Fatalf("rowName = %q", got)
	}
}

// The header's count is "?" before herdr's state answers, never 0, and
// "N of M" under a filter.
func TestHeaderCountsAgents(t *testing.T) {
	b, _ := fixture(t)
	m, err := New(Config{Board: b, Context: ui.Context{WorkspaceID: "w1", WorkspaceLabel: "swarm"}})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if v := plain(m); !strings.Contains(v, "? agents") || strings.Contains(v, "0 agents") {
		t.Fatalf("before the first read:\n%s", v)
	}
	settle(t, m)
	m.b.Sel.SetFilter("icy")
	if v := plain(m); !strings.Contains(v, "1 of 3 agents") {
		t.Fatalf("under a filter:\n%s", v)
	}
}

// Pane, name and title text cannot drive the terminal.
func TestHostileTextIsCleaned(t *testing.T) {
	m := newModel(t, Config{})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.b.Preview(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no preview")
		}
		time.Sleep(2 * time.Millisecond)
	}
	raw := m.View()
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "pwned"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("the view holds %q", bad)
		}
	}
	if !strings.Contains(plain(m), "hello from icy") {
		t.Fatalf("the preview is missing:\n%s", plain(m))
	}
}

// Typed text goes to the filter before any shortcut: q, r and x in the
// filter neither quit, refresh nor mark.
func TestFilterTakesTextFirst(t *testing.T) {
	m := newModel(t, Config{})
	press(m, "/")
	if cmd := press(m, "q", "x", "r", "b", "l", "u", "e"); isQuit(cmd) {
		t.Fatal("q quit while filtering")
	}
	if len(m.b.Sel.Marks()) != 0 {
		t.Fatal("x marked while filtering")
	}
	press(m, "backspace", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	press(m, "b", "l", "u", "e", "enter")
	rows := m.b.Sel.Rows()
	if len(rows) != 1 || rows[0].Name != "BlueLake" {
		t.Fatalf("filtered rows = %+v", rows)
	}
	press(m, "/", "z", "z", "esc")
	if m.b.Sel.Filter() != "blue" {
		t.Fatalf("esc did not put the filter back: %q", m.b.Sel.Filter())
	}
	press(m, "esc")
	if m.b.Sel.Filter() != "" || len(m.b.Sel.Rows()) != 3 {
		t.Fatalf("esc did not clear the filter: %q", m.b.Sel.Filter())
	}
}

func TestMarksAndQuit(t *testing.T) {
	m := newModel(t, Config{})
	press(m, "x", "j", "x")
	if n := len(m.b.Sel.Marks()); n != 2 || !strings.Contains(plain(m), "2 marked") {
		t.Fatalf("marks = %d\n%s", n, plain(m))
	}
	press(m, "esc")
	if len(m.b.Sel.Marks()) != 0 {
		t.Fatal("esc kept the marks")
	}
	if !isQuit(press(m, "q")) {
		t.Fatal("q did not quit")
	}
	if !isQuit(press(m, "ctrl+c")) {
		t.Fatal("ctrl+c did not quit")
	}
}

// enter zooms the cursor's pane in the board's workspace, in a command;
// without a zoom call, enter is not a key at all.
func TestZoom(t *testing.T) {
	var got []string
	m := newModel(t, Config{Zoom: func(_ context.Context, ws, pane string) error {
		got = append(got, ws+"/"+pane)
		return nil
	}})
	press(m, "j")
	cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("enter started nothing")
	}
	m.Update(cmd())
	if len(got) != 1 || got[0] != "w1/w1:p2" || !strings.Contains(plain(m), "zoomed BlueLake") {
		t.Fatalf("zoomed %v\n%s", got, plain(m))
	}
	// The pane is zoomed, but its tab did not come in front: the board
	// says so, and does not call it a failed zoom.
	unshown := newModel(t, Config{Zoom: func(_ context.Context, ws, pane string) error {
		return &zoom.Unshown{PaneID: pane, Reason: "no such tab"}
	}})
	unshown.Update(press(unshown, "enter")())
	if v := plain(unshown); !strings.Contains(v, "zoomed IcyKnoll, but herdr did not bring its tab in front: no such tab") ||
		strings.Contains(v, "zoom IcyKnoll:") {
		t.Fatalf("a zoom without its tab:\n%s", v)
	}
	// A failed zoom says so, and never says zoomed.
	failed := newModel(t, Config{Zoom: func(context.Context, string, string) error {
		return errors.New("pane w1:p1 not found")
	}})
	failed.Update(press(failed, "enter")())
	if v := plain(failed); !strings.Contains(v, "zoom IcyKnoll: pane w1:p1 not found") || strings.Contains(v, "zoomed") {
		t.Fatalf("a failed zoom:\n%s", v)
	}

	off := newModel(t, Config{})
	if cmd := press(off, "enter"); cmd != nil {
		t.Fatal("enter did something without a zoom call")
	}
	if strings.Contains(plain(off), "zoom") {
		t.Fatalf("the help shows a dead zoom key:\n%s", plain(off))
	}
}

// i peeks at the cursor's inbox; an answer to an older request is
// dropped.
func TestInboxPeek(t *testing.T) {
	calls := 0
	m := newModel(t, Config{Inbox: func(_ context.Context, project, name string) ([]agentmail.Message, error) {
		calls++
		return []agentmail.Message{{ID: 1, From: "BlueLake", Subject: "review \x1b]0;x\x07done", Importance: "urgent", AckRequired: true}}, nil
	}})
	cmd := press(m, "i")
	if cmd == nil || m.view != viewInbox || !strings.Contains(plain(m), "reading the inbox") {
		t.Fatalf("i did not open the inbox:\n%s", plain(m))
	}
	stale := inboxMsg{seq: m.inbox.seq - 1, err: errors.New("old")}
	m.Update(stale)
	if !m.inbox.loading {
		t.Fatal("a stale answer was taken")
	}
	m.Update(cmd())
	v := plain(m)
	if calls != 1 || !strings.Contains(v, "review done") || !strings.Contains(v, "nothing is marked read") || strings.Contains(m.View(), "\x1b]0;x") {
		t.Fatalf("inbox view:\n%s", v)
	}
	press(m, "esc")
	if m.view != viewMain {
		t.Fatal("esc did not close the inbox")
	}
	// The unbound codex has no inbox.
	press(m, "j", "j")
	if cmd := press(m, "i"); cmd != nil || m.view != viewMain {
		t.Fatal("an unbound agent's inbox was opened")
	}
}

func TestViewsAndSmallSizes(t *testing.T) {
	m := newModel(t, Config{})
	for _, k := range []string{"d", "$", "?"} {
		press(m, k)
		if m.view == viewMain {
			t.Fatalf("%s opened no view", k)
		}
		for _, size := range [][2]int{{120, 30}, {40, 10}, {20, 5}} {
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			if v := m.View(); v == "" {
				t.Fatalf("%s at %v: empty view", k, size)
			}
		}
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
		press(m, "esc")
	}
	press(m, "d")
	if v := plain(m); !strings.Contains(v, "state") || !strings.Contains(v, "herdr 0.9.3") {
		t.Fatalf("diagnostics:\n%s", v)
	}
	press(m, "esc", "$")
	if v := plain(m); !strings.Contains(v, "no quota observation yet") {
		t.Fatalf("usage:\n%s", v)
	}
}

// A table taller than the screen scrolls with the cursor; the cursor's
// row is always on screen.
func TestTableKeepsTheCursorInView(t *testing.T) {
	b, h := fixture(t)
	h.mu.Lock()
	for i := 4; i <= 30; i++ {
		h.snap.Agents = append(h.snap.Agents, herdr.AgentInfo{PaneID: "w1:p" + strconv.Itoa(i), TabID: "w1:t2",
			WorkspaceID: "w1", Agent: "codex", Name: "agent" + strconv.Itoa(i), AgentStatus: herdr.StatusIdle})
	}
	h.mu.Unlock()
	m := newModel(t, Config{Board: b})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for range len(m.b.Sel.Rows()) {
		r, _ := m.b.Sel.Cursor()
		if v := plain(m); !strings.Contains(v, board.Clean(rowName(r))) || !strings.Contains(v, "rows ") {
			t.Fatalf("cursor on %s, not in view:\n%s", rowName(r), v)
		}
		press(m, "j")
	}
}

// After the binary changes on disk, the board refuses new mutations at
// once, with no tick between, and says so. The palette and the composer
// do not open either: they would send.
func TestUpgradeFreezes(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "nhm")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := board.WatchBinary(bin)
	if err != nil {
		t.Fatal(err)
	}
	ic := &interruptCore{}
	palettes := 0
	m := newModel(t, Config{Binary: w, Core: ic.core(), NewOperationID: opIDs(),
		OpenPalette: func(context.Context, ui.Context) error { palettes++; return nil }})
	m.Update(tickMsg(time.Now()))
	if m.Frozen() != nil {
		t.Fatal("frozen before an upgrade")
	}
	if err := os.WriteFile(bin+".new", []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(m.Frozen(), board.ErrUpgraded) {
		t.Fatal("not frozen before the next tick")
	}
	m.Update(tickMsg(time.Now().Add(2 * time.Second)))
	if !strings.Contains(plain(m), "upgraded on disk") {
		t.Fatalf("no banner:\n%s", plain(m))
	}
	for _, k := range []string{"x", "s", "a"} {
		cmd := press(m, "space", k)
		runAll(m, cmd)
		if m.view != viewMain || !strings.Contains(plain(m), "reopen the board") {
			t.Fatalf("space %s on a frozen board: view %d\n%s", k, m.view, plain(m))
		}
	}
	if len(ic.asked) != 0 || palettes != 0 {
		t.Fatalf("a frozen board acted: %v, %d palettes", ic.asked, palettes)
	}
}

// watchedBinary is a binary the board watches, and the install that
// replaces it.
func watchedBinary(t *testing.T) (*board.Binary, func()) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "nhm")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := board.WatchBinary(bin)
	if err != nil {
		t.Fatal(err)
	}
	return w, func() {
		t.Helper()
		if err := os.WriteFile(bin+".new", []byte("v2"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(bin+".new", bin); err != nil {
			t.Fatal(err)
		}
	}
}

// A form opened before an upgrade refuses its confirm after it: the
// board's form checks the binary at the confirm (finding 5's case).
func TestUpgradeRefusesAnOpenFormsConfirm(t *testing.T) {
	w, upgrade := watchedBinary(t)
	ic := &interruptCore{}
	m := newModel(t, Config{Binary: w, Core: ic.core(), NewOperationID: opIDs()})
	press(m, "space", "x")
	if m.view != viewForm {
		t.Fatalf("no form:\n%s", plain(m))
	}
	upgrade()
	runAll(m, press(m, "right", "enter"))
	if len(ic.asked) != 0 || !strings.Contains(plain(m), "reopen the board") {
		t.Fatalf("the confirm after an upgrade: asked %v\n%s", ic.asked, plain(m))
	}
}

// After an upgrade, ctrl+w opens no wizard; and a wizard opened before it
// spawns nothing: the board's core checks the binary at the spawn.
func TestUpgradeFreezesTheWizard(t *testing.T) {
	w, upgrade := watchedBinary(t)
	spawns := 0
	core := actions.Core{Spawn: func(context.Context, actions.SpawnRequest, string) (spawn.Result, error) {
		spawns++
		return spawn.Result{}, nil
	}}
	m := newModel(t, Config{Binary: w, Core: core, NewOperationID: opIDs(), Kinds: kinds.All()})
	runAll(m, press(m, "ctrl+w"))
	if m.view != viewWizard {
		t.Fatalf("ctrl+w: view %d\n%s", m.view, plain(m))
	}
	upgrade()
	// The wizard runs on m.cfg.Core; its spawn is refused at the call.
	if _, err := m.cfg.Core.Spawn(context.Background(), actions.SpawnRequest{}, "op-1"); !errors.Is(err, board.ErrUpgraded) || spawns != 0 {
		t.Fatalf("the open wizard's spawn: %v, %d spawns", err, spawns)
	}
	runAll(m, press(m, "esc"))
	runAll(m, press(m, "ctrl+w"))
	if m.view != viewMain || m.wizard != nil || !strings.Contains(plain(m), "reopen the board") {
		t.Fatalf("ctrl+w on a frozen board: view %d\n%s", m.view, plain(m))
	}
}

// A board that cannot watch its binary is frozen from the start, and
// says why.
func TestUnwatchedBoardTakesNoActions(t *testing.T) {
	ic := &interruptCore{}
	m := newModel(t, Config{Binary: board.Unwatched(errors.New("no launch path")), Core: ic.core(), NewOperationID: opIDs()})
	m.Update(tickMsg(time.Now()))
	if !errors.Is(m.Frozen(), board.ErrUnwatched) || !strings.Contains(plain(m), "cannot be watched") {
		t.Fatalf("not frozen:\n%s", plain(m))
	}
	press(m, "space", "x")
	if m.view != viewMain || len(ic.asked) != 0 {
		t.Fatalf("view %d, asked %v", m.view, ic.asked)
	}
}

// interruptCore is a core that interrupts, and records what it was asked.
type interruptCore struct {
	mu     sync.Mutex
	asked  []map[string]int64
	ops    []string
	gate   chan struct{} // when set, the interrupt waits for it
	result wait.InterruptResult
}

func (c *interruptCore) core() actions.Core {
	return actions.Core{Interrupt: func(_ context.Context, expect map[string]int64, opID string) (wait.InterruptResult, error) {
		if c.gate != nil {
			<-c.gate
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.asked, c.ops = append(c.asked, expect), append(c.ops, opID)
		res := wait.InterruptResult{OperationID: opID, Status: "sent"}
		for id := range expect {
			res.Targets = append(res.Targets, wait.InterruptTarget{BindingID: id, Status: wait.InterruptSent})
		}
		return res, nil
	}}
}

func opIDs() func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		return "op-" + strconv.Itoa(n), nil
	}
}

// space lists the action menu with each reason; x interrupts the marked
// agents, frozen at the review, once.
func TestActionMenuInterruptsTheMarks(t *testing.T) {
	ic := &interruptCore{}
	m := newModel(t, Config{Core: ic.core(), NewOperationID: opIDs()})
	press(m, "x", "j", "x", "space")
	v := plain(m)
	if m.view != viewActions || !strings.Contains(v, "Actions for 2 marked agents") || !strings.Contains(v, "x  interrupt") ||
		!strings.Contains(v, "this surface cannot retire") {
		t.Fatalf("action menu:\n%s", v)
	}
	press(m, "x")
	if m.view != viewForm || !strings.Contains(plain(m), "IcyKnoll") || !strings.Contains(plain(m), "BlueLake") {
		t.Fatalf("form:\n%s", plain(m))
	}
	cmd := press(m, "right", "enter")
	if cmd == nil {
		t.Fatal("confirm started nothing")
	}
	m.Update(cmd())
	if len(ic.asked) != 1 || len(ic.asked[0]) != 2 || ic.ops[0] != "op-1" {
		t.Fatalf("asked %v, ops %v", ic.asked, ic.ops)
	}
	if r, ok := m.b.Results.Get("op-1"); !ok || r.Outcome != "sent" || len(r.Targets) != 2 {
		t.Fatalf("result = %+v", r)
	}
	press(m, "enter") // close the result
	if m.view != viewMain || !strings.Contains(plain(m), "interrupt op-1: sent") {
		t.Fatalf("after the form:\n%s", plain(m))
	}
}

// A mark whose agent was recycled since refuses the action; it is never
// swapped for the new generation.
func TestActionMenuRefusesAReplacedMark(t *testing.T) {
	b, _, bs := fixtureStore(t)
	ic := &interruptCore{}
	m := newModel(t, Config{Board: b, Core: ic.core(), NewOperationID: opIDs()})
	press(m, "x")
	icy, err := bs.ActiveByPane(context.Background(), "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Occupy(context.Background(), icy.ID, icy.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
		PaneID: "w1:p1", HerdrAlias: "icyknoll"}); err != nil {
		t.Fatal(err)
	}
	settle(t, m)
	press(m, "space", "x")
	if m.view != viewMain || !strings.Contains(plain(m), "no longer name their agent") || len(ic.asked) != 0 {
		t.Fatalf("view %d, asked %v:\n%s", m.view, ic.asked, plain(m))
	}
}

// space freezes the cursor's agent. When it is recycled while the menu
// is open, the cursor moves to the row now in its place, but no action
// takes that row: each refuses, and nothing is sent or opened. A new
// space acts on the board as it is now.
func TestActionMenuFreezesTheCursor(t *testing.T) {
	b, _, bs := fixtureStore(t)
	ic, sc := &interruptCore{}, &sendCore{}
	core := ic.core()
	core.SendBase, core.Send = sc.core().SendBase, sc.core().Send
	var palettes []ui.Context
	m := newModel(t, Config{Board: b, Core: core, NewOperationID: opIDs(),
		OpenPalette: func(_ context.Context, c ui.Context) error { palettes = append(palettes, c); return nil }})
	icy, err := bs.ActiveByPane(context.Background(), "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	recycle := func() {
		t.Helper()
		cur, err := bs.Get(context.Background(), icy.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bs.Occupy(context.Background(), cur.ID, cur.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p1", HerdrAlias: "icyknoll"}); err != nil {
			t.Fatal(err)
		}
		settle(t, m)
	}
	for _, k := range []string{"a", "s", "x"} {
		press(m, "space")
		recycle()
		if r, _ := m.b.Sel.Cursor(); r.Key.Generation == m.menuTargets.Rows[0].Key.Generation {
			t.Fatal("the recycle did not move the cursor to the new generation")
		}
		cmd := press(m, k)
		runAll(m, cmd)
		if m.view != viewMain || !strings.Contains(plain(m), "no longer on the board as it was") {
			t.Fatalf("space %s after a recycle: view %d\n%s", k, m.view, plain(m))
		}
	}
	if len(palettes) != 0 || len(sc.reqs) != 0 || len(ic.asked) != 0 {
		t.Fatalf("an action took the row in its place: %d palettes, %d sends, %d interrupts", len(palettes), len(sc.reqs), len(ic.asked))
	}
	cmd := press(m, "space", "a")
	runAll(m, cmd)
	cur, _ := m.b.Sel.Cursor()
	if len(palettes) != 1 {
		t.Fatalf("a new space opened %d palettes", len(palettes))
	}
	if ts, _, _ := palettes[0].Selection(); len(ts) != 1 || ts[0].Generation != cur.Key.Generation {
		t.Fatalf("a new space: selection %+v, cursor %+v", ts, cur.Key)
	}
}

// The cursor on an unbound agent: interrupt refuses it (bound agents
// only), with the table's reason.
func TestActionMenuRefusesAnUnboundAgent(t *testing.T) {
	ic := &interruptCore{}
	m := newModel(t, Config{Core: ic.core(), NewOperationID: opIDs()})
	press(m, "j", "j", "space", "x")
	if m.view != viewMain || !strings.Contains(plain(m), "has no binding") {
		t.Fatalf("view %d:\n%s", m.view, plain(m))
	}
}

// A form reviewed and then closed leaves no result: the result begins at
// the submission, not at the review that gave the form its id.
func TestReviewedThenClosedFormLeavesNoResult(t *testing.T) {
	ic := &interruptCore{}
	m := newModel(t, Config{Core: ic.core(), NewOperationID: opIDs()})
	press(m, "space", "x")
	if m.view != viewForm || m.form == nil || m.form.OperationID() == "" {
		t.Fatalf("no reviewed form:\n%s", plain(m))
	}
	id := m.form.OperationID()
	press(m, "esc")
	if m.view != viewMain || len(ic.asked) != 0 {
		t.Fatalf("view %d, asked %v", m.view, ic.asked)
	}
	if r, ok := m.b.Results.Get(id); ok {
		t.Fatalf("a closed review left a result: %+v", r)
	}
}

// The action menu says why its targets do not fit an action before the
// pick: adopt on a bound agent, adopt on one with no agent session,
// interrupt on an unbound one.
func TestActionMenuSaysWhyTheTargetsDoNotFit(t *testing.T) {
	ic := &interruptCore{}
	core := ic.core()
	core.Adopt = func(context.Context, bindings.AdoptRequest) (bindings.AdoptResult, error) {
		return bindings.AdoptResult{}, errors.New("not in this test")
	}
	m := newModel(t, Config{Core: core, NewOperationID: opIDs()})
	press(m, "space")
	if v := plain(m); !strings.Contains(v, "adopt") || !strings.Contains(v, "takes an agent with no binding") {
		t.Fatalf("adopt on IcyKnoll:\n%s", v)
	}
	press(m, "esc", "j", "j", "space")
	v := plain(m)
	if !strings.Contains(v, "herdr reports no agent session") || !strings.Contains(v, "has no binding") {
		t.Fatalf("the unbound codex without a session:\n%s", v)
	}
}

// esc while the job runs closes the form, not the job: the job still
// runs, and the board says recent operations has its outcome.
func TestClosedFormKeepsItsJob(t *testing.T) {
	ic := &interruptCore{gate: make(chan struct{})}
	m := newModel(t, Config{Core: ic.core(), NewOperationID: opIDs()})
	press(m, "space", "x")
	cmd := press(m, "right", "enter")
	press(m, "esc")
	if m.view != viewMain || m.form != nil || !strings.Contains(plain(m), "runs on") {
		t.Fatalf("view %d:\n%s", m.view, plain(m))
	}
	if r, ok := m.b.Results.Get("op-1"); !ok || r.Outcome != board.OutcomePending {
		t.Fatalf("result before the answer = %+v", r)
	}
	close(ic.gate)
	m.Update(cmd())
	if len(ic.asked) != 1 || ic.ops[0] != "op-1" {
		t.Fatalf("the job did not run: %v", ic.asked)
	}
}

// A wide board shows attention beside the table: tab moves the focus to
// it, the arrows move its selection, and enter zooms the item's pane. A
// narrow board has no room: tab is off and the header counts attention.
func TestAttentionBesideTheTable(t *testing.T) {
	var zoomed []string
	m := newModel(t, Config{Zoom: func(_ context.Context, ws, pane string) error {
		zoomed = append(zoomed, pane)
		return nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 30})
	if v := plain(m); !strings.Contains(v, "Attention") || !strings.Contains(v, "BlueLake waits for an answer") {
		t.Fatalf("wide view:\n%s", v)
	}
	press(m, "tab")
	if m.focusedPanel != PanelAttention {
		t.Fatal("tab did not focus attention")
	}
	// The blocked BlueLake (action) sorts first; down selects the codex.
	press(m, "j")
	if it := m.attention.SelectedItem(); it == nil || !strings.Contains(it.Summary, "no binding") {
		t.Fatalf("selected = %+v", it)
	}
	if r, _ := m.b.Sel.Cursor(); r.Name != "IcyKnoll" {
		t.Fatalf("the arrows moved the table too: %s", r.Name)
	}
	cmd := press(m, "enter")
	m.Update(cmd())
	if len(zoomed) != 1 || zoomed[0] != "w1:p3" {
		t.Fatalf("zoomed %v", zoomed)
	}
	press(m, "tab")
	if m.focusedPanel != PanelPaneList {
		t.Fatal("tab did not come back to the table")
	}
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	if m.focusedPanel != PanelPaneList || m.keys.Tab.Enabled() {
		t.Fatal("a narrow board keeps a hidden panel's focus or tab")
	}
	if v := plain(m); strings.Contains(v, "Attention") || !strings.Contains(v, "attention: 1 action") {
		t.Fatalf("narrow view:\n%s", v)
	}
}

// A tall, wide board shows this workspace's send history under
// attention; its keys work while it has the focus, and q in its preview
// closes the preview, not the board.
func TestHistoryPanelOnTheBoard(t *testing.T) {
	now := time.Now().UTC()
	m := newModel(t, Config{History: func() ([]history.HistoryEntry, error) {
		return []history.HistoryEntry{
			{ID: "old1", Session: "swarm", Prompt: "older prompt", Targets: []string{"0.1"}, AgentTypes: []string{"claude"}, Success: true, Timestamp: now.Add(-time.Hour)},
			{ID: "new1", Session: "w1", Prompt: "newer prompt", Targets: []string{"0.2"}, AgentTypes: []string{"claude"}, Success: false, Timestamp: now},
			{ID: "else", Session: "other", Prompt: "another workspace", Targets: []string{"0.1"}, Success: true, Timestamp: now},
		}, nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(m.readHistory()())
	m.Update(changedMsg{})
	v := plain(m)
	if !strings.Contains(v, "Command History") || !strings.Contains(v, "newer prompt") || !strings.Contains(v, "older prompt") ||
		strings.Contains(v, "another workspace") {
		t.Fatalf("view:\n%s", v)
	}
	press(m, "tab", "tab")
	if m.focusedPanel != PanelHistory {
		t.Fatalf("focus = %v", m.focusedPanel)
	}
	press(m, "v")
	if !m.history.PreviewOpen() {
		t.Fatal("v did not open the preview")
	}
	if cmd := press(m, "q"); isQuit(cmd) || m.history.PreviewOpen() {
		t.Fatal("q in the preview quit the board, or kept the preview")
	}
	if !isQuit(press(m, "q")) {
		t.Fatal("q outside the preview did not quit")
	}
}

// space r reads the cursor's pane, cleaned and bounded; it follows the
// pane on the tick until p pauses it; scrolling back pauses it too; two
// marked agents are refused; an older read's answer is dropped.
func TestReadView(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	text := "line 1\nline 2\n\x1b]0;evil\x07\x1b[2Jline 3"
	m := newModel(t, Config{Read: func(_ context.Context, pane string, lines int) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if pane != "w1:p1" || lines != ReadLines {
			return "", errors.New("wrong pane " + pane)
		}
		return text, nil
	}})
	cmd := press(m, "space", "r")
	if m.view != viewRead || cmd == nil {
		t.Fatalf("view %d", m.view)
	}
	m.Update(cmd())
	raw := m.View()
	if v := ansi.Strip(raw); !strings.Contains(v, "read IcyKnoll · w1:p1 · following") || !strings.Contains(v, "line 3") ||
		strings.Contains(raw, "evil") || strings.Contains(raw, "\x1b[2J") {
		t.Fatalf("read view:\n%s", ansi.Strip(raw))
	}
	// The tick reads again while following.
	_, tick := m.Update(tickMsg(time.Now().Add(2 * time.Second)))
	runAll(m, tick)
	if reads != 2 {
		t.Fatalf("%d reads after a tick", reads)
	}
	press(m, "p")
	_, tick = m.Update(tickMsg(time.Now().Add(10 * time.Second)))
	runAll(m, tick)
	if reads != 2 || !strings.Contains(plain(m), "paused") {
		t.Fatalf("%d reads while paused:\n%s", reads, plain(m))
	}
	stale := readMsg{seq: m.read.seq - 1, text: "stale"}
	m.Update(stale)
	if strings.Contains(plain(m), "stale") {
		t.Fatal("an older read was taken")
	}
	press(m, "esc")
	if m.view != viewMain {
		t.Fatal("esc did not close the read view")
	}
	press(m, "x", "j", "x", "space", "r")
	if m.view != viewMain || !strings.Contains(plain(m), "read takes one agent") {
		t.Fatalf("two marks:\n%s", plain(m))
	}
}

// A late read of the agent read before never shows under the next
// agent's title: the reads' sequence goes on across opens.
func TestReadViewDropsTheLastAgentsRead(t *testing.T) {
	m := newModel(t, Config{Read: func(_ context.Context, pane string, _ int) (string, error) {
		return "the screen of " + pane, nil
	}})
	first := press(m, "space", "r")
	if m.view != viewRead || !strings.Contains(plain(m), "read IcyKnoll") || first == nil {
		t.Fatalf("read A:\n%s", plain(m))
	}
	press(m, "esc", "j")
	second := press(m, "space", "r")
	if m.view != viewRead || !strings.Contains(plain(m), "read BlueLake") || second == nil {
		t.Fatalf("read B:\n%s", plain(m))
	}
	m.Update(second())
	m.Update(first()) // A's read answers last
	if v := plain(m); !strings.Contains(v, "the screen of w1:p2") || strings.Contains(v, "the screen of w1:p1") {
		t.Fatalf("B's read view:\n%s", v)
	}
}

// sendCore is a core that sends, and records what it was asked.
type sendCore struct {
	mu   sync.Mutex
	reqs []send.Request
}

func (c *sendCore) core() actions.Core {
	return actions.Core{
		SendBase: func(context.Context) (send.Request, error) { return send.Request{Session: "w1"}, nil },
		Send: func(_ context.Context, req send.Request) (send.Result, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.reqs = append(c.reqs, req)
			res := send.Result{OperationID: req.OperationID}
			for id := range req.Expect {
				res.Targets = append(res.Targets, send.Receipt{BindingID: id, MailName: "Name-" + id, Status: send.StatusSubmitted})
			}
			return res, nil
		},
	}
}

// space s composes in the palette's editor and sends the text once to
// the frozen targets, by pane and expected generation; an empty draft is
// refused by the editor and sends nothing.
func TestSendFromTheBoard(t *testing.T) {
	sc := &sendCore{}
	m := newModel(t, Config{Core: sc.core(), NewOperationID: opIDs()})
	press(m, "x", "j", "x", "space", "s")
	if m.view != viewSend || !strings.Contains(plain(m), "to IcyKnoll, BlueLake") {
		t.Fatalf("composer:\n%s", plain(m))
	}
	ctrlS := tea.KeyMsg{Type: tea.KeyCtrlS}
	if _, cmd := m.Update(ctrlS); cmd != nil {
		// The editor answers an empty draft with a notice, no message.
		if msg := cmd(); msg != nil {
			if _, ok := msg.(palette.ComposedMsg); ok {
				t.Fatal("an empty draft was composed")
			}
		}
	}
	press(m, "h", "e", "l", "l", "o")
	_, cmd := m.Update(ctrlS)
	if cmd == nil {
		t.Fatal("ctrl+s composed nothing")
	}
	_, submit := m.Update(cmd())
	if submit == nil || !m.send.running {
		t.Fatalf("not submitted:\n%s", plain(m))
	}
	m.Update(submit())
	if len(sc.reqs) != 1 {
		t.Fatalf("%d sends", len(sc.reqs))
	}
	req := sc.reqs[0]
	if req.Message != "hello" || req.OperationID != "op-1" || len(req.Expect) != 2 || len(req.Selector.Panes) != 2 {
		t.Fatalf("send request = %+v", req)
	}
	if v := plain(m); !strings.Contains(v, "submitted (op-1)") || !strings.Contains(v, "Name-") {
		t.Fatalf("result:\n%s", v)
	}
	if r, ok := m.b.Results.Get("op-1"); !ok || r.Outcome != "submitted" {
		t.Fatalf("board result = %+v", r)
	}
	press(m, "enter")
	if m.view != viewMain || len(sc.reqs) != 1 {
		t.Fatalf("view %d, %d sends", m.view, len(sc.reqs))
	}
}

// Two ctrl+s before the first is taken send once. Once the answer is in,
// the guard lets the same text be sent again, as a new operation.
func TestSendSubmitsOnceAndSettles(t *testing.T) {
	sc := &sendCore{}
	m := newModel(t, Config{Core: sc.core(), NewOperationID: opIDs()})
	press(m, "space", "s", "h", "i")
	// Other text the second time: the guard alone would refuse the same
	// text, so only the composer's one submission refuses this one.
	_, first := m.Update(palette.ComposedMsg{Text: "hi"})
	_, second := m.Update(palette.ComposedMsg{Text: "hi!"})
	if first == nil || second != nil {
		t.Fatalf("submits: first %v, second %v", first != nil, second != nil)
	}
	m.Update(first())
	press(m, "enter")
	press(m, "space", "s", "h", "i")
	_, again := m.Update(palette.ComposedMsg{Text: "hi"})
	if again == nil {
		t.Fatalf("the same text after its answer was refused:\n%s", plain(m))
	}
	m.Update(again())
	if len(sc.reqs) != 2 || sc.reqs[0].OperationID != "op-1" || sc.reqs[1].OperationID != "op-2" {
		t.Fatalf("sends = %+v", sc.reqs)
	}
}

// A lost answer keeps the guard's hold, so the same text is not sent
// again while its job may still run. The board keeps one guard for its
// life: once the job has ended (OperationEnded), the hold is released,
// and the text goes as a new operation.
func TestLostAnswerIsReleasedWhenItsJobEnded(t *testing.T) {
	ended := map[string]bool{}
	var reqs []send.Request
	core := actions.Core{
		SendBase: func(context.Context) (send.Request, error) { return send.Request{Session: "w1"}, nil },
		Send: func(_ context.Context, req send.Request) (send.Result, error) {
			reqs = append(reqs, req)
			return send.Result{}, fmt.Errorf("%w: the job died", actions.ErrNoAnswer)
		},
	}
	m := newModel(t, Config{Core: core, NewOperationID: opIDs(), OperationEnded: func(id string) bool { return ended[id] }})
	press(m, "space", "s", "h", "i")
	_, first := m.Update(palette.ComposedMsg{Text: "hi"})
	if first == nil {
		t.Fatal("the first send started nothing")
	}
	m.Update(first())
	press(m, "enter") // close the result
	press(m, "space", "s", "h", "i")
	if _, again := m.Update(palette.ComposedMsg{Text: "hi"}); again != nil {
		t.Fatalf("the same text was sent again while op-1 may run:\n%s", plain(m))
	}
	ended["op-1"] = true
	_, again := m.Update(palette.ComposedMsg{Text: "hi"})
	if again == nil {
		t.Fatalf("the hold outlived its ended job:\n%s", plain(m))
	}
	m.Update(again())
	if len(reqs) != 2 || reqs[0].OperationID != "op-1" || reqs[1].OperationID != "op-2" {
		t.Fatalf("sends = %+v", reqs)
	}
}

// ctrl+s and esc in one burst, their answers in reverse order: the
// composer closed first, so its text is not sent.
func TestComposedTextAfterEscSendsNothing(t *testing.T) {
	sc := &sendCore{}
	m := newModel(t, Config{Core: sc.core(), NewOperationID: opIDs()})
	press(m, "space", "s", "h", "i")
	m.Update(palette.ComposeCanceledMsg{})
	if _, cmd := m.Update(palette.ComposedMsg{Text: "hi"}); cmd != nil || m.view != viewMain {
		t.Fatalf("a closed composer submitted (view %d)", m.view)
	}
	if len(sc.reqs) != 0 || !strings.Contains(plain(m), "nothing was sent") {
		t.Fatalf("sends %+v\n%s", sc.reqs, plain(m))
	}
}

// space a opens the palette on exactly the board's frozen selection, an
// explicit one; with no agent it opens nothing.
func TestPaletteFromTheBoard(t *testing.T) {
	var got []ui.Context
	m := newModel(t, Config{OpenPalette: func(_ context.Context, c ui.Context) error {
		got = append(got, c)
		return nil
	}})
	press(m, "x", "j", "x")
	cmd := press(m, "space", "a")
	if cmd == nil || m.view != viewMain {
		t.Fatalf("space a started nothing (view %d)", m.view)
	}
	m.Update(cmd())
	if len(got) != 1 {
		t.Fatalf("%d palettes", len(got))
	}
	ts, pick, err := got[0].Selection()
	if err != nil || pick || len(ts) != 2 || got[0].Origin != "board" || ts[0].Generation < 1 {
		t.Fatalf("selection %+v, pick %v, err %v, origin %q", ts, pick, err, got[0].Origin)
	}
	if !strings.Contains(plain(m), "the palette opened for 2 agents") {
		t.Fatalf("status:\n%s", plain(m))
	}
	m.b.Sel.ClearMarks()
	m.b.Sel.SetFilter("nobody")
	if cmd := press(m, "space", "a"); cmd != nil || len(got) != 1 {
		t.Fatal("the palette opened with no agent")
	}
}

// runAll runs a command, batches too, and hands each message back.
func runAll(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runAll(m, c)
		}
		return
	}
	if _, isTick := msg.(tickMsg); isTick {
		return // the next tick is the test's to send
	}
	m.Update(msg)
}

// Disabled keys do nothing: space, ctrl+w and tab until their actions
// exist.
func TestNoDeadKeys(t *testing.T) {
	m := newModel(t, Config{})
	before := plain(m)
	if cmd := press(m, "space"); cmd != nil || m.view != viewMain {
		t.Fatal("space did something")
	}
	if cmd := press(m, "ctrl+w"); cmd != nil || m.view != viewMain {
		t.Fatal("ctrl+w did something with no spawn")
	}
	if strings.Contains(before, "actions") || strings.Contains(before, "spawn") {
		t.Fatalf("the help shows keys that do nothing:\n%s", before)
	}
}

// ctrl+w opens ntm's spawn wizard for this workspace; esc closes it and
// spawns nothing.
func TestSpawnWizardFromTheBoard(t *testing.T) {
	var spawns []actions.SpawnRequest
	core := actions.Core{Spawn: func(_ context.Context, req actions.SpawnRequest, _ string) (spawn.Result, error) {
		spawns = append(spawns, req)
		return spawn.Result{}, nil
	}}
	m := newModel(t, Config{Core: core, NewOperationID: opIDs(), Kinds: kinds.All()})
	press(m, "?")
	if !strings.Contains(plain(m), "spawn wizard") {
		t.Fatalf("the help does not show ctrl+w:\n%s", plain(m))
	}
	press(m, "esc")
	runAll(m, press(m, "ctrl+w"))
	if m.view != viewWizard || !strings.Contains(plain(m), "Spawn Wizard") {
		t.Fatalf("ctrl+w: view %d\n%s", m.view, plain(m))
	}
	runAll(m, press(m, "esc"))
	if m.view != viewMain || m.wizard != nil || len(spawns) != 0 {
		t.Fatalf("esc: view %d, %d spawns\n%s", m.view, len(spawns), plain(m))
	}
}

// The wizard's spawn checks the binary at the call: a frozen board's
// spawn is refused and never reaches the core; its dry run still runs.
func TestFrozenSpawnRefusesTheSpawn(t *testing.T) {
	calls := 0
	run := func(context.Context, actions.SpawnRequest, string) (spawn.Result, error) {
		calls++
		return spawn.Result{}, nil
	}
	wrapped := frozenSpawn(board.Unwatched(errors.New("no launch path")), run)
	if _, err := wrapped(context.Background(), actions.SpawnRequest{}, "op-1"); !errors.Is(err, board.ErrUnwatched) || calls != 0 {
		t.Fatalf("spawn on a frozen board: %v, %d calls", err, calls)
	}
	if _, err := wrapped(context.Background(), actions.SpawnRequest{DryRun: true}, ""); err != nil || calls != 1 {
		t.Fatalf("dry run on a frozen board: %v, %d calls", err, calls)
	}
}
