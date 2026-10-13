package uilaunch

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

var t0 = time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC)

// The real client is an Opener.
var _ Opener = (*herdr.Client)(nil)

// fakeHerdr is a herdr server with panes, each with a shell pid.
type fakeHerdr struct {
	mu         sync.Mutex
	workspaces []string
	panes      map[string]string // pane id -> workspace id
	shells     map[string]int    // pane id -> shell pid
	next       int
	opened     []herdr.PluginPaneOpenParams
	focused    []string
	tabs       []string // tab.focus calls
	openErr    error
	snapErr    error
	tabErr     error
	// onOpen runs as the pane opens (a board that starts at once).
	onOpen func(paneID string, p herdr.PluginPaneOpenParams)
	// onTabFocus runs as herdr brings a tab in front.
	onTabFocus func()
}

func newFake(workspaces ...string) *fakeHerdr {
	return &fakeHerdr{workspaces: workspaces, panes: map[string]string{}, shells: map[string]int{}}
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return herdr.SessionSnapshot{}, f.snapErr
	}
	var s herdr.SessionSnapshot
	for _, w := range f.workspaces {
		s.Workspaces = append(s.Workspaces, herdr.WorkspaceInfo{WorkspaceID: w})
	}
	for p, w := range f.panes {
		s.Panes = append(s.Panes, herdr.PaneInfo{PaneID: p, WorkspaceID: w, TabID: "t-" + p})
	}
	return s, nil
}

func (f *fakeHerdr) PaneProcessInfo(_ context.Context, pane string) (herdr.ProcessInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.panes[pane]; !ok {
		return herdr.ProcessInfo{}, &herdr.Error{Code: "pane_not_found"}
	}
	pid := f.shells[pane]
	return herdr.ProcessInfo{PaneID: pane, ShellPID: &pid, ForegroundProcessGroupID: &pid}, nil
}

func (f *fakeHerdr) PluginPaneOpen(_ context.Context, p herdr.PluginPaneOpenParams) (*herdr.PluginPane, error) {
	f.mu.Lock()
	f.opened = append(f.opened, p)
	if f.openErr != nil {
		err := f.openErr
		f.mu.Unlock()
		return nil, err
	}
	f.next++
	id := "p" + string(rune('a'+f.next))
	f.panes[id] = p.WorkspaceID
	f.shells[id] = 1000 + f.next
	onOpen := f.onOpen
	f.mu.Unlock()
	if onOpen != nil {
		onOpen(id, p)
	}
	return &herdr.PluginPane{PluginID: p.PluginID, Entrypoint: p.Entrypoint, Pane: herdr.PaneInfo{PaneID: id, WorkspaceID: p.WorkspaceID, TabID: "t-" + id}}, nil
}

func (f *fakeHerdr) PluginPaneFocus(_ context.Context, pane string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = append(f.focused, pane)
	return nil
}

func (f *fakeHerdr) TabFocus(_ context.Context, tab string) (herdr.TabInfo, error) {
	f.mu.Lock()
	f.tabs = append(f.tabs, tab)
	err, hook := f.tabErr, f.onTabFocus
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return herdr.TabInfo{}, err
	}
	return herdr.TabInfo{TabID: tab, Focused: true}, nil
}

