package spawn

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/guards"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/store"
)

// fakeHerdr is a herdr with workspaces, tabs and panes in memory. A new
// tab's pane is 240x60 cells; a split halves the pane it cuts.
type fakeHerdr struct {
	mu         sync.Mutex
	workspaces []herdr.WorkspaceInfo
	agents     map[string]herdr.AgentInfo // by pane
	envs       map[string]map[string]string
	n          int
	calls      []string
	// The tabs: each pane's tab and place, in the order panes were made,
	// and each tab's workspace and label.
	panes    []string
	tabOf    map[string]string
	rects    map[string]herdr.LayoutRect
	tabWS    map[string]string
	tabLabel map[string]string
	tabs     []string
	splits   []string // "<target> <direction>"
	// startErr fails agent.start for the n-th start (1-based), every try.
	startErr map[int]error
	starts   int
	// blocked makes the first agent.wait answer blocked.
	blocked bool
	// After the keys, stale reads (agent.get, agent.wait) still answer the
	// dialog's blocked, as herdr does for a moment (nhm-913); again makes
	// every read after them answer blocked with a later state_change_seq:
	// another dialog.
	stale int
	again bool
	keyed bool
	// waitErr fails every agent.wait (herdr's timeout, say).
	waitErr error
	// screen is what the pane shows; scrollback the lines above it, which
	// a recent read gives too.
	screen     string
	scrollback string
	keys       []string
	closed     []string
}

func newFake() *fakeHerdr {
	return &fakeHerdr{agents: map[string]herdr.AgentInfo{}, envs: map[string]map[string]string{}, startErr: map[int]error{},
		tabOf: map[string]string{}, rects: map[string]herdr.LayoutRect{}, tabWS: map[string]string{}, tabLabel: map[string]string{}}
}

func (f *fakeHerdr) call(s string) {
	f.calls = append(f.calls, s)
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snap := herdr.SessionSnapshot{Workspaces: append([]herdr.WorkspaceInfo(nil), f.workspaces...)}
	count := map[string]uint{}
	for _, p := range f.panes {
		tab := f.tabOf[p]
		count[tab]++
		snap.Panes = append(snap.Panes, herdr.PaneInfo{PaneID: p, TabID: tab, WorkspaceID: f.tabWS[tab], Agent: f.agents[p].Agent})
	}
	for _, tab := range f.tabs {
		if count[tab] > 0 {
			snap.Tabs = append(snap.Tabs, herdr.TabInfo{TabID: tab, WorkspaceID: f.tabWS[tab], Label: f.tabLabel[tab], PaneCount: count[tab]})
		}
	}
	return snap, nil
}

// newPane makes a tab with one pane.
func (f *fakeHerdr) newPane(ws string, env map[string]string) (string, string) {
	f.n++
	pane, tab := fmt.Sprintf("%s:p%d", ws, f.n), fmt.Sprintf("%s:t%d", ws, f.n)
	f.envs[pane] = env
	f.panes = append(f.panes, pane)
	f.tabOf[pane], f.rects[pane], f.tabWS[tab] = tab, herdr.LayoutRect{Width: 240, Height: 60}, ws
	f.tabs = append(f.tabs, tab)
	f.tabLabel[tab] = strconv.Itoa(len(f.tabs))
	return pane, tab
}

func (f *fakeHerdr) PaneLayout(_ context.Context, pane string) (herdr.PaneLayout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tab, ok := f.tabOf[pane]
	if !ok {
		return herdr.PaneLayout{}, &herdr.Error{Code: "pane_not_found"}
	}
	l := herdr.PaneLayout{WorkspaceID: f.tabWS[tab], TabID: tab}
	for _, p := range f.panes {
		if f.tabOf[p] == tab {
			l.Panes = append(l.Panes, herdr.LayoutPane{PaneID: p, Rect: f.rects[p]})
		}
	}
	return l, nil
}

func (f *fakeHerdr) WorkspaceCreate(_ context.Context, p herdr.WorkspaceCreateParams) (herdr.Created, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("workspace.create " + p.Label)
	ws := fmt.Sprintf("w%d", len(f.workspaces)+1)
	f.workspaces = append(f.workspaces, herdr.WorkspaceInfo{WorkspaceID: ws, Label: p.Label})
	pane, tab := f.newPane(ws, p.Env)
	return herdr.Created{Workspace: &herdr.WorkspaceInfo{WorkspaceID: ws, Label: p.Label},
		Tab: herdr.TabInfo{TabID: tab, WorkspaceID: ws}, RootPane: herdr.PaneInfo{PaneID: pane, TabID: tab, WorkspaceID: ws}}, nil
}

func (f *fakeHerdr) TabCreate(_ context.Context, p herdr.TabCreateParams) (herdr.Created, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("tab.create " + p.Label)
	pane, tab := f.newPane(p.WorkspaceID, p.Env)
	return herdr.Created{Tab: herdr.TabInfo{TabID: tab, WorkspaceID: p.WorkspaceID},
		RootPane: herdr.PaneInfo{PaneID: pane, TabID: tab, WorkspaceID: p.WorkspaceID}}, nil
}

