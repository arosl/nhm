package palette

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
	"github.com/arosl/nhm/internal/ui"
)

func u64(v uint64) *uint64 { return &v }

// fakeHerdr holds the agents of workspace w1 by pane and records prompts.
type fakeHerdr struct {
	mu      sync.Mutex
	agents  map[string]herdr.AgentInfo
	prompts []string
}

func (f *fakeHerdr) set(pane, kind, name string, st herdr.AgentStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := herdr.AgentInfo{PaneID: pane, WorkspaceID: "w1", Agent: kind, Name: name, AgentStatus: st, StateChangeSeq: u64(10)}
	if st == herdr.StatusIdle {
		a.CompletionSeq = u64(10)
	}
	f.agents[pane] = a
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

func (f *fakeHerdr) AgentGet(_ context.Context, pane string) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[pane]
	if !ok {
		return herdr.AgentInfo{}, &herdr.Error{Method: "agent.get", Code: herdr.CodeAgentNotFound}
	}
	return a, nil
}

func (f *fakeHerdr) AgentPrompt(_ context.Context, p herdr.AgentPromptParams) (herdr.AgentInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, p.Target+": "+p.Text)
	a := f.agents[p.Target]
	a.AgentStatus = herdr.StatusWorking
	return a, nil
}

func (f *fakeHerdr) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prompts)
}

type world struct {
	env  send.Env
	fake *fakeHerdr
	db   *store.DB
}

func newWorld(t *testing.T) *world {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	inst, _, err := store.Enroll(t.TempDir(), "/run/herdr/herdr.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &world{fake: &fakeHerdr{agents: map[string]herdr.AgentInfo{}}, db: db}
	w.env = send.Env{DB: db, Bindings: bindings.NewStore(db), Herdr: w.fake, InstanceDir: inst.Dir}
	return w
}

// bind creates a binding and its agent; it returns the pane as the
// palette sees it.
func (w *world) bind(t *testing.T, name, pane, kind string, st herdr.AgentStatus) Pane {
	t.Helper()
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: pane,
			HerdrAlias: strings.ToLower(name), Kind: kind}})
	if err != nil {
		t.Fatal(err)
	}
	w.fake.set(pane, kind, strings.ToLower(name), st)
	return Pane{ID: pane, Title: name, Type: kind, State: st, BindingID: b.ID, Generation: b.Generation}
}

func (w *world) sender() Sender {
	return func(ctx context.Context, req send.Request) (send.Result, error) { return send.Send(ctx, w.env, req) }
}

func baseRequest() send.Request {
	return send.Request{Session: "swarm", WorkspaceID: "w1", Scope: map[string]string{"workspace_id": "w1"}}
}

func receiptFor(t *testing.T, res send.Result, bindingID string) send.Receipt {
	t.Helper()
	for _, rc := range res.Targets {
		if rc.BindingID == bindingID {
			return rc
		}
	}
	t.Fatalf("no receipt for %s in %+v", bindingID, res.Targets)
	return send.Receipt{}
}

