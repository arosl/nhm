package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

// The tests resume claude without one on PATH (CI has no agent CLI):
// the kinds' program checks see a claude binary that is an ELF file.
func init() {
	kinds.ResolveProgram = func(name string) (string, error) { return "/opt/" + name + "/bin/" + name, nil }
	kinds.ProgramHead = func(string) ([]byte, error) { return []byte("\x7fELF\x02\x01\x01"), nil }
}

func u64(v uint64) *uint64 { return &v }

// fakeHerdr holds agents by pane and the panes whose shell is at its
// prompt.
type fakeHerdr struct {
	mu        sync.Mutex
	agents    map[string]herdr.AgentInfo
	panes     map[string]bool
	closed    []string
	wsClosed  []string
	prompts   []string
	starts    []string
	renamed   []string
	exitOnCmd bool
	// jobs are panes whose foreground runs a process that is no agent.
	jobs map[string]bool
}

func newFake() *fakeHerdr {
	return &fakeHerdr{agents: map[string]herdr.AgentInfo{}, panes: map[string]bool{}, exitOnCmd: true}
}

func (f *fakeHerdr) add(pane, kind, name, session string, st herdr.AgentStatus) {
	f.agents[pane] = herdr.AgentInfo{PaneID: pane, WorkspaceID: "w1", Agent: kind, Name: name, AgentStatus: st,
		StateChangeSeq: u64(5), AgentSession: &herdr.AgentSession{Value: session}}
	f.panes[pane] = true
}

func (f *fakeHerdr) Snapshot(context.Context) (herdr.SessionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s herdr.SessionSnapshot
	for p := range f.panes {
		s.Panes = append(s.Panes, herdr.PaneInfo{PaneID: p, WorkspaceID: "w1", TabID: "w1:t1"})
	}
	for _, a := range f.agents {
		s.Agents = append(s.Agents, a)
	}
	return s, nil
}

func (f *fakeHerdr) AgentList(ctx context.Context) ([]herdr.AgentInfo, error) {
	s, _ := f.Snapshot(ctx)
	return s.Agents, nil
}

func (f *fakeHerdr) AgentGet(_ context.Context, pane string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[pane]
	if !ok {
		return a, &herdr.Error{Code: herdr.CodeAgentNotFound}
	}
	return a, nil
}

func (f *fakeHerdr) AgentStart(_ context.Context, p herdr.AgentStartParams) (herdr.AgentStarted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, fmt.Sprintf("%s %s %s %v", p.Name, p.Kind, p.PaneID, p.Args))
	f.agents[p.PaneID] = herdr.AgentInfo{PaneID: p.PaneID, Agent: p.Kind, Name: p.Name, AgentStatus: herdr.StatusIdle,
		StateChangeSeq: u64(1), AgentSession: &herdr.AgentSession{Value: "new-session"}}
	return herdr.AgentStarted{}, nil
}

func (f *fakeHerdr) AgentWait(_ context.Context, p herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	return f.AgentGet(context.Background(), p.Target)
}

func (f *fakeHerdr) AgentPrompt(_ context.Context, p herdr.AgentPromptParams) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, p.Target+"="+p.Text)
	if strings.HasPrefix(p.Text, "/") && f.exitOnCmd {
		delete(f.agents, p.Target)
	}
	return herdr.AgentInfo{}, nil
}

func (f *fakeHerdr) AgentRename(_ context.Context, target, name string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renamed = append(f.renamed, target+"="+name)
	return herdr.AgentInfo{}, nil
}

func (f *fakeHerdr) PaneProcessInfo(_ context.Context, pane string) (herdr.ProcessInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	shell, fg := 100, 100
	info := herdr.ProcessInfo{PaneID: pane, ShellPID: &shell, ForegroundProcessGroupID: &fg}
	if _, ok := f.agents[pane]; ok || f.jobs[pane] {
		agent := 200
		info.ForegroundProcessGroupID = &agent
		info.ForegroundProcesses = []herdr.Process{{PID: 200}}
	}
	return info, nil
}

func (f *fakeHerdr) PaneReportMetadata(context.Context, herdr.ReportMetadataParams) error { return nil }

func (f *fakeHerdr) PaneClose(_ context.Context, pane string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, pane)
	delete(f.agents, pane)
	delete(f.panes, pane)
	return nil
}