func (f *fakeHerdr) TabRename(_ context.Context, tab, label string) (herdr.TabInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("tab.rename " + tab + " " + label)
	f.tabLabel[tab] = label
	return herdr.TabInfo{TabID: tab, Label: label}, nil
}

func (f *fakeHerdr) PaneSplit(_ context.Context, p herdr.PaneSplitParams) (herdr.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("pane.split " + p.TargetPaneID)
	f.splits = append(f.splits, p.TargetPaneID+" "+p.Direction)
	tab, ok := f.tabOf[p.TargetPaneID]
	if !ok {
		return herdr.PaneInfo{}, &herdr.Error{Code: "pane_not_found"}
	}
	ws := f.tabWS[tab]
	f.n++
	pane := fmt.Sprintf("%s:p%d", ws, f.n)
	f.envs[pane] = p.Env
	// The ratio is the target's share, rounded to whole cells, as herdr
	// 0.9.3 splits (nhm-cxh).
	ratio := 0.5
	if p.Ratio != nil {
		ratio = *p.Ratio
	}
	r := f.rects[p.TargetPaneID]
	kept, made := r, r
	if p.Direction == "right" {
		kept.Width = uint16(math.Round(float64(r.Width) * ratio))
		made.X, made.Width = r.X+kept.Width, r.Width-kept.Width
	} else {
		kept.Height = uint16(math.Round(float64(r.Height) * ratio))
		made.Y, made.Height = r.Y+kept.Height, r.Height-kept.Height
	}
	f.rects[p.TargetPaneID], f.rects[pane], f.tabOf[pane] = kept, made, tab
	f.panes = append(f.panes, pane)
	return herdr.PaneInfo{PaneID: pane, TabID: tab, WorkspaceID: ws}, nil
}

func (f *fakeHerdr) PaneProcessInfo(_ context.Context, pane string) (herdr.ProcessInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	shell := 100
	fg := shell
	info := herdr.ProcessInfo{PaneID: pane, ShellPID: &shell, ForegroundProcessGroupID: &fg}
	if _, ok := f.agents[pane]; ok {
		agent := 200
		info.ForegroundProcessGroupID = &agent
		info.ForegroundProcesses = []herdr.Process{{PID: 200, Name: "claude"}}
	}
	return info, nil
}

func (f *fakeHerdr) PaneRename(_ context.Context, pane, label string) (herdr.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("pane.rename " + pane + " " + label)
	return herdr.PaneInfo{PaneID: pane, Label: label}, nil
}

func (f *fakeHerdr) PaneReportMetadata(_ context.Context, p herdr.ReportMetadataParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("pane.report_metadata " + p.PaneID + " " + p.Tokens["mail"])
	return nil
}

func (f *fakeHerdr) PaneClose(_ context.Context, pane string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, pane)
	for i, p := range f.panes {
		if p == pane {
			f.panes = append(f.panes[:i], f.panes[i+1:]...)
			break
		}
	}
	delete(f.tabOf, pane)
	return nil
}

func (f *fakeHerdr) AgentList(context.Context) ([]herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []herdr.AgentInfo
	for _, a := range f.agents {
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeHerdr) AgentStart(_ context.Context, p herdr.AgentStartParams) (herdr.AgentStarted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.call(fmt.Sprintf("agent.start %s %s %s %v", p.Name, p.Kind, p.PaneID, p.Args))
	if err := f.startErr[len(f.agents)+1]; err != nil {
		return herdr.AgentStarted{}, err
	}
	seq := uint64(1)
	f.agents[p.PaneID] = herdr.AgentInfo{PaneID: p.PaneID, Name: p.Name, Agent: p.Kind, AgentStatus: herdr.StatusIdle,
		StateChangeSeq: &seq, AgentSession: &herdr.AgentSession{Value: "sess-" + p.Name}}
	return herdr.AgentStarted{Agent: f.agents[p.PaneID]}, nil
}

func (f *fakeHerdr) AgentWait(_ context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[p.Target]
	if f.waitErr != nil {
		return herdr.AgentInfo{}, f.waitErr
	}
	if f.blocked {
		f.blocked = false
		a.AgentStatus = herdr.StatusBlocked
		return a, nil
	}
	return f.afterKeys(a), nil
}

func (f *fakeHerdr) AgentGet(_ context.Context, target string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[target]
	if !ok {
		return herdr.AgentInfo{}, &herdr.Error{Code: herdr.CodeAgentNotFound}
	}
	return f.afterKeys(a), nil
}

// afterKeys is a read's answer once the keys are sent (stale, again).
func (f *fakeHerdr) afterKeys(a herdr.AgentInfo) herdr.AgentInfo {
	switch {
	case !f.keyed:
	case f.again:
		seq := uint64(1)
		if a.StateChangeSeq != nil {
			seq = *a.StateChangeSeq + 1
		}
		a.AgentStatus, a.StateChangeSeq = herdr.StatusBlocked, &seq
	case f.stale > 0:
		f.stale--
		a.AgentStatus = herdr.StatusBlocked
	}
	return a
}

func (f *fakeHerdr) AgentRead(_ context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error) {
	if p.Source == herdr.ReadVisible {
		return herdr.PaneRead{Text: f.screen}, nil
	}
	return herdr.PaneRead{Text: f.scrollback + f.screen}, nil
}

func (f *fakeHerdr) AgentSendKeys(_ context.Context, _ string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, keys...)
	f.keyed = true
	return nil
}