func TestBuildRequestFreezesTheTargets(t *testing.T) {
	ts := Targets{{Target: ui.Target{BindingID: "b1", Generation: 3, PaneID: "w1:p1"}},
		{Target: ui.Target{BindingID: "b2", Generation: 1, PaneID: "w1:p2"}}}
	base := baseRequest()
	base.WhenAny, base.Force, base.DryRun, base.OperationID = true, true, true, "op-x"
	base.Selector = send.Selector{All: true, Kinds: []string{"claude"}}
	req, err := BuildRequest(base, Payload{Text: "hi"}, ts, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Selector.All || len(req.Selector.Kinds) != 0 || len(req.Selector.Names) != 0 ||
		strings.Join(req.Selector.Panes, ",") != "w1:p1,w1:p2" {
		t.Fatalf("selector = %+v", req.Selector)
	}
	if len(req.Expect) != 2 || req.Expect["b1"] != 3 || req.Expect["b2"] != 1 {
		t.Fatalf("expect = %v", req.Expect)
	}
	if req.Message != "hi" || req.Template != "" || req.Source != history.SourcePalette {
		t.Fatalf("payload = %+v", req)
	}
	if req.WhenAny || req.Force || req.DryRun || req.OperationID != "" || req.Session != "swarm" || req.WorkspaceID != "w1" {
		t.Fatalf("request = %+v", req)
	}
	req, err = BuildRequest(base, Payload{Key: "fix", Text: "Fix {{thread}}", Template: true, Vars: map[string]string{"thread": "t1"}},
		ts, "/tmp/d/1/fix.md")
	if err != nil || req.Template != "/tmp/d/1/fix.md" || req.Message != "" || req.Vars["thread"] != "t1" {
		t.Fatalf("template request = %+v %v", req, err)
	}
}

func TestBuildRequestRefusals(t *testing.T) {
	ts := Targets{{Target: ui.Target{BindingID: "b1", Generation: 1, PaneID: "w1:p1"}}}
	if _, err := BuildRequest(baseRequest(), Payload{Text: "hi"}, nil, ""); !errors.Is(err, ErrNoTargets) {
		t.Fatalf("no targets: %v", err)
	}
	if _, err := BuildRequest(baseRequest(), Payload{Text: "  "}, ts, ""); !errors.Is(err, ErrEmptyDraft) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := BuildRequest(baseRequest(), Payload{Text: "x", Template: true}, ts, ""); err == nil {
		t.Fatal("a template payload with no file was accepted")
	}
}

// A draft sent as a template: its placeholders are filled per target,
// and a draft that starts with "---" is text, not front matter.
func TestBodiesAreTemplateBodies(t *testing.T) {
	w := newWorld(t)
	icy := w.bind(t, "IcyKnoll", "w1:p1", "claude", herdr.StatusIdle)
	blue := w.bind(t, "BlueLake", "w1:p2", "codex", herdr.StatusIdle)
	bodies := Bodies{Dir: t.TempDir()}
	p := Payload{Key: "hello", Text: "---\nname: x\n---\nHi {{agent}}, see {{thread}}.", Template: true,
		Vars: map[string]string{"thread": "nhm-1"}, Rev: 7}
	path, err := bodies.Path(p)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(bodies.Dir, "7", "hello.md") {
		t.Fatalf("path = %s", path)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file = %v %v", st, err)
	}
	req, err := BuildRequest(baseRequest(), p, Targets{icy.Frozen(), blue.Frozen()}, path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := send.Send(context.Background(), w.env, req)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(w.fake.sent(), "\n")
	want := "---\nname: x\n---\nHi IcyKnoll, see nhm-1."
	if !strings.Contains(got, "w1:p1: "+want) || !strings.Contains(got, "w1:p2: ---\nname: x\n---\nHi BlueLake, see nhm-1.") {
		t.Fatalf("prompts:\n%s", got)
	}
	if c := res.Counts(); c[send.StatusSubmitted] != 2 {
		t.Fatalf("counts = %v", c)
	}
	// A missing variable refuses the whole send before any prompt, and
	// the error names the prompt by its key.
	p.Vars, p.Rev = nil, 8
	path, _ = bodies.Path(p)
	req, _ = BuildRequest(baseRequest(), p, Targets{icy.Frozen()}, path)
	_, err = send.Send(context.Background(), w.env, req)
	if err == nil || !strings.Contains(err.Error(), `"hello"`) || !strings.Contains(err.Error(), "thread") {
		t.Fatalf("missing variable: %v", err)
	}
	if len(w.fake.sent()) != 2 {
		t.Fatal("a refused send prompted")
	}
	// A key that is not a file name gives "draft".
	if path, _ := bodies.Path(Payload{Key: "../x", Text: "t", Rev: 9}); filepath.Base(path) != "draft.md" {
		t.Fatalf("path = %s", path)
	}
}

// The palette's request through the core: the send reaches only the
// targets frozen at the keypress, refuses a busy one, and never swaps in
// the agent that took a target's place.
func TestPaletteSendThroughTheCore(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	icy := w.bind(t, "IcyKnoll", "w1:p1", "claude", herdr.StatusIdle)
	busy := w.bind(t, "RedFox", "w1:p2", "claude", herdr.StatusWorking)
	blue := w.bind(t, "BlueLake", "w1:p3", "codex", herdr.StatusIdle)
	moved := w.bind(t, "GreenOwl", "w1:p4", "codex", herdr.StatusIdle)
	ts := Targets{icy.Frozen(), busy.Frozen(), blue.Frozen(), moved.Frozen()}
	req, err := BuildRequest(baseRequest(), Payload{Text: "hello"}, ts, "")
	if err != nil {
		t.Fatal(err)
	}

	// After the keypress: BlueLake is recycled in place (a new
	// generation), GreenOwl hands its pane to a new identity.
	if _, err := w.env.Bindings.Occupy(ctx, blue.BindingID, blue.Generation, bindings.Occupant{WorkspaceID: "w1",
		TabID: "w1:t1", PaneID: "w1:p3", HerdrAlias: "bluelake", Kind: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.env.Bindings.Retire(ctx, moved.BindingID, moved.Generation, "handoff"); err != nil {
		t.Fatal(err)
	}
	heir := w.bind(t, "NewOtter", "w1:p4", "codex", herdr.StatusIdle)
	other := w.bind(t, "Bystander", "w1:p5", "claude", herdr.StatusIdle)

	sub := NewSubmitter(w.sender(), func(ctx context.Context, id string) (send.Result, error) { return send.Stored(ctx, w.db, id) },
		nil, func() (string, error) { return store.NewOperationID(time.Now()) })
	_, opID, cmd, err := sub.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	sm := cmd().(SubmitMsg)
	if sm.Outcome() != Answered || sm.OperationID != opID || sm.Result.OperationID != opID {
		t.Fatalf("submit = %+v", sm)
	}
	res := sm.Result
	if rc := receiptFor(t, res, icy.BindingID); rc.Status != send.StatusSubmitted {
		t.Fatalf("ready target = %+v", rc)
	}
	if rc := receiptFor(t, res, busy.BindingID); rc.Status != send.StatusRefused || rc.ErrorCode != "gate_busy" {
		t.Fatalf("busy target = %+v", rc)
	}
	if rc := receiptFor(t, res, blue.BindingID); rc.Status != send.StatusRefused || rc.ErrorCode != send.CodeGenerationChanged {
		t.Fatalf("recycled target = %+v", rc)
	}
	if rc := receiptFor(t, res, heir.BindingID); rc.Status != send.StatusRefused || rc.ErrorCode != send.CodeNotReviewed {
		t.Fatalf("the heir in a target's pane = %+v", rc)
	}
	if rc := receiptFor(t, res, moved.BindingID); rc.Status != send.StatusRefused || rc.ErrorCode != send.CodeNotSelected {
		t.Fatalf("the target that handed off = %+v", rc)
	}
	for _, rc := range res.Targets {
		if rc.BindingID == other.BindingID {
			t.Fatalf("an agent outside the targets got a receipt: %+v", rc)
		}
	}
	if got := w.fake.sent(); len(got) != 1 || got[0] != "w1:p1: hello" {
		t.Fatalf("prompts = %v", got)
	}

	// The core recorded it: a lost answer reads back by id.
	rm := sub.Reconcile(opID)().(ReconcileMsg)
	if rm.Err != nil || len(rm.Result.Targets) != len(res.Targets) {
		t.Fatalf("reconcile = %+v", rm)
	}
}

func TestSubmitRunsEvenIfTheProgramQuits(t *testing.T) {
	var calls int
	var mu sync.Mutex
	release := make(chan struct{})
	sub := NewSubmitter(func(_ context.Context, req send.Request) (send.Result, error) {
		<-release
		mu.Lock()
		calls++
		mu.Unlock()
		return send.Result{OperationID: req.OperationID}, nil
	}, nil, nil, func() (string, error) { return "op-1", nil })
	_, opID, _, err := sub.Submit(send.Request{Message: "x"})
	if err != nil || opID != "op-1" {
		t.Fatalf("submit = %q %v", opID, err)
	}
	if !sub.InFlight() {
		t.Fatal("not in flight")
	}
	// One submission at a time.
	if _, _, _, err := sub.Submit(send.Request{Message: "x"}); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second submit: %v", err)
	}
	// The program never runs the command: the send still runs, and Wait
	// returns when it ends.
	close(release)
	sub.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 || sub.InFlight() {
		t.Fatalf("calls = %d, in flight = %v", calls, sub.InFlight())
	}
}

func TestSubmitterRequestsAndOutcomes(t *testing.T) {
	var got []send.Request
	var mu sync.Mutex
	result := func(req send.Request) (send.Result, error) {
		switch req.Message {
		case "refused":
			return send.Result{}, errors.New("no agent matches the selection")
		case "lost":
			return send.Result{OperationID: req.OperationID}, errors.New("receipt write failed")
		case "panic":
			panic("boom")
		}
		return send.Result{OperationID: req.OperationID}, nil
	}
	var queries []string
	memory := func(_ context.Context, text string, _ MemoryModes) (Memory, error) {
		mu.Lock()
		queries = append(queries, text)
		mu.Unlock()
		if text == "fails" {
			return Memory{}, errors.New("cm: not installed")
		}
		return Memory{Query: text, Block: "[memory]"}, nil
	}
	sub := NewSubmitter(func(_ context.Context, req send.Request) (send.Result, error) {
		mu.Lock()
		got = append(got, req)
		mu.Unlock()
		return result(req)
	}, nil, memory, func() (string, error) { return store.NewOperationID(time.Now()) })

	s1, c1 := sub.Memory("first draft", MemoryModes{}, 3)
	s2, c2 := sub.Memory("fails", MemoryModes{}, 4)
	if s2 <= s1 {
		t.Fatalf("sequence %d then %d", s1, s2)
	}
	// Answers can come in any order; each carries its request and the
	// draft revision it was made for.
	if m := c2().(MemoryMsg); m.Seq != s2 || m.Rev != 4 || m.Err == nil {
		t.Fatalf("second preview = %+v", m)
	}
	if m := c1().(MemoryMsg); m.Seq != s1 || m.Rev != 3 || m.Err != nil || m.Memory.Query != "first draft" || m.Memory.Block != "[memory]" {
		t.Fatalf("first preview = %+v", m)
	}
	if len(got) != 0 || len(queries) != 2 {
		t.Fatalf("a memory preview sends nothing: sends %d, queries %v", len(got), queries)
	}
	none := NewSubmitter(nil, nil, nil, nil)
	if _, cmd := none.Memory("x", MemoryModes{}, 1); cmd().(MemoryMsg).Err == nil {
		t.Fatal("memory with no retrieval")
	}
	if m := none.Reconcile("op-1")().(ReconcileMsg); m.Err == nil {
		t.Fatal("reconcile with no reader")
	}

	for msg, want := range map[string]Outcome{"ok": Answered, "refused": Refused, "lost": Unknown, "panic": Unknown} {
		seq, opID, cmd, err := sub.Submit(send.Request{Message: msg, DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		m := cmd().(SubmitMsg)
		if m.Outcome() != want || m.Seq != seq || m.OperationID != opID || !strings.HasPrefix(opID, "op-") {
			t.Fatalf("%s: outcome %v, msg %+v", msg, m.Outcome(), m)
		}
		if msg == "panic" && (!m.Panicked || m.Err == nil) {
			t.Fatalf("panic: %+v", m)
		}
		sub.Wait()
	}
	mu.Lock()
	defer mu.Unlock()
	for _, r := range got {
		if r.DryRun || !strings.HasPrefix(r.OperationID, "op-") {
			t.Fatalf("a submission is real and has its id: %+v", r)
		}
	}
}