func (f *fakeHerdr) WorkspaceClose(_ context.Context, ws string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsClosed = append(f.wsClosed, ws)
	return nil
}

// fakeFacts answers the external reads.
type fakeFacts struct {
	reservations []Reservation
	ever         map[string][]string
	dirty        []string
	worktrees    []string
	wtState      map[string][2]bool
	assigned     map[string][]Bead
	closed       []string
	fail         error
	removed      []string
}

func (f *fakeFacts) Reservations(context.Context, string) ([]Reservation, error) {
	return f.reservations, f.fail
}
func (f *fakeFacts) ReservedEver(_ context.Context, _, agent string) ([]string, error) {
	return f.ever[agent], nil
}
func (f *fakeFacts) DirtyFiles(context.Context, string) ([]string, error) { return f.dirty, nil }
func (f *fakeFacts) Worktrees(context.Context, string) ([]string, error)  { return f.worktrees, nil }
func (f *fakeFacts) WorktreeState(_ context.Context, wt string) (bool, bool, error) {
	s := f.wtState[wt]
	return s[0], s[1], nil
}
func (f *fakeFacts) RemoveWorktree(_ context.Context, _, wt string) error {
	f.removed = append(f.removed, wt)
	return nil
}
func (f *fakeFacts) Assigned(_ context.Context, _, agent string) ([]Bead, error) {
	return f.assigned[agent], nil
}
func (f *fakeFacts) Closed(context.Context, string, string) ([]string, error) { return f.closed, nil }

type fakeMail struct {
	retired []string
	down    bool
}

func (m *fakeMail) Call(_ context.Context, tool string, args, _ any) error {
	if m.down {
		return errors.New("connection refused")
	}
	if tool == "retire_agent" {
		a := args.(map[string]any)
		m.retired = append(m.retired, fmt.Sprint(a["agent_name"], ":", a["registration_token"]))
	}
	return nil
}

type world struct {
	env   Env
	h     *fakeHerdr
	facts *fakeFacts
	mail  *fakeMail
	db    *store.DB
	sent  []send.Request
	clock time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := t.TempDir()
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &world{h: newFake(), facts: &fakeFacts{ever: map[string][]string{}, wtState: map[string][2]bool{}, assigned: map[string][]Bead{}},
		mail: &fakeMail{}, db: db, clock: time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)}
	w.env = Env{DB: db, Bindings: bindings.NewStore(db), Herdr: w.h, Mail: w.mail, Facts: w.facts, InstanceDir: inst.Dir,
		Home: home, MailConfigHome: filepath.Join(home, ".config"),
		Send: func(_ context.Context, r send.Request) (send.Result, error) {
			w.sent = append(w.sent, r)
			return send.Result{OperationID: "op-kick", Targets: []send.Receipt{{Status: send.StatusSubmitted}}}, nil
		},
		Now:   func() time.Time { return w.clock },
		Sleep: func(context.Context, time.Duration) { w.clock = w.clock.Add(time.Second) },
	}
	return w
}