// fakeMail answers ensure_project, create_agent_identity and
// retire_agent.
type fakeMail struct {
	mu      sync.Mutex
	names   []string
	n       int
	retired []string
	fail    bool
}

func (m *fakeMail) Call(_ context.Context, tool string, args, out any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch tool {
	case "ensure_project":
		return nil
	case "create_agent_identity":
		if m.fail {
			return errors.New("agent mail is down")
		}
		name := m.names[m.n]
		m.n++
		p := out.(*struct {
			Name  string `json:"name"`
			Token string `json:"registration_token"`
		})
		p.Name, p.Token = name, "tok-"+name
		return nil
	case "retire_agent":
		m.retired = append(m.retired, args.(map[string]any)["agent_name"].(string))
		return nil
	}
	return fmt.Errorf("unexpected tool %s", tool)
}

type world struct {
	env     Env
	h       *fakeHerdr
	mail    *fakeMail
	db      *store.DB
	sent    []send.Request
	idFiles []string
	inst    store.Instance
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	write := func(path, s string) {
		if err := writeFileAll(path, s); err != nil {
			t.Fatal(err)
		}
	}
	write(home+"/.acfs/onboard/docs/ntm/command_palette.md",
		"## A\n### default_new_agent | Default\nRead AGENTS.md.\n### next_bead | Next\nPick your next bead with bv.\n")
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &world{h: newFake(), mail: &fakeMail{names: []string{"IcyKnoll", "BlueLake", "RedStone", "GreenPike"}}, db: db, inst: inst}
	w.env = Env{DB: db, Bindings: bindings.NewStore(db), Herdr: w.h, Mail: w.mail, InstanceDir: inst.Dir,
		Admission: AdmissionEnv{},
		Send: func(_ context.Context, r send.Request) (send.Result, error) {
			w.sent = append(w.sent, r)
			return send.Result{OperationID: fmt.Sprintf("op-kick-%d", len(w.sent)),
				Targets: []send.Receipt{{Status: send.StatusSubmitted}}}, nil
		},
		IdentityFile: func(_, pane, name string) error { w.idFiles = append(w.idFiles, pane+"="+name); return nil },
		ProcStart:    func(int64) (int64, error) { return 4242, nil },
		Sleep:        func(context.Context, time.Duration) {},
	}
	return w
}

// req gives each agent its own tab, as spawn did before nhm-4c4; the
// shared-tab tests set PerTab themselves.
func req(agents ...Want) Request {
	return Request{Session: "swarm", ProjectRoot: "/p", ProjectID: "proj", Agents: agents, PerTab: 1}
}

