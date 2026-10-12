package spawn

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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

// fakeHerdr is a herdr with workspaces, tabs and panes in memory.
type fakeHerdr struct {
	mu         sync.Mutex
	workspaces []herdr.WorkspaceInfo
	agents     map[string]herdr.AgentInfo // by pane
	envs       map[string]map[string]string
	n          int
	calls      []string
	// startErr fails agent.start for the n-th start (1-based), every try.
	startErr map[int]error
	starts   int
	// blocked makes the first agent.wait answer blocked.
	blocked bool
	screen  string
	keys    []string
	closed  []string
}

func newFake() *fakeHerdr {
	return &fakeHerdr{agents: map[string]herdr.AgentInfo{}, envs: map[string]map[string]string{}, startErr: map[int]error{}}
}

func (f *fakeHerdr) call(s string) {
	f.calls = append(f.calls, s)
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return herdr.SessionSnapshot{Workspaces: append([]herdr.WorkspaceInfo(nil), f.workspaces...)}, nil
}

func (f *fakeHerdr) newPane(ws string, env map[string]string) (string, string) {
	f.n++
	pane, tab := fmt.Sprintf("%s:p%d", ws, f.n), fmt.Sprintf("%s:t%d", ws, f.n)
	f.envs[pane] = env
	return pane, tab
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
	return herdr.TabInfo{TabID: tab, Label: label}, nil
}

func (f *fakeHerdr) PaneSplit(_ context.Context, p herdr.PaneSplitParams) (herdr.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call("pane.split " + p.TargetPaneID)
	ws := strings.Split(p.TargetPaneID, ":")[0]
	f.n++
	pane := fmt.Sprintf("%s:p%d", ws, f.n)
	f.envs[pane] = p.Env
	tab := strings.Replace(p.TargetPaneID, ":p", ":t", 1)
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
	if f.blocked {
		f.blocked = false
		a.AgentStatus = herdr.StatusBlocked
	}
	return a, nil
}

func (f *fakeHerdr) AgentGet(_ context.Context, target string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[target]
	if !ok {
		return herdr.AgentInfo{}, &herdr.Error{Code: herdr.CodeAgentNotFound}
	}
	return a, nil
}

func (f *fakeHerdr) AgentRead(context.Context, herdr.AgentReadParams) (herdr.PaneRead, error) {
	return herdr.PaneRead{Text: f.screen}, nil
}

func (f *fakeHerdr) AgentSendKeys(_ context.Context, _ string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, keys...)
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

func req(agents ...Want) Request {
	return Request{Session: "swarm", ProjectRoot: "/p", ProjectID: "proj", Agents: agents}
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
	// The pane env carries the launch id and the name.
	if w.h.envs["w1:p1"]["NHM_LAUNCH_ID"] != "op-sp-1.0" || w.h.envs["w1:p2"]["NHM_AGENT"] != "BlueLake" {
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
	// add into the same workspace takes the next tab number.
	add := req(Want{Kind: "claude", Count: 2})
	add.Add, add.PerTab, add.NoKickoff = true, 2, true
	res, err = Spawn(ctx, w.env, add)
	if err != nil || res.Targets[0].Slot != "2.1" || res.Targets[1].Slot != "2.2" || len(w.sent) != 2 {
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
	if ad.Decision != Admit || !strings.Contains(strings.Join(ad.Warnings, ";"), "pi runs no hooks") {
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

func TestAliasSuffixAndAssign(t *testing.T) {
	w := newWorld(t)
	seq := uint64(1)
	// IcyKnoll's herdr name is live in another project.
	w.h.agents["w9:p9"] = herdr.AgentInfo{PaneID: "w9:p9", Name: "icyknoll", StateChangeSeq: &seq}
	r := req(Want{Kind: "claude", Count: 1})
	r.Assign = true
	res, err := Spawn(context.Background(), w.env, r)
	if err != nil || res.Targets[0].HerdrName != "icyknoll-2" {
		t.Fatalf("alias = %+v %v", res.Targets[0], err)
	}
	if !strings.HasSuffix(w.sent[0].Message, "Read AGENTS.md.\n\nPick your next bead with bv.") {
		t.Fatalf("assign kickoff = %q", w.sent[0].Message)
	}
	r.NoKickoff = true
	if _, err := Spawn(context.Background(), w.env, r); err == nil {
		t.Fatal("--assign without a kickoff was accepted")
	}
	// The operator's own prompt goes out as typed, braces included; the
	// palette's next_bead follows it.
	w = newWorld(t)
	r = req(Want{Kind: "claude", Count: 1})
	r.Prompt, r.Assign = "Check {{this}} literally.", true
	if _, err := Spawn(context.Background(), w.env, r); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(w.sent[0].Message, "\n\nCheck {{this}} literally.\n\nPick your next bead with bv.") {
		t.Fatalf("own prompt = %q", w.sent[0].Message)
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