// bind creates an agent with a binding, a token, a transcript and an
// identity file.
func (w *world) bind(t *testing.T, name, pane string, st herdr.AgentStatus) bindings.Binding {
	t.Helper()
	ctx := context.Background()
	session := "sess-" + strings.ToLower(name)
	b, err := w.env.Bindings.Create(ctx, bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: pane,
			HerdrAlias: strings.ToLower(name), Kind: "claude", Model: "opus", LaunchID: "op-x.0",
			NativeSession: &bindings.NativeSession{Value: session}}})
	if err != nil {
		t.Fatal(err)
	}
	w.h.add(pane, "claude", strings.ToLower(name), session, st)
	if err := store.WriteSecret(w.env.InstanceDir, b.ID, "tok-"+name); err != nil {
		t.Fatal(err)
	}
	tr := filepath.Join(w.env.Home, ".claude", "projects", "-p", session+".jsonl")
	writeFile(t, tr, `{"type":"user"}`+"\n")
	if _, err := agentmail.WriteIdentity(w.env.MailConfigHome, "/p", pane, name, time.Now()); err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRetire(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.facts.closed = []string{"acfs-1"}
	// A dry run lists the actions and changes nothing.
	res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", DryRun: true})
	if err != nil || len(res.Actions) != 3 || len(w.h.closed) != 0 {
		t.Fatalf("dry run = %+v %v", res, err)
	}
	res, err = Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", OperationID: "op-ret"})
	if err != nil || !res.Done || !res.IdentityRetired || res.Transcript == nil || res.ClosedBeads[0] != "acfs-1" {
		t.Fatalf("retire = %+v %v", res, err)
	}
	if len(w.h.closed) != 1 || w.mail.retired[0] != "IcyKnoll:tok-IcyKnoll" {
		t.Fatalf("closed %v, retired %v", w.h.closed, w.mail.retired)
	}
	got, _ := w.env.Bindings.Get(ctx, b.ID)
	if got.Status != bindings.Retired {
		t.Fatalf("binding = %s", got.Status)
	}
	if _, err := store.ReadSecret(w.env.InstanceDir, b.ID); err == nil {
		t.Fatal("the token stays")
	}
	if _, ok, _ := agentmail.ReadIdentity(w.env.MailConfigHome, "/p", "w1:p1"); ok {
		t.Fatal("the identity file stays")
	}
	data, err := os.ReadFile(res.Transcript.Path)
	if err != nil || string(data) != `{"type":"user"}`+"\n" || res.Transcript.SHA256 == "" || res.Transcript.NativeSession != "sess-icyknoll" {
		t.Fatalf("transcript = %+v %v", res.Transcript, err)
	}
	if _, err := os.Stat(res.Transcript.Meta); err != nil {
		t.Fatal(err)
	}
	// A retry with the same --op-id reports the done operation; it does
	// not refuse the binding the first run retired.
	again, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", OperationID: "op-ret"})
	if err != nil || !again.Done || again.OperationID != "op-ret" || len(w.h.closed) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}
	// A retry of an operation that failed is an error, not a quiet 0.
	if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-bad", Verb: "retire", Request: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w.db.SetOperationStatus(ctx, "op-bad", "failed", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", OperationID: "op-bad"}); err == nil || !strings.Contains(err.Error(), "ended failed") {
		t.Fatalf("retry of a failed operation = %v", err)
	}
	// A pane whose agent herdr lost, with its shell at the prompt: a
	// warning, and the pane stays.
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	delete(w.h.agents, "w1:p2")
	res, err = Retire(ctx, w.env, RetireRequest{Target: "BlueLake"})
	if err != nil || !strings.Contains(strings.Join(res.Warnings, "|"), "its shell stays open") || len(w.h.closed) != 1 {
		t.Fatalf("lost agent at its shell = %+v %v", res, err)
	}
}