func TestSpawnIdentityPaneBindingKickoff(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := req(Want{Kind: "claude", Count: 1, Model: "opus"}, Want{Kind: "codex", Count: 1})
	r.OperationID = "op-sp-1"
	settings := HookSettings(func(bin string) (string, error) { return "/usr/bin/" + bin, nil })
	w.env.Admission.Settings = settings
	res, err := Spawn(ctx, w.env, r)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready() || res.WorkspaceID != "w1" || !res.WorkspaceNew || len(res.Targets) != 2 {
		t.Fatalf("res = %+v", res)
	}
	icy, blue := res.Targets[0], res.Targets[1]
	if icy.MailName != "IcyKnoll" || icy.HerdrName != "icyknoll" || icy.Slot != "0.1" || icy.Status != StatusKickoffSubmitted ||
		blue.Slot != "1.1" || blue.PaneID != "w1:p2" || icy.Provenance != "created" {
		t.Fatalf("targets = %+v / %+v", icy, blue)
	}
	calls := strings.Join(w.h.calls, "\n")
	// ntm's launch: Claude with --dangerously-skip-permissions, the model,
	// --effort xhigh and the dcg and rch hooks on --settings; Codex with
	// --dangerously-bypass-approvals-and-sandbox and xhigh, plus the
	// fork's --no-daemon.
	if settings != `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"'/usr/bin/dcg'","timeout":5}]},`+
		`{"matcher":"Bash","hooks":[{"type":"command","command":"'/usr/bin/rch'","timeout":5}]}]}}` {
		t.Fatalf("settings = %s", settings)
	}
	for _, want := range []string{"workspace.create swarm", "tab.create BlueLake",
		"agent.start icyknoll claude w1:p1 [--dangerously-skip-permissions --model opus --effort xhigh --settings " + settings + "]",
		"agent.start bluelake codex w1:p2 [--no-daemon --dangerously-bypass-approvals-and-sandbox -c model_reasoning_effort=xhigh]",
		"pane.rename w1:p1 IcyKnoll opus", "pane.rename w1:p2 BlueLake codex"} {
		if !strings.Contains(calls, want) {
			t.Errorf("no %q in\n%s", want, calls)
		}
	}
	// The pane env carries the launch id and the name, also as
	// AGENT_MAIL_AGENT for ACFS's Agent Mail hook (nhm-4c4).
	if w.h.envs["w1:p1"]["NHM_LAUNCH_ID"] != "op-sp-1.0" || w.h.envs["w1:p2"]["NHM_AGENT"] != "BlueLake" ||
		w.h.envs["w1:p2"]["AGENT_MAIL_AGENT"] != "BlueLake" {
		t.Fatalf("envs = %v", w.h.envs)
	}
	// The binding: generation 1, the slot, the session, the process, the token.
	b, err := w.env.Bindings.ActiveByName(ctx, "/p", "IcyKnoll")
	if err != nil || b.Generation != 1 || b.Slot != "0.1" || b.LaunchID != "op-sp-1.0" || b.CreatedByOp != "op-sp-1" ||
		b.NativeSession == nil || b.NativeSession.Value != "sess-icyknoll" || b.ForegroundPID != 200 || b.ForegroundStart != 4242 {
		t.Fatalf("binding = %+v %v", b, err)
	}
	if tok, err := store.ReadSecret(w.inst.Dir, b.ID); err != nil || tok != "tok-IcyKnoll" {
		t.Fatalf("token = %q %v", tok, err)
	}
	// The kickoff: the identity preamble, then ACFS's default prompt.
	if len(w.sent) != 2 || !strings.HasPrefix(w.sent[0].Message, "Your Agent Mail identity is already registered: name IcyKnoll, project key /p") ||
		!strings.HasSuffix(w.sent[0].Message, "Read AGENTS.md.") || w.sent[0].Selector.Names[0] != "IcyKnoll" {
		t.Fatalf("kickoffs = %+v", w.sent)
	}
	// Its memory query is the task, not the identity preamble
	// (acfs-9hkh.87).
	if q := w.sent[0].Enrich.Query; strings.Contains(q, "Agent Mail identity") || !strings.HasSuffix(q, "Read AGENTS.md.") {
		t.Fatalf("kickoff memory query = %q", q)
	}
	if strings.Join(w.idFiles, ",") != "w1:p1=IcyKnoll,w1:p2=BlueLake" {
		t.Fatalf("identity files = %v", w.idFiles)
	}
	// A repeat of the id starts nothing.
	starts := w.h.starts
	again, err := Spawn(ctx, w.env, r)
	if err != nil || !again.Replayed || w.h.starts != starts || len(again.Targets) != 2 || again.Targets[1].MailName != "BlueLake" {
		t.Fatalf("replay = %+v %v", again, err)
	}
	// add with --per-tab 2 fills the project's two tabs, one agent
	// each, which then take the project's label (nhm-4c4).
	add := req(Want{Kind: "claude", Count: 2})
	add.Add, add.PerTab, add.NoKickoff = true, 2, true
	res, err = Spawn(ctx, w.env, add)
	if err != nil || res.Targets[0].Slot != "0.2" || res.Targets[1].Slot != "1.2" || len(w.sent) != 2 {
		t.Fatalf("add = %+v %v", res.Targets, err)
	}
	if strings.Join(w.h.splits, ",") != "w1:p1 right,w1:p2 right" || w.h.tabLabel["w1:t1"] != "p" || w.h.tabLabel["w1:t2"] != "p" {
		t.Fatalf("splits %v, labels %v", w.h.splits, w.h.tabLabel)
	}
	// Both tabs are full: the next add takes the next tab number.
	w.mail.names = append(w.mail.names, "TanFinch", "OakRiver")
	res, err = Spawn(ctx, w.env, add)
	if err != nil || res.Targets[0].Slot != "2.1" || res.Targets[1].Slot != "2.2" {
		t.Fatalf("add = %+v %v", res.Targets, err)
	}
	if !strings.Contains(strings.Join(w.h.calls, "\n"), "pane.split "+res.Targets[0].PaneID) {
		t.Fatalf("per-tab did not split: %v", w.h.calls)
	}
}

func TestDryRunCreatesNothing(t *testing.T) {
	w := newWorld(t)
	r := req(Want{Kind: "claude", Count: 2})
	r.DryRun, r.OperationID = true, "op-dry"
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || !res.DryRun || len(res.Targets) != 2 || res.Targets[1].Slot != "1.1" || res.Admission.Decision != Admit {
		t.Fatalf("dry = %+v %v", res, err)
	}
	if len(w.h.calls) != 0 || w.mail.n != 0 {
		t.Fatalf("calls = %v, identities %d", w.h.calls, w.mail.n)
	}
	if _, err := w.db.Operation(context.Background(), "op-dry"); err == nil {
		t.Fatal("a dry run claimed its id")
	}
	out := SpawnOutput(res, nil, "json", time.Now())
	if !out.Success || !out.DryRun || len(out.WouldCreate) != 2 || out.WouldCreate[0].Pane != "0.1" || out.Admission.Decision != "admit" ||
		len(out.Assignments) != 0 || len(out.Stagger.Schedule) != 2 {
		t.Fatalf("robot dry = %+v", out)
	}
	if _, err := Spawn(context.Background(), w.env, Request{Session: "s", Agents: []Want{{Kind: "nope", Count: 1}}}); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if _, err := Spawn(context.Background(), w.env, Request{Session: "s", Agents: []Want{{Kind: "agy", Count: 1, Model: "x"}}}); err == nil {
		t.Fatal("a model for agy was accepted")
	}
	add := req(Want{Kind: "claude", Count: 1})
	add.Add = true
	if _, err := Spawn(context.Background(), w.env, add); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("add without a workspace = %v", err)
	}
}