func (f *fakeHerdr) shell(pane string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shells[pane]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testEnv(t *testing.T) (Env, *clock) {
	t.Helper()
	c := &clock{t: t0}
	return Env{InstanceDir: t.TempDir(), InstanceID: "inst-000000000001", Now: c.now,
		ProcStart: func(pid int) (uint64, error) { return uint64(pid) * 10, nil }}, c
}

// A first prefix+d opens a tab with a token; the board registers; a
// second prefix+d focuses the same pane and opens nothing.
func TestOpenRegisterFocus(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1", Env: map[string]string{"NHM_CONTEXT": "{}"}})
	if err != nil || res.Action != Opened || res.PaneID == "" || res.Token == "" {
		t.Fatalf("Open = %+v, %v", res, err)
	}
	if len(h.opened) != 1 || h.opened[0].Placement != "tab" || h.opened[0].WorkspaceID != "w1" ||
		h.opened[0].Env[TokenEnv] != res.Token || h.opened[0].Env["NHM_CONTEXT"] != "{}" {
		t.Fatalf("opened = %+v", h.opened)
	}
	// The new tab comes in front of the attached clients.
	if res.Unshown != "" || len(h.tabs) != 1 || h.tabs[0] != "t-"+res.PaneID {
		t.Fatalf("tab.focus %v, unshown %q", h.tabs, res.Unshown)
	}
	l, err := Register(ctx, env, h, "w1", res.Token, Self{PID: h.shell(res.PaneID)})
	if err != nil || l.State != Registered || l.PaneID != res.PaneID || l.PIDStart == 0 {
		t.Fatalf("Register = %+v, %v", l, err)
	}
	again, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err != nil || again.Action != Focused || again.PaneID != res.PaneID {
		t.Fatalf("second Open = %+v, %v", again, err)
	}
	if len(h.opened) != 1 || len(h.focused) != 1 || h.focused[0] != res.PaneID {
		t.Fatalf("opened %d, focused %v", len(h.opened), h.focused)
	}
	if again.Unshown != "" || len(h.tabs) != 2 || h.tabs[1] != "t-"+res.PaneID {
		t.Fatalf("tab.focus %v, unshown %q", h.tabs, again.Unshown)
	}
	if err := Unregister(ctx, env, "w1", res.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.path("w1")); !os.IsNotExist(err) {
		t.Fatalf("record after Unregister: %v", err)
	}
}

// After a server restart herdr gives the pane back with a shell in it:
// the same pane id, another process. nhm opens a new board and names the
// old pane; it does not focus the shell.
func TestRestoredShellIsNotTheBoard(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if _, err := Register(ctx, env, h, "w1", res.Token, Self{PID: h.shell(res.PaneID)}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.shells[res.PaneID] = 4242 // the restored bash
	h.mu.Unlock()
	again, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err != nil || again.Action != Opened || again.PaneID == res.PaneID || again.StalePane != res.PaneID ||
		again.Dropped != "its pane runs another process" {
		t.Fatalf("Open after restart = %+v, %v", again, err)
	}
	if len(h.focused) != 0 {
		t.Fatalf("focused %v", h.focused)
	}
}

// The tab comes in front after the workspace lock: a board that registers
// while herdr does it is not kept waiting (a slow tab.focus would make it
// give up on the lock). Each of Open's three paths.
func TestTabFocusRunsAfterTheLock(t *testing.T) {
	env, c := testEnv(t)
	env.LockWait = 200 * time.Millisecond
	h := newFake("w1")
	ctx := context.Background()
	var lockErrs []error
	h.onTabFocus = func() {
		lockErrs = append(lockErrs, env.locked(ctx, "w1", func() error { return nil }))
	}
	res, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"}) // opened
	if err != nil || res.Action != Opened {
		t.Fatalf("Open = %+v, %v", res, err)
	}
	c.add(time.Second)
	if again, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"}); err != nil || again.Action != Awaiting {
		t.Fatalf("Open while starting = %+v, %v", again, err)
	}
	if _, err := Register(ctx, env, h, "w1", res.Token, Self{PID: h.shell(res.PaneID)}); err != nil {
		t.Fatal(err)
	}
	if third, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"}); err != nil || third.Action != Focused {
		t.Fatalf("Open of the live board = %+v, %v", third, err)
	}
	if len(lockErrs) != 3 {
		t.Fatalf("%d tab.focus calls", len(lockErrs))
	}
	for i, err := range lockErrs {
		if err != nil {
			t.Errorf("tab.focus %d ran under the workspace lock: %v", i, err)
		}
	}
}

// herdr does not bring the tab in front: the board is open all the same,
// its record stays, and the result says why.
func TestUnshownTabStillOpensTheBoard(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	h.tabErr = &herdr.Error{Code: "tab_not_found", Message: "no such tab"}
	ctx := context.Background()
	res, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err != nil || res.Action != Opened || res.PaneID == "" || !strings.Contains(res.Unshown, "no such tab") {
		t.Fatalf("Open = %+v, %v", res, err)
	}
	if l, err := env.read("w1"); err != nil || l == nil || l.Token != res.Token || l.PaneID != res.PaneID {
		t.Fatalf("record = %+v, %v", l, err)
	}
}

// A reused pid (same pid, another start time) is not the board.
func TestReusedPIDIsNotTheBoard(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	l, err := Register(ctx, env, h, "w1", res.Token, Self{PID: h.shell(res.PaneID)})
	if err != nil {
		t.Fatal(err)
	}
	env.ProcStart = func(int) (uint64, error) { return 7, nil }
	st, err := Verify(ctx, env, h, l)
	if err != nil || st.State != Gone {
		t.Fatalf("Verify = %+v, %v", st, err)
	}
}

// A launch that has not registered is waited for, never opened twice;
// after StartGrace it is dropped.
func TestStartingLaunchIsNotOpenedTwice(t *testing.T) {
	env, c := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	c.add(3 * time.Second)
	again, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err != nil || again.Action != Awaiting || again.Token != res.Token || again.OpenedAt == nil ||
		env.now().Sub(*again.OpenedAt) != 3*time.Second {
		t.Fatalf("Open while starting = %+v, %v", again, err)
	}
	if len(h.opened) != 1 || len(h.focused) != 1 || h.focused[0] != res.PaneID {
		t.Fatalf("opened %d, focused %v", len(h.opened), h.focused)
	}
	if len(h.tabs) != 2 || h.tabs[1] != "t-"+res.PaneID {
		t.Fatalf("tab.focus %v", h.tabs)
	}
	c.add(StartGrace)
	third, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err != nil || third.Action != Opened || third.Dropped != "the launch never registered" || third.StalePane != res.PaneID {
		t.Fatalf("Open after the grace = %+v, %v", third, err)
	}
	// The first board starts late: its token is no longer the record.
	if _, err := Register(ctx, env, h, "w1", res.Token, Self{PID: h.shell(res.PaneID)}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("late Register = %v", err)
	}
}