func TestFactParsers(t *testing.T) {
	// br 0.7's list output, as captured.
	issues, err := parseBrList([]byte(`{"issues":[{"id":"acfs-1","title":"T","status":"in_progress","assignee":"IcyKnoll"}],"total":1,"limit":0,"offset":0,"has_more":false}`))
	if err != nil || len(issues) != 1 || issues[0].ID != "acfs-1" || issues[0].Assignee != "IcyKnoll" {
		t.Fatalf("br list = %+v %v", issues, err)
	}
	for _, bad := range []string{`[]`, `{"total":0}`, `{"issues":[],"has_more":true}`} {
		if _, err := parseBrList([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	got := parsePorcelainZ([]byte(" M a.go\x00R  new.go\x00old.go\x00?? dir/x.go\x00"))
	if strings.Join(got, ",") != "a.go,new.go,old.go,dir/x.go" {
		t.Fatalf("porcelain = %v", got)
	}
}

func TestRetireRefusals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(w *world)
		want  string
	}{
		{"working", func(w *world) {
			a := w.h.agents["w1:p1"]
			a.AgentStatus = herdr.StatusWorking
			w.h.agents["w1:p1"] = a
		}, "is working"},
		{"reservation", func(w *world) {
			w.facts.reservations = []Reservation{{Agent: "IcyKnoll", Path: "a/**"}}
		}, "still holds reservations"},
		{"dirty reserved file", func(w *world) {
			w.facts.ever["IcyKnoll"] = []string{"internal/**"}
			w.facts.dirty = []string{"internal/x.go"}
		}, "uncommitted changes in files"},
		{"dirty worktree", func(w *world) {
			w.facts.worktrees = []string{"/p", "/tmp/icyknoll-wt"}
			w.facts.wtState["/tmp/icyknoll-wt"] = [2]bool{true, true}
		}, "has uncommitted changes"},
		{"unmerged worktree", func(w *world) {
			w.facts.worktrees = []string{"/tmp/icyknoll-wt"}
		}, "not merged"},
		{"bead without handoff", func(w *world) {
			w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-9"}}
		}, "no handoff comment"},
		{"am down", func(w *world) {
			w.facts.fail = errors.New("connection refused")
		}, "cannot be read"},
		{"caller", func(w *world) { w.env.Caller = "w1:p1" }, "runs in this pane"},
		{"agent mail server down", func(w *world) { w.mail.down = true }, "server cannot be reached"},
		{"herdr lost a working agent", func(w *world) {
			delete(w.h.agents, "w1:p1")
			w.h.jobs = map[string]bool{"w1:p1": true}
		}, "may be IcyKnoll, working"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
			c.setup(w)
			res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll"})
			if !errors.Is(err, ErrRefused) || !strings.Contains(strings.Join(res.Refusals, "|"), c.want) || len(w.h.closed) != 0 {
				t.Fatalf("= %v %v, closed %v", res.Refusals, err, w.h.closed)
			}
		})
	}
	// A dirty file another agent holds now is not the retiring agent's.
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.facts.ever["IcyKnoll"] = []string{"internal/**"}
	w.facts.dirty = []string{"internal/x.go"}
	w.facts.reservations = []Reservation{{Agent: "BlueLake", Path: "internal/x.go"}}
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", DryRun: true}); err != nil {
		t.Fatalf("other's file = %v", err)
	}
	// A send or recycle in progress refuses.
	unlock, err := send.LockTarget(ctx, w.env.InstanceDir, mustBinding(t, w, "IcyKnoll").ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("locked = %v", err)
	}
	unlock()
	// A merged worktree is removed only with --cleanup-worktrees.
	w.facts.worktrees = []string{"/tmp/icyknoll-wt"}
	w.facts.wtState["/tmp/icyknoll-wt"] = [2]bool{false, true}
	res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", CleanupWorktrees: true})
	if err != nil || len(res.Removed) != 1 || len(w.facts.removed) != 1 {
		t.Fatalf("cleanup = %+v %v", res, err)
	}
}