func pct(v float64) *float64 { return &v }

// Every distinct launch argv is screened: a second model of a kind too.
func TestAdmissionScreensEveryArgv(t *testing.T) {
	var screened []string
	env := AdmissionEnv{
		Guards: func(string) []guards.Result {
			return []guards.Result{{Kind: "claude", Guard: "dcg", State: guards.Configured, Sources: []string{"user"}}}
		},
		Screen: func(_ context.Context, argv []string) (guards.Verdict, error) {
			screened = append(screened, strings.Join(argv, " "))
			if strings.Contains(strings.Join(argv, " "), "evil") {
				return guards.Verdict{Decision: guards.Deny, Command: strings.Join(argv, " "), Reason: "rule"}, nil
			}
			return guards.Verdict{Decision: guards.Allow}, nil
		},
	}
	r := req(Want{Kind: "claude", Count: 1, Model: "opus"}, Want{Kind: "claude", Count: 2, Model: "opus"},
		Want{Kind: "claude", Count: 1, Model: "evil", Effort: "high"})
	ad := Decide(context.Background(), env, r)
	if len(screened) != 2 || ad.Decision != Refuse || !strings.Contains(ad.Reason, "evil") {
		t.Fatalf("screened %v, admission %+v", screened, ad)
	}
	if screened[1] != "claude --dangerously-skip-permissions --model evil --effort high" {
		t.Fatalf("argv = %q", screened[1])
	}
	r = req(Want{Kind: "claude", Count: 1})
	r.Effort = "x y"
	if err := r.Validate(); err == nil {
		t.Fatal("a bad --effort was accepted")
	}
	r = req(Want{Kind: "agy", Count: 1, Effort: "high"})
	if err := r.Validate(); err == nil {
		t.Fatal("an effort for agy was accepted")
	}
	// A request-wide effort leaves the kinds that take none alone.
	r = req(Want{Kind: "agy", Count: 1}, Want{Kind: "claude", Count: 1})
	r.Effort = "high"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmission(t *testing.T) {
	now := time.Now()
	at := now.Add(-time.Minute)
	full := []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, FiveHour: &quota.Window{UsedPct: pct(95)}}}
	limit := []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, LimitReached: &quota.Limit{Type: "rate_limit", ObservedAt: at}}}
	weekly := []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, Weekly: &quota.Window{UsedPct: pct(91)}}}
	ok := []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, FiveHour: &quota.Window{UsedPct: pct(20)}}}
	configured := func(string) []guards.Result {
		return []guards.Result{{Kind: "claude", Guard: "dcg", State: guards.Configured, Sources: []string{"user"}}}
	}
	missing := func(string) []guards.Result {
		return []guards.Result{{Kind: "claude", Guard: "dcg", State: guards.Missing, Detail: "no hook"}}
	}
	allow := func(context.Context, []string) (guards.Verdict, error) {
		return guards.Verdict{Decision: guards.Allow}, nil
	}
	deny := func(context.Context, []string) (guards.Verdict, error) {
		return guards.Verdict{Decision: guards.Deny, Command: "claude --x", Reason: "rule"}, nil
	}
	// As guards.ScreenArgv answers when dcg hangs past ScreenTimeout.
	noAnswer := func(context.Context, []string) (guards.Verdict, error) {
		return guards.Verdict{}, errors.New("guards: dcg test gave no answer (limit 30s): context deadline exceeded")
	}
	red := func(context.Context) (Capacity, error) {
		return Capacity{Status: "red", Reasons: []string{"no memory"}}, nil
	}
	cases := []struct {
		name  string
		env   AdmissionEnv
		mod   func(*Request)
		want  string
		match string
	}{
		{"ok", AdmissionEnv{Quota: func() []quota.Observation { return ok }, Guards: configured, Screen: allow}, nil, Admit, ""},
		{"quota full", AdmissionEnv{Quota: func() []quota.Observation { return full }}, nil, Refuse, "95%"},
		{"quota full, forced", AdmissionEnv{Quota: func() []quota.Observation { return full }}, func(r *Request) { r.Force = true }, Admit, ""},
		{"limit, forced", AdmissionEnv{Quota: func() []quota.Observation { return limit }}, func(r *Request) { r.Force = true }, Refuse, "limit reached"},
		{"weekly", AdmissionEnv{Quota: func() []quota.Observation { return weekly }}, nil, Refuse, "weekly"},
		{"unknown, operator", AdmissionEnv{Quota: func() []quota.Observation { return nil }}, nil, Admit, ""},
		{"unknown, robot", AdmissionEnv{Quota: func() []quota.Observation { return nil }}, func(r *Request) { r.RequireObservations = true }, Refuse, "needs an observation"},
		{"unknown, robot, config", AdmissionEnv{Quota: func() []quota.Observation { return nil }, AdmitUnknown: []string{"claude"}},
			func(r *Request) { r.RequireObservations = true }, Admit, ""},
		{"capacity red, forced", AdmissionEnv{Capacity: red}, func(r *Request) { r.Force = true }, Refuse, "capacity red"},
		{"guard missing", AdmissionEnv{Guards: missing, Screen: allow}, nil, Refuse, "guard dcg is missing"},
		{"guard missing, exception", AdmissionEnv{Guards: missing, Screen: allow}, func(r *Request) { r.AllowUnguarded = true }, Admit, ""},
		{"argv denied, even unguarded", AdmissionEnv{Guards: configured, Screen: deny}, func(r *Request) { r.AllowUnguarded = true }, Refuse, "DCG denies"},
		// The operator's ruling (nhm-eid): a dcg that does not answer in
		// time refuses the launch, --allow-unguarded or not.
		{"argv not screened, even unguarded", AdmissionEnv{Guards: configured, Screen: noAnswer}, func(r *Request) { r.AllowUnguarded = true },
			Refuse, "the launch argv could not be screened: guards: dcg test gave no answer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := req(Want{Kind: "claude", Count: 1})
			if c.mod != nil {
				c.mod(&r)
			}
			c.env.Now = func() time.Time { return now }
			ad := Decide(context.Background(), c.env, r)
			if ad.Decision != c.want || !strings.Contains(ad.Reason, c.match) {
				t.Fatalf("= %s %q, want %s %q", ad.Decision, ad.Reason, c.want, c.match)
			}
		})
	}
	// pi is admitted with a warning that nothing guards it.
	ad := Decide(context.Background(), AdmissionEnv{Guards: missing}, req(Want{Kind: "pi", Count: 1}))
	if ad.Decision != Admit || !strings.Contains(strings.Join(ad.Warnings, ";"), "pi has no guard that nhm sets up") {
		t.Fatalf("pi = %+v", ad)
	}
	// A refusal creates nothing.
	w := newWorld(t)
	w.env.Admission = AdmissionEnv{Capacity: red}
	res, err := Spawn(context.Background(), w.env, req(Want{Kind: "claude", Count: 1}))
	if !errors.Is(err, ErrRefused) || len(w.h.calls) != 0 || res.Admission.Decision != Refuse {
		t.Fatalf("refused spawn = %v, calls %v", err, w.h.calls)
	}
	out := SpawnOutput(res, err, "json", time.Now())
	if out.Success || out.ErrorCode != "ADMISSION_REFUSED" || out.Admission.Decision != "refuse" {
		t.Fatalf("robot refusal = %+v", out.RobotResponse)
	}
}