// The user closes a starting board's tab: the next prefix+d opens a new
// one at once, without waiting out the grace.
func TestClosedStartingBoardIsGone(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	h.mu.Lock()
	delete(h.panes, res.PaneID)
	h.mu.Unlock()
	again, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err != nil || again.Action != Opened || again.Dropped != "its pane is closed" || again.StalePane != "" {
		t.Fatalf("Open = %+v, %v", again, err)
	}
	if len(h.focused) != 0 {
		t.Fatalf("focused %v", h.focused)
	}
}

// A board that no pane of its workspace runs does not register: its
// record could never be checked.
func TestRegisterNeedsItsPane(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	h.openErr = errors.New("timeout")
	_, _ = Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	l, _ := env.read("w1")
	if _, err := Register(ctx, env, h, "w1", l.Token, Self{PID: 31337}); err == nil || !strings.Contains(err.Error(), "own pane") {
		t.Fatalf("Register = %v", err)
	}
}

// herdr refuses: no pane, no record. No answer: the record stays
// "opening", and a board that did start can register.
func TestOpenRefusedAndUncertain(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	h.openErr = &herdr.Error{Code: "invalid_params", Message: "no"}
	if _, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"}); err == nil {
		t.Fatal("Open succeeded")
	}
	if _, err := os.Stat(env.path("w1")); !os.IsNotExist(err) {
		t.Fatalf("a refused open left a record: %v", err)
	}
	h.openErr = errors.New("read: connection reset")
	if _, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"}); err == nil {
		t.Fatal("Open succeeded")
	}
	l, err := env.read("w1")
	if err != nil || l == nil || l.State != Opening || l.PaneID != "" {
		t.Fatalf("record after a lost answer = %+v, %v", l, err)
	}
	// The board did start, in a pane nobody was told about; it finds its
	// pane by its own pid.
	h.mu.Lock()
	h.panes["pz"], h.shells["pz"] = "w1", 777
	h.mu.Unlock()
	got, err := Register(ctx, env, h, "w1", l.Token, Self{PID: 777})
	if err != nil || got.PaneID != "pz" {
		t.Fatalf("Register = %+v, %v", got, err)
	}
}

// herdr does not answer the check: nhm opens no second board.
func TestUncertainCheckRefuses(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if _, err := Register(ctx, env, h, "w1", res.Token, Self{PID: h.shell(res.PaneID)}); err != nil {
		t.Fatal(err)
	}
	h.snapErr = errors.New("timeout")
	if _, err := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"}); err == nil {
		t.Fatal("Open succeeded without a check")
	}
	if len(h.opened) != 1 {
		t.Fatalf("opened %d", len(h.opened))
	}
}

// Register validates what the board was launched for: this instance and
// a workspace herdr has. It never takes another workspace.
func TestRegisterValidates(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	pid := h.shell(res.PaneID)
	other := env
	other.InstanceID = "inst-000000000002"
	if _, err := Register(ctx, other, h, "w1", res.Token, Self{PID: pid}); !errors.Is(err, ErrWrongInstance) {
		t.Fatalf("other instance = %v", err)
	}
	if _, err := Register(ctx, env, h, "w1", "bl-forged", Self{PID: pid}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("wrong token = %v", err)
	}
	if _, err := Register(ctx, env, h, "w1", res.Token, Self{PID: pid, PaneID: "pq"}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("other pane = %v", err)
	}
	h.mu.Lock()
	h.workspaces = nil
	h.mu.Unlock()
	if _, err := Register(ctx, env, h, "w1", res.Token, Self{PID: pid}); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("closed workspace = %v", err)
	}
}

// Unregister leaves a newer launch's record alone.
func TestUnregisterKeepsNewerRecord(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	res, _ := Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
	if err := Unregister(ctx, env, "w1", "bl-older"); err != nil {
		t.Fatal(err)
	}
	if l, err := env.read("w1"); err != nil || l == nil || l.Token != res.Token {
		t.Fatalf("record = %+v, %v", l, err)
	}
}

// Two prefix+d at once open one board: the workspace lock orders them.
func TestConcurrentOpensOpenOne(t *testing.T) {
	env, _ := testEnv(t)
	h := newFake("w1")
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]OpenResult, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = Open(ctx, env, h, OpenRequest{WorkspaceID: "w1"})
		}()
	}
	wg.Wait()
	opened := 0
	for _, r := range results {
		if r.Action == Opened {
			opened++
		} else if r.Action != Awaiting {
			t.Errorf("result %+v", r)
		}
	}
	if opened != 1 || len(h.opened) != 1 {
		t.Fatalf("opened %d boards (%d herdr opens)", opened, len(h.opened))
	}
}

func TestProcStartReadsSelf(t *testing.T) {
	a, err := ProcStart(os.Getpid())
	if err != nil || a == 0 {
		t.Skipf("no /proc here: %v", err)
	}
	b, _ := ProcStart(os.Getpid())
	if a != b {
		t.Fatalf("start time changed: %d, %d", a, b)
	}
	if _, err := ProcStart(-1); err == nil {
		t.Fatal("ProcStart(-1) worked")
	}
}