func mustBinding(t *testing.T, w *world, name string) bindings.Binding {
	t.Helper()
	b, err := w.env.Bindings.ActiveByName(context.Background(), "/p", name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRecycle(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusDone)
	w.env.Handoff = func(_ context.Context, _ bindings.Binding, _ *Bead, transcript string) Handoff {
		return Handoff{Text: "# Handoff\nkeep going", Sources: []string{"excerpt"}, Degraded: []string{}}
	}
	w.env.LaunchSettings = `{"hooks":{}}`
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Continue the bead."})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 || res.Transcript == nil {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if w.h.prompts[0] != "w1:p1=/exit" ||
		w.h.starts[0] != `icyknoll claude w1:p1 [--dangerously-skip-permissions --effort xhigh --model opus --settings {"hooks":{}}]` &&
			w.h.starts[0] != `icyknoll claude w1:p1 [--dangerously-skip-permissions --model opus --effort xhigh --settings {"hooks":{}}]` {
		t.Fatalf("prompts %v, starts %v", w.h.prompts, w.h.starts)
	}
	nb, _ := w.env.Bindings.Get(ctx, b.ID)
	if nb.Slot != b.Slot || nb.PaneID != "w1:p1" || nb.NativeSession.Value != "new-session" || nb.HerdrAlias != "icyknoll" {
		t.Fatalf("binding = %+v", nb)
	}
	if len(w.sent) != 1 || !strings.Contains(w.sent[0].Message, "# Handoff\nkeep going\n\nContinue the bead.") {
		t.Fatalf("kickoff = %+v", w.sent)
	}
	// Two copies: before the exit (recovery) and after it (final).
	var copies int
	if err := w.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM transcripts WHERE binding_id = ? AND reason IN ('recycle', 'recycle-exit')`, b.ID).Scan(&copies); err != nil || copies != 2 ||
		!strings.Contains(res.Transcript.Path, "recycle-exit") {
		t.Fatalf("transcript copies = %d %v, final %s", copies, err, res.Transcript.Path)
	}
	// A retry with the recycle's operation id starts nothing.
	rid := res.OperationID
	again, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x", OperationID: rid})
	if err != nil || again.Stage != StageDone || len(w.h.starts) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}

	// An agent that does not exit: stop fails, nothing starts.
	w = newWorld(t)
	b = w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.h.exitOnCmd = false
	res, err = Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err == nil || res.Stage != StageStop || len(w.h.starts) != 0 {
		t.Fatalf("no exit = %+v %v", res, err)
	}
	if nb, _ := w.env.Bindings.Get(ctx, b.ID); nb.Generation != b.Generation {
		t.Fatal("the generation moved")
	}
	// A working agent is not recycled.
	w = newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusWorking)
	if res, err = Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"}); err == nil || res.Stage != StageCheck {
		t.Fatalf("working = %+v %v", res, err)
	}
}

func TestReapAndKill(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	w.bind(t, "RedStone", "w1:p3", herdr.StatusIdle)
	w.bind(t, "GreenPike", "w1:p4", herdr.StatusIdle)
	w.facts.assigned["RedStone"] = []Bead{{ID: "acfs-2", HasHandoff: true}}
	mail := func(_ context.Context, _, agent string) (int, int, error) {
		if agent == "GreenPike" {
			return 1, 0, nil
		}
		return 0, 0, nil
	}
	req := ReapRequest{Idle: 10 * time.Minute, WorkspaceID: "w1", Keep: []string{"bluelake"}}
	items, err := Reap(ctx, w.env, mail, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Action != "kept" || !strings.Contains(it.Reason, "under 10m0s") {
			t.Fatalf("first sweep = %+v", it)
		}
	}
	w.clock = w.clock.Add(11 * time.Minute)
	items, err = Reap(ctx, w.env, mail, req)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, it := range items {
		actions[it.MailName] = it.Action + ": " + it.Reason
	}
	if !strings.HasPrefix(actions["IcyKnoll"], "retired") || !strings.Contains(actions["BlueLake"], "keep list") ||
		!strings.Contains(actions["RedStone"], "work in progress") || !strings.Contains(actions["GreenPike"], "unread") {
		t.Fatalf("second sweep = %v", actions)
	}
	if _, err := Reap(ctx, w.env, mail, ReapRequest{}); err == nil {
		t.Fatal("a reap without a session was accepted")
	}

	// Kill: one refusal refuses all, and nothing closes.
	w.facts.assigned["RedStone"] = []Bead{{ID: "acfs-2"}}
	if _, err := Kill(ctx, w.env, "w1", true); !errors.Is(err, ErrRefused) || len(w.h.wsClosed) != 0 {
		t.Fatalf("kill with a refusal = %v", err)
	}
	w.facts.assigned["RedStone"] = nil
	// What retire cannot check refuses too: an agent nhm has no binding
	// for, and a job in a shell's foreground.
	w.h.add("w1:p9", "codex", "", "s9", herdr.StatusIdle)
	if _, err := Kill(ctx, w.env, "w1", true); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no binding for") {
		t.Fatalf("kill over an unbound agent = %v", err)
	}
	delete(w.h.agents, "w1:p9")
	w.h.jobs = map[string]bool{"w1:p9": true}
	if _, err := Kill(ctx, w.env, "w1", true); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "job in the foreground") {
		t.Fatalf("kill over a job = %v", err)
	}
	w.h.jobs = nil
	if _, err := Kill(ctx, w.env, "w1", false); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("kill without --yes = %v", err)
	}
	res, err := Kill(ctx, w.env, "w1", true)
	if err != nil || !res.Closed || len(w.h.wsClosed) != 1 || len(res.Agents) != 3 {
		t.Fatalf("kill = %+v %v", res, err)
	}
}

func TestRestoreInspectResume(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	icy := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	blue := w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	// After the restart: IcyKnoll is back without its herdr name; BlueLake's
	// pane is gone.
	a := w.h.agents["w1:p1"]
	a.Name = ""
	w.h.agents["w1:p1"] = a
	delete(w.h.agents, "w1:p2")
	delete(w.h.panes, "w1:p2")
	items, err := Restore(ctx, w.env)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RestoreItem{}
	for _, it := range items {
		got[it.MailName] = it
	}
	if got["IcyKnoll"].Action != "verified" || w.h.renamed[0] != "w1:p1=icyknoll" || got["BlueLake"].Action != "displaced" {
		t.Fatalf("restore = %+v, renamed %v", got, w.h.renamed)
	}
	if b, _ := w.env.Bindings.Get(ctx, blue.ID); b.Status != bindings.Displaced {
		t.Fatalf("blue = %s", b.Status)
	}
	// A send that never finished is pending.
	if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-pend", Verb: "send", Request: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	in, err := Inspect(ctx, w.env)
	if err != nil || len(in.Displaced) != 1 || len(in.Pending) != 1 || in.Pending[0].OperationID != "op-pend" {
		t.Fatalf("inspect = %+v %v", in, err)
	}
	// Resume: the dry run shows the argv, which keeps the model, the
	// effort and the guard settings; the pane holds an agent, so a run is
	// refused.
	w.env.LaunchSettings = `{"hooks":{}}`
	p, err := Resume(ctx, w.env, icy.ID, false)
	want := `claude --dangerously-skip-permissions --resume sess-icyknoll --model opus --effort xhigh --settings {"hooks":{}}`
	if err != nil || strings.Join(p.Argv, " ") != want || p.Ready {
		t.Fatalf("resume plan = %+v %v", p, err)
	}
	if _, err := Resume(ctx, w.env, icy.ID, true); err == nil {
		t.Fatal("a resume over a live agent ran")
	}
	delete(w.h.agents, "w1:p1")
	p, err = Resume(ctx, w.env, icy.ID, true)
	if err != nil || !p.Started || len(w.h.starts) != 1 {
		t.Fatalf("resume = %+v %v", p, err)
	}
	if b, _ := w.env.Bindings.Get(ctx, icy.ID); b.Generation != icy.Generation+1 {
		t.Fatalf("generation = %d", b.Generation)
	}
}

func TestExportImport(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", KeepIdentity: true})
	if err != nil || !res.Done {
		t.Fatal(err)
	}
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	dir := t.TempDir()
	x, err := ExportTo(ctx, w.env, "/p", dir, []string{"BlueLake"})
	if err != nil || len(x.Bindings) != 1 || x.Bindings[0].Token != "tok-BlueLake" {
		t.Fatalf("export = %+v %v", x, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "export.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("export.json = %v %v", fi, err)
	}
	target := newWorld(t)
	keep, notes, err := ImportFrom(ctx, target.env, dir)
	if err != nil || len(keep) != 1 || keep[0] != "BlueLake" {
		t.Fatalf("import = %v %v %v", keep, notes, err)
	}
	list, _ := target.env.Bindings.List(ctx, bindings.Filter{})
	if len(list) != 1 || list[0].Status != bindings.Displaced || list[0].Provenance != bindings.Imported || list[0].PaneID != "" {
		t.Fatalf("imported = %+v", list)
	}
	if tok, err := store.ReadSecret(target.env.InstanceDir, list[0].ID); err != nil || tok != "tok-BlueLake" {
		t.Fatalf("token = %q %v", tok, err)
	}
}

func TestLoadKeepAndGlob(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.toml"), filepath.Join(dir, "b.toml")
	writeFile(t, a, "[reap]\nkeep = [\"GreenCastle\"]\n[other]\nx = 1\n")
	writeFile(t, b, "[reap]\nprotected = [\"OldName\"]\n")
	names, err := LoadKeep(a, b, filepath.Join(dir, "missing.toml"))
	if err != nil || strings.Join(names, ",") != "GreenCastle,OldName" {
		t.Fatalf("keep = %v %v", names, err)
	}
	writeFile(t, b, "[reap]\nwho = [\"x\"]\n")
	if _, err := LoadKeep(b); err == nil {
		t.Fatal("an unknown [reap] key was accepted")
	}
	for _, c := range []struct {
		p, f string
		ok   bool
	}{{"internal/**", "internal/a/b.go", true}, {"internal/*.go", "internal/x.go", true}, {"a.go", "b.go", false}, {"x?.go", "xy.go", true}} {
		if globMatch(c.p, c.f) != c.ok {
			t.Errorf("%s %s", c.p, c.f)
		}
	}
}