func TestPartialFailureCleansOnlyWhatItCreated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// The second start finds a busy pane, twice: herdr started nothing.
	w.h.startErr[2] = &herdr.Error{Code: codeAgentPaneBusy, Message: "busy"}
	r := req(Want{Kind: "claude", Count: 3})
	r.NoKickoff = true
	res, err := Spawn(ctx, w.env, r)
	if err != nil {
		t.Fatal(err)
	}
	st := []string{res.Targets[0].Status, res.Targets[1].Status, res.Targets[2].Status}
	if strings.Join(st, ",") != "ready,failed,not_started" || res.Ready() {
		t.Fatalf("statuses = %v", st)
	}
	second := res.Targets[1]
	if !strings.Contains(second.Cleanup, "closed its empty pane w1:p2") || !strings.Contains(second.Cleanup, "retired the unused Agent Mail identity BlueLake") {
		t.Fatalf("cleanup = %q", second.Cleanup)
	}
	if b, err := w.env.Bindings.Get(ctx, second.BindingID); err != nil || b.Status != bindings.Retired {
		t.Fatalf("binding = %+v %v", b, err)
	}
	if _, err := store.ReadSecret(w.inst.Dir, second.BindingID); err == nil {
		t.Fatal("the token of the retired binding stays")
	}
	// The first agent stays.
	if b, err := w.env.Bindings.ActiveByName(ctx, "/p", "IcyKnoll"); err != nil || b.Status != bindings.Active {
		t.Fatalf("first = %+v %v", b, err)
	}
	if op, _ := w.db.Operation(ctx, res.OperationID); op.Status != "partial" {
		t.Fatalf("operation = %s", op.Status)
	}

	// A start that may have happened is unresolved, and nothing is removed.
	w = newWorld(t)
	w.h.startErr[1] = &herdr.TransportError{Method: "agent.start", Err: errors.New("EOF")}
	res, err = Spawn(ctx, w.env, req(Want{Kind: "claude", Count: 1}))
	if err != nil || res.Targets[0].Status != StatusUnresolved || len(w.h.closed) != 0 || len(w.mail.retired) != 0 {
		t.Fatalf("unresolved = %+v %v", res.Targets[0], err)
	}
	// Agent Mail down: nothing in herdr.
	w = newWorld(t)
	w.mail.fail = true
	res, err = Spawn(ctx, w.env, req(Want{Kind: "claude", Count: 1}))
	if err != nil || res.Targets[0].Status != StatusFailed || len(w.h.calls) != 0 {
		t.Fatalf("mail down = %+v %v %v", res.Targets[0], err, w.h.calls)
	}
}

