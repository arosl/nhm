package dashboard

import (
	"context"
	"errors"
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
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/board"
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
	return b, h
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
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
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

// After the binary changes on disk, the board says so and refuses new
// mutations.
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
	m := newModel(t, Config{Binary: w})
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
	m.Update(tickMsg(time.Now().Add(2 * time.Second)))
	if !errors.Is(m.Frozen(), board.ErrUpgraded) || !strings.Contains(plain(m), "upgraded on disk") {
		t.Fatalf("not frozen:\n%s", plain(m))
	}
}

// Disabled keys do nothing: space, ctrl+w and tab until their actions
// exist.
func TestNoDeadKeys(t *testing.T) {
	m := newModel(t, Config{})
	before := plain(m)
	if cmd := press(m, "space"); cmd != nil || m.view != viewMain {
		t.Fatal("space did something")
	}
	if strings.Contains(before, "actions") || strings.Contains(before, "spawn") {
		t.Fatalf("the help shows keys that do nothing:\n%s", before)
	}
}