func TestTrustFolder(t *testing.T) {
	w := newWorld(t)
	w.h.blocked = true
	w.h.screen = "Is this a project you created or one you trust?\n ❯ Yes, I trust this folder\n   No, exit\n"
	r := req(Want{Kind: "claude", Count: 1})
	r.NoKickoff = true
	res, _ := Spawn(context.Background(), w.env, r)
	if res.Targets[0].Status != StatusUnresolved || !strings.Contains(res.Targets[0].Error, "--trust-folder") {
		t.Fatalf("without --trust-folder = %+v", res.Targets[0])
	}
	w = newWorld(t)
	w.h.blocked = true
	w.h.screen = "Is this a project you created or one you trust?\n ❯ Yes, I trust this folder\n   No, exit\n"
	r.TrustFolder = true
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || res.Targets[0].Status != StatusReady || strings.Join(w.h.keys, " ") != "enter" {
		t.Fatalf("with --trust-folder = %+v %v keys %v", res.Targets[0], err, w.h.keys)
	}
}

// Claude Code's folder-trust dialog in a pane of 40x12, as herdr reads it
// (Claude Code 2.1.309, herdr 0.9.3, nhm-exg): the dialog is taller than
// the pane, so the question has scrolled off the screen into the
// scrollback, and the answers stay on the screen.
const (
	shortPaneScrollback = `$ claude --dangerously-skip-permissions

` + "───────────────────────────────────────" + `
 Accessing workspace:

 /srv/project

 Quick safety check: Is this a project
 you created or one you trust? (Like
 your own code, a well-known open
 source project, or work from your
 team). If not, take a moment to
`
	shortPaneScreen = ` review what's in this folder first.

 Claude Code'll be able to read, edit,
 and execute files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel
`
)

func TestTrustFolderShortPane(t *testing.T) {
	w := newWorld(t)
	w.h.blocked = true
	w.h.scrollback, w.h.screen = shortPaneScrollback, shortPaneScreen
	r := req(Want{Kind: "claude", Count: 1})
	r.NoKickoff, r.TrustFolder = true, true
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || res.Targets[0].Status != StatusReady || strings.Join(w.h.keys, " ") != "down enter" {
		t.Fatalf("short pane = %+v %v keys %v", res.Targets[0], err, w.h.keys)
	}
}

// After the keys, herdr answers the dialog's blocked for a moment
// (nhm-913): the agent is ready once herdr's state moves on. A blocked
// with a later state_change_seq is another dialog; a blocked that never
// moves is the dialog the keys did not answer.
func TestTrustFolderAfterTheKeys(t *testing.T) {
	trust := func(setup func(w *world), timeout time.Duration) Target {
		w := newWorld(t)
		w.h.blocked = true
		w.h.screen = "Is this a project you created or one you trust?\n ❯ Yes, I trust this folder\n   No, exit\n"
		setup(w)
		r := req(Want{Kind: "claude", Count: 1})
		r.NoKickoff, r.TrustFolder, r.ReadyTimeout = true, true, timeout
		res, err := Spawn(context.Background(), w.env, r)
		if err != nil || strings.Join(w.h.keys, " ") != "enter" {
			t.Fatalf("spawn = %v keys %v", err, w.h.keys)
		}
		return res.Targets[0]
	}
	if got := trust(func(w *world) { w.h.stale = 3 }, time.Minute); got.Status != StatusReady {
		t.Fatalf("stale blocked = %+v", got)
	}
	if got := trust(func(w *world) { w.h.again = true }, time.Minute); got.Status != StatusUnresolved ||
		!strings.Contains(got.Error, "waits at another dialog") {
		t.Fatalf("another dialog = %+v", got)
	}
	if got := trust(func(w *world) { w.h.stale = 1 << 20 }, time.Second); got.Status != StatusUnresolved ||
		!strings.Contains(got.Error, "still waits at a dialog after nhm answered the folder-trust question") {
		t.Fatalf("not answered = %+v", got)
	}
}

// herdr reads Codex's 'Hooks need review' dialog as unknown, so the ready
// wait runs out (nhm-5as): the error names the dialog the kind knows on
// the pane, and keeps herdr's timeout. Another screen, or a kind that
// knows no such dialog, keeps the timeout alone.
func TestReadyTimeoutNamesTheDialog(t *testing.T) {
	start := func(kind, screen string) Target {
		w := newWorld(t)
		w.h.waitErr = &herdr.Error{Method: "agent.wait", Code: herdr.CodeTimeout, Message: "agent did not reach the state"}
		w.h.screen = screen
		r := req(Want{Kind: kind, Count: 1})
		r.NoKickoff = true
		res, err := Spawn(context.Background(), w.env, r)
		if err != nil {
			t.Fatal(err)
		}
		return res.Targets[0]
	}
	hooks := "  Hooks need review\n  1 hook is new or changed.\n\n› 1. Review hooks\n  2. Trust all and continue\n" +
		"  3. Continue without trusting (hooks won't run)\n"
	got := start("codex", hooks)
	if got.Status != StatusUnresolved || !strings.Contains(got.Error, "the agent waits at Codex's 'Hooks need review' dialog") ||
		!strings.Contains(got.Error, "answer it in pane "+got.PaneID) || !strings.Contains(got.Error, "agent did not reach the state") {
		t.Fatalf("hooks dialog = %+v", got)
	}
	for _, c := range []struct{ kind, screen string }{{"codex", "› Ask Codex to do anything\n"}, {"claude", hooks}} {
		if got := start(c.kind, c.screen); got.Status != StatusUnresolved || strings.Contains(got.Error, "Hooks need review") ||
			!strings.Contains(got.Error, "agent did not reach the state") {
			t.Fatalf("%s = %+v", c.kind, got)
		}
	}
}

func TestAliasSuffixAndOwnPrompt(t *testing.T) {
	w := newWorld(t)
	seq := uint64(1)
	// IcyKnoll's herdr name is live in another project.
	w.h.agents["w9:p9"] = herdr.AgentInfo{PaneID: "w9:p9", Name: "icyknoll", StateChangeSeq: &seq}
	r := req(Want{Kind: "claude", Count: 1})
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || res.Targets[0].HerdrName != "icyknoll-2" {
		t.Fatalf("alias = %+v %v", res.Targets[0], err)
	}
	if !strings.HasSuffix(w.sent[0].Message, "Read AGENTS.md.") {
		t.Fatalf("kickoff = %q", w.sent[0].Message)
	}
	// The operator's own prompt goes out as typed, braces included.
	w = newWorld(t)
	r = req(Want{Kind: "claude", Count: 1})
	r.Prompt = "Check {{this}} literally."
	if _, err := Spawn(context.Background(), w.env, r); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(w.sent[0].Message, "\n\nCheck {{this}} literally.") {
		t.Fatalf("own prompt = %q", w.sent[0].Message)
	}
}

// Without --assign the fingerprint keeps "assign": false and no more, as
// before nhm-cvw; with it, the choices stand beside "assign": true.
func TestFingerprintAssignOptions(t *testing.T) {
	r := req(Want{Kind: "claude", Count: 1})
	fp := fingerprint(r)
	if _, ok := fp["assign_options"]; fp["assign"] != false || ok {
		t.Fatalf("without --assign = %v", fp)
	}
	r.AssignOptions = `{"strategy":"balanced"}`
	fp = fingerprint(r)
	if fp["assign"] != true || fp["assign_options"] != `{"strategy":"balanced"}` {
		t.Fatalf("with --assign = %v", fp)
	}
}

// spawn --assign's kickoff goes ahead of every agent's work prompt
// (nhm-cvw): the operator's prompt as typed, else default_new_agent with
// the session and project filled in, else nothing. A palette prompt that
// names one agent refuses, since one text goes to all.
func TestAssignPrefix(t *testing.T) {
	newWorld(t)
	r := req(Want{Kind: "claude", Count: 1})
	if got, err := AssignPrefix(r); err != nil || got != "Read AGENTS.md." {
		t.Fatalf("palette prefix = %q, %v", got, err)
	}
	r.Prompt = "Check {{this}} literally."
	if got, err := AssignPrefix(r); err != nil || got != r.Prompt {
		t.Fatalf("own prompt = %q, %v", got, err)
	}
	r.Prompt, r.NoKickoff = "", true
	if got, err := AssignPrefix(r); err != nil || got != "" {
		t.Fatalf("no kickoff = %q, %v", got, err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeFileAll(home+"/.acfs/onboard/docs/ntm/command_palette.md",
		"## A\n### default_new_agent | Default\nYou are {{agent}} in {{session}}.\n"); err != nil {
		t.Fatal(err)
	}
	r.NoKickoff = false
	if got, err := AssignPrefix(r); err == nil || !strings.Contains(err.Error(), "every agent") {
		t.Fatalf("per-agent placeholder = %q, %v", got, err)
	}
}

// TestRobotContract applies upstream swarm_launch.sh's spawn_response to
// a launch.
func TestRobotContract(t *testing.T) {
	w := newWorld(t)
	r := req(Want{Kind: "claude", Count: 1}, Want{Kind: "codex", Count: 1})
	r.NoKickoff, r.ProjectRoot = true, t.TempDir()
	res, err := Spawn(context.Background(), w.env, r)
	out := SpawnOutput(res, err, "json", time.Now())
	slot := regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	if !out.Success || out.Session != "swarm" || out.WorkingDir != r.ProjectRoot || out.Error != "" || len(out.Assignments) != 0 ||
		out.Admission.Decision != "admit" || len(out.Agents) != 2 {
		t.Fatalf("out = %+v", out)
	}
	types := map[string]int{}
	panes := map[string]bool{}
	for _, a := range out.Agents {
		if !slot.MatchString(a.Pane) || a.Error != "" || !a.Ready {
			t.Fatalf("agent = %+v", a)
		}
		types[a.Type]++
		panes[a.Pane] = true
	}
	if types["claude"] != 1 || types["codex"] != 1 || len(panes) != 2 {
		t.Fatalf("mix = %v %v", types, panes)
	}
}
