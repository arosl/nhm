package send

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/store/history"
)

func u64(v uint64) *uint64 { return &v }

type promptCall struct {
	pane, text string
	wait       *herdr.PromptWait
}

// fakeHerdr holds the agents of workspace w1 by pane.
type fakeHerdr struct {
	mu        sync.Mutex
	agents    map[string]herdr.AgentInfo
	promptErr map[string]error
	prompts   []promptCall
	// onErr runs, under the lock, when a prompt fails.
	onErr func(pane string)
	// screens are the panes' last lines (none: an empty screen); a read
	// of a pane in readErr fails. reads counts the reads.
	screens map[string]string
	readErr map[string]error
	reads   int
}

func newFake() *fakeHerdr {
	return &fakeHerdr{agents: map[string]herdr.AgentInfo{}, promptErr: map[string]error{}, screens: map[string]string{}, readErr: map[string]error{}}
}

func (f *fakeHerdr) add(pane, kind, name string, st herdr.AgentStatus, seq uint64) {
	a := herdr.AgentInfo{PaneID: pane, WorkspaceID: "w1", Agent: kind, Name: name, AgentStatus: st, StateChangeSeq: u64(seq)}
	if st == herdr.StatusIdle || st == herdr.StatusDone {
		a.CompletionSeq = u64(seq)
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
	f.prompts = append(f.prompts, promptCall{pane: p.Target, text: p.Text, wait: p.Wait})
	if err := f.promptErr[p.Target]; err != nil {
		if f.onErr != nil {
			f.onErr(p.Target)
		}
		return herdr.AgentInfo{}, err
	}
	a := f.agents[p.Target]
	a.AgentStatus = herdr.StatusWorking
	*a.StateChangeSeq++
	a.CompletionSeq = nil
	f.agents[p.Target] = a
	return a, nil
}

func (f *fakeHerdr) AgentRead(_ context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if err := f.readErr[p.Target]; err != nil {
		return herdr.PaneRead{}, err
	}
	return herdr.PaneRead{PaneID: p.Target, Source: p.Source, Text: f.screens[p.Target]}, nil
}

func (f *fakeHerdr) promptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

type world struct {
	env     Env
	fake    *fakeHerdr
	db      *store.DB
	hist    []*history.HistoryEntry
	created map[string]bindings.Binding
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
	w := &world{fake: newFake(), db: db, created: map[string]bindings.Binding{}}
	w.env = Env{DB: db, Bindings: bindings.NewStore(db), Herdr: w.fake, InstanceDir: inst.Dir,
		History: func(e *history.HistoryEntry) error { w.hist = append(w.hist, e); return nil }}
	return w
}

// bind creates a binding and its agent in herdr.
func (w *world) bind(t *testing.T, name, tab, pane, kind string, st herdr.AgentStatus) bindings.Binding {
	t.Helper()
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: tab, PaneID: pane,
			HerdrAlias: strings.ToLower(name), Kind: kind, Model: "m-" + kind}})
	if err != nil {
		t.Fatal(err)
	}
	w.fake.add(pane, kind, strings.ToLower(name), st, 10)
	w.created[name] = b
	return b
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func base(req Request) Request {
	req.Session, req.WorkspaceID = "swarm", "w1"
	return req
}

func TestSendSubmitsOnceWithBaselineAndReceipt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusDone)
	req := base(Request{Message: "Review {{file}} please.", OperationID: "op-1", Caller: "w1:p9"})
	res, err := Send(ctx, w.env, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Targets) != 2 || res.Replayed || res.InProgress || res.CompletedAt == nil {
		t.Fatalf("res = %+v", res)
	}
	for _, rc := range res.Targets {
		if rc.Status != StatusSubmitted || rc.Baseline == nil || rc.Baseline.StateChangeSeq != 10 || rc.Result.StateAfter != "working" {
			t.Fatalf("receipt = %+v", rc)
		}
	}
	// A plain prompt goes out as typed, with a second over herdr's
	// five-second gate, so herdr answers a stall as a stall (nhm-0li).
	if w.fake.promptCount() != 2 || w.fake.prompts[0].text != "Review {{file}} please." ||
		*w.fake.prompts[0].wait.TimeoutMS != 6000 || len(w.fake.prompts[0].wait.Until) != 2 {
		t.Fatalf("prompts = %+v", w.fake.prompts)
	}
	digest, n := Digest("Review {{file}} please.")
	if res.PayloadSHA256 != digest || res.PayloadBytes != n || res.Targets[0].PayloadSHA256 != digest {
		t.Fatalf("digest = %s %d", res.PayloadSHA256, res.PayloadBytes)
	}
	if res.Targets[0].Key != "0.1" || res.Targets[1].Key != "1.1" {
		t.Fatalf("keys = %s %s", res.Targets[0].Key, res.Targets[1].Key)
	}
	op, err := w.db.Operation(ctx, "op-1")
	if err != nil || op.Status != OpCompleted || strings.Contains(op.RequestJSON, "please") {
		t.Fatalf("operation = %+v %v", op, err)
	}
	if len(w.hist) != 1 || w.hist[0].OperationID != "op-1" || !w.hist[0].Success {
		t.Fatalf("history = %+v", w.hist)
	}

	// The same id sends nothing and returns the stored receipt.
	again, err := Send(ctx, w.env, req)
	if err != nil || !again.Replayed || w.fake.promptCount() != 2 || again.Targets[0].Status != StatusSubmitted {
		t.Fatalf("replay = %+v %v, %d prompts", again, err, w.fake.promptCount())
	}
	// The agents left: the retry still returns the stored receipt.
	w.fake.mu.Lock()
	w.fake.agents = map[string]herdr.AgentInfo{}
	w.fake.mu.Unlock()
	if again, err = Send(ctx, w.env, req); err != nil || !again.Replayed || len(again.Targets) != 2 {
		t.Fatalf("replay after the agents left = %+v %v", again, err)
	}
	// The same id with another prompt is a conflict.
	req.Message = "something else"
	if _, err := Send(ctx, w.env, req); !errors.Is(err, store.ErrOperationIDConflict) {
		t.Fatalf("conflict = %v", err)
	}
	// The wait reads the receipt's baseline.
	rs, err := wait.Receipts(ctx, w.db.SQL(), "op-1")
	if err != nil || rs[0].Baseline == nil || rs[0].Status != wait.Submitted {
		t.Fatalf("wait receipts = %+v %v", rs, err)
	}
}

func TestDeliveryGate(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusWorking)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "claude", herdr.StatusBlocked)
	w.bind(t, "RedStone", "w1:t3", "w1:p3", "claude", herdr.StatusIdle)
	a := w.fake.agents["w1:p3"]
	a.Agent = "codex"
	w.fake.agents["w1:p3"] = a
	res, err := Send(ctx, w.env, base(Request{Message: "hi"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"IcyKnoll": "gate_busy", "BlueLake": "gate_blocked", "RedStone": "gate_unsafe"}
	for _, rc := range res.Targets {
		if rc.Status != StatusRefused || rc.ErrorCode != want[rc.MailName] {
			t.Errorf("%s = %s %s", rc.MailName, rc.Status, rc.ErrorCode)
		}
	}
	if w.fake.promptCount() != 0 {
		t.Fatalf("%d prompts", w.fake.promptCount())
	}
	// --when any admits busy; blocked and unsafe stay refused.
	res, err = Send(ctx, w.env, base(Request{Message: "hi", WhenAny: true}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Targets[0].Status != StatusSubmitted || res.Targets[1].Status != StatusRefused || res.Targets[2].Status != StatusRefused {
		t.Fatalf("when any = %s %s %s", res.Targets[0].Status, res.Targets[1].Status, res.Targets[2].Status)
	}
}

func TestPromptErrorsMapToSubmissions(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "claude", herdr.StatusIdle)
	w.bind(t, "RedStone", "w1:t3", "w1:p3", "claude", herdr.StatusIdle)
	w.bind(t, "GreenPike", "w1:t4", "w1:p4", "claude", herdr.StatusIdle)
	w.fake.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentPromptStalled}
	w.fake.promptErr["w1:p2"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	w.fake.promptErr["w1:p3"] = &herdr.TransportError{Method: "agent.prompt", Err: &net.OpError{Op: "dial", Err: errors.New("refused")}}
	w.fake.promptErr["w1:p4"] = &herdr.TransportError{Method: "agent.prompt", Err: errors.New("EOF")}
	res, err := Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-err"}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"IcyKnoll": StatusPossiblySubmitted, "BlueLake": StatusRefused, "RedStone": StatusFailed, "GreenPike": StatusPossiblySubmitted}
	for _, rc := range res.Targets {
		if rc.Status != want[rc.MailName] {
			t.Errorf("%s = %s (%s)", rc.MailName, rc.Status, rc.ErrorCode)
		}
	}
	out := SendOutput(res, "json", time.Now())
	if out.Success || out.ErrorCode != robot.ErrCodeOperationOutcomeUnknown {
		t.Fatalf("projection = %+v", out.RobotResponse)
	}
	states := map[string]string{}
	for _, a := range out.Operation.Admissions {
		states[a.Target] = a.State
	}
	if states["0.1"] != robot.AdmissionUnknown || states["1.1"] != robot.AdmissionRejected || states["2.1"] != robot.AdmissionRejected {
		t.Fatalf("admissions = %v", states)
	}
}

func TestNeverPromptsOutsideTheWorkspace(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	// The pane moved to w2. Its old id stays an alias, so agent.get by
	// w1:p1 answers with the agent in w2:p5; the binding still says w1.
	a := w.fake.agents["w1:p1"]
	a.PaneID, a.WorkspaceID = "w2:p5", "w2"
	w.fake.agents["w1:p1"] = a
	dry, err := Send(ctx, w.env, base(Request{Message: "hi", DryRun: true, WhenAny: true}))
	if err != nil || dry.Targets[0].Status != StatusRefused {
		t.Fatalf("dry run = %+v %v", dry.Targets, err)
	}
	res, err := Send(ctx, w.env, base(Request{Message: "hi", WhenAny: true}))
	if err != nil {
		t.Fatal(err)
	}
	rc := res.Targets[0]
	if rc.Status != StatusRefused || rc.ErrorCode != "gate_unsafe" || !strings.Contains(rc.Result.Reason, "w2") || w.fake.promptCount() != 0 {
		t.Fatalf("moved = %+v, %d prompts", rc, w.fake.promptCount())
	}
}

func TestStallWithACompletionStaysPossible(t *testing.T) {
	// herdr's stall, and its timeout (nhm-0li), get the same note.
	for _, code := range []string{herdr.CodeAgentPromptStalled, herdr.CodeTimeout} {
		w := newWorld(t)
		ctx := context.Background()
		w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
		w.fake.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: code}
		// The turn ran and ended inside herdr's five seconds (exp.3).
		w.fake.onErr = func(pane string) {
			a := w.fake.agents[pane]
			a.AgentStatus, a.StateChangeSeq, a.CompletionSeq = herdr.StatusDone, u64(12), u64(12)
			w.fake.agents[pane] = a
		}
		res, err := Send(ctx, w.env, base(Request{Message: "hi"}))
		if err != nil {
			t.Fatal(err)
		}
		rc := res.Targets[0]
		if rc.Status != StatusPossiblySubmitted || rc.ErrorCode != code || !strings.Contains(rc.Result.Reason, "completion_seq 12 > 10") ||
			rc.Result.StateAfter != "done" {
			t.Fatalf("%s: stall = %+v", code, rc)
		}
	}
}

func TestSelection(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusDone)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusDone)
	w.bind(t, "RedStone", "w1:t3", "w1:p3", "claude", herdr.StatusWorking)
	w.fake.add("w1:p7", "claude", "loosefox", herdr.StatusIdle, 1)
	agents, _ := w.fake.AgentList(ctx)
	names := func(ts []Target) string {
		var out []string
		for _, t := range ts {
			out = append(out, t.Binding.MailName)
		}
		return strings.Join(out, ",")
	}
	sel := func(s Selector, caller string) ([]Target, []Candidate, error) {
		return Select(ctx, w.env.Bindings, agents, "w1", "", caller, s)
	}
	if ts, _, err := sel(Selector{}, "w1:p1"); err != nil || names(ts) != "BlueLake,RedStone" {
		t.Fatalf("default without the caller = %s %v", names(ts), err)
	}
	// --state done leaves Codex out unless named.
	if ts, _, _ := sel(Selector{States: []herdr.AgentStatus{herdr.StatusDone}}, ""); names(ts) != "IcyKnoll" {
		t.Fatalf("done = %s", names(ts))
	}
	if ts, _, _ := sel(Selector{Names: []string{"bluelake"}, States: []herdr.AgentStatus{herdr.StatusDone}}, ""); names(ts) != "BlueLake" {
		t.Fatalf("named codex = %s", names(ts))
	}
	// Names and panes are a union; a kind narrows them.
	if ts, _, _ := sel(Selector{Names: []string{"IcyKnoll"}, Panes: []string{"1.1", "w1:p3"}, Kinds: []string{"claude"}}, ""); names(ts) != "IcyKnoll,RedStone" {
		t.Fatalf("union = %s", names(ts))
	}
	if ts, _, _ := sel(Selector{Limit: 2}, ""); names(ts) != "IcyKnoll,BlueLake" {
		t.Fatalf("limit = %s", names(ts))
	}
	// A herdr name without a binding is a candidate, never a target.
	ts, cs, err := sel(Selector{Names: []string{"LooseFox"}}, "")
	if err != nil || len(ts) != 0 || len(cs) != 1 || !strings.Contains(cs[0].Hint, "nhm adopt LooseFox --pane w1:p7") {
		t.Fatalf("candidate = %v %+v %v", names(ts), cs, err)
	}
	for _, s := range []Selector{{Names: []string{"Nobody"}}, {Panes: []string{"9.9"}}, {Kinds: []string{"pi"}}} {
		if _, _, err := sel(s, ""); !errors.Is(err, ErrNoTargets) {
			t.Errorf("%+v = %v", s, err)
		}
	}
	// A candidate becomes a refused receipt with the hint.
	res, err := Send(ctx, w.env, base(Request{Message: "hi", Selector: Selector{Names: []string{"LooseFox"}}}))
	if err != nil || len(res.Targets) != 1 || res.Targets[0].Status != StatusRefused || res.Targets[0].ErrorCode != "unbound" {
		t.Fatalf("candidate send = %+v %v", res.Targets, err)
	}
}

func TestTemplatesRenderPerTarget(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "claude", herdr.StatusIdle)
	project := t.TempDir()
	tdir := project + "/.nhm/templates"
	writeFile(t, tdir+"/kick.md", "---\nname: kick\n---\n{{agent}} ({{herdr}}, {{model}}): take {{bead_id}} {{bead_title}} [{{bead_priority}}] in {{session}}, {{x}}.")
	w.env.Bead = func(_ context.Context, root, id string) (Bead, error) {
		if root != project {
			t.Errorf("bead read in %s", root)
		}
		return Bead{ID: id, Title: "Do it", Priority: 1}, nil
	}
	req := base(Request{Template: "kick", BeadID: "acfs-1", Vars: map[string]string{"x": "now"}, ProjectRoot: project})
	res, err := Send(ctx, w.env, req)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"IcyKnoll (icyknoll, m-claude): take acfs-1 Do it [P1] in swarm, now.",
		"BlueLake (bluelake, m-claude): take acfs-1 Do it [P1] in swarm, now."}
	for i, p := range w.fake.prompts {
		if p.text != want[0] && p.text != want[1] {
			t.Errorf("prompt %d = %q", i, p.text)
		}
	}
	if res.Targets[0].PayloadSHA256 == res.Targets[1].PayloadSHA256 {
		t.Fatal("per-target payloads share a digest")
	}
	// A placeholder that nothing fills refuses before any claim.
	req.Vars, req.OperationID = nil, "op-missing"
	_, err = Send(ctx, w.env, req)
	var unresolved *templates.UnresolvedVariablesError
	if !errors.As(err, &unresolved) {
		t.Fatalf("missing var = %v", err)
	}
	if _, err := w.db.Operation(ctx, "op-missing"); err == nil {
		t.Fatal("a refused send claimed its id")
	}
	for _, r := range []Request{{}, {Message: "a", Template: "kick"}, {Message: "a", BeadID: "x"}} {
		if _, err := Send(ctx, w.env, base(r)); err == nil {
			t.Errorf("%+v was accepted", r)
		}
	}
	// --file is the prompt without a template. (The first send left the
	// agents working, so the gate needs --when any.)
	if _, err := Send(ctx, w.env, base(Request{FileContent: "from a file", WhenAny: true, Selector: Selector{Names: []string{"IcyKnoll"}}})); err != nil {
		t.Fatal(err)
	}
	if last := w.fake.prompts[len(w.fake.prompts)-1].text; last != "from a file" {
		t.Fatalf("file prompt = %q", last)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "claude", herdr.StatusBlocked)
	res, err := Send(ctx, w.env, base(Request{Message: "hi", DryRun: true, OperationID: "op-dry"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.OperationID != "" || res.Targets[0].Status != "would_send" || res.Targets[1].Status != StatusRefused ||
		len(res.Rendered) != 2 || w.fake.promptCount() != 0 || len(w.hist) != 0 {
		t.Fatalf("dry run = %+v", res)
	}
	if _, err := w.db.Operation(ctx, "op-dry"); err == nil {
		t.Fatal("a dry run claimed its id")
	}
	out := SendOutput(res, "json", time.Now())
	if !out.Success || !out.DryRun || len(out.WouldSendTo) != 1 || out.WouldSendTo[0] != "0.1" || len(out.Failed) != 1 || out.Operation != nil {
		t.Fatalf("dry run projection = %+v", out)
	}
}

func TestDispatchRefusesAChangedBinding(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	if _, err := w.env.Bindings.Occupy(ctx, b.ID, b.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
		PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-x", Verb: Verb, Request: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rc := Receipt{OperationID: "op-x", Key: "0.1", BindingID: b.ID, Status: StatusPending, UpdatedAt: time.Now()}
	rdb := receiptDB{w.db}
	if err := rdb.insert(ctx, rc); err != nil {
		t.Fatal(err)
	}
	a := w.fake.agents["w1:p1"]
	if err := dispatch(ctx, w.env, rdb, Target{Binding: b, Agent: &a, Key: "0.1"}, &rc, "hi", false, nil); err != nil {
		t.Fatal(err)
	}
	if rc.Status != StatusRefused || rc.ErrorCode != "generation_changed" || w.fake.promptCount() != 0 {
		t.Fatalf("= %+v", rc)
	}
}

func TestStoppedDispatchIsUnknown(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	if _, err := w.db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-crash", Verb: Verb,
		Scope: opScope{Session: "swarm", WorkspaceID: "w1"}, Request: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rdb := receiptDB{w.db}
	rc := Receipt{OperationID: "op-crash", Key: "0.1", BindingID: b.ID, Generation: b.Generation, Status: StatusAttemptStarted,
		UpdatedAt: time.Now(), Result: ReceiptResult{Key: "0.1"}}
	if err := rdb.insert(ctx, rc); err != nil {
		t.Fatal(err)
	}
	res, err := Stored(ctx, w.db, "op-crash")
	if err != nil || !res.InProgress {
		t.Fatalf("stored = %+v %v", res, err)
	}
	out := ReceiptOutput(res, "json", time.Now())
	if out.Operation.Status != robot.OperationInProgress || out.Outcome != nil || out.Operation.Admissions[0].State != robot.AdmissionUnknown {
		t.Fatalf("receipt = %+v", out)
	}
	if s := SendOutput(res, "json", time.Now()); s.Success || s.ErrorCode != robot.ErrCodeOperationInProgress {
		t.Fatalf("send projection = %+v", s.RobotResponse)
	}
}

func TestObserveRecordsTheCompletion(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	if _, err := Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-obs"})); err != nil {
		t.Fatal(err)
	}
	// The turn finishes.
	w.fake.mu.Lock()
	a := w.fake.agents["w1:p1"]
	a.AgentStatus, a.StateChangeSeq, a.CompletionSeq = herdr.StatusDone, u64(12), u64(12)
	w.fake.agents["w1:p1"] = a
	w.fake.mu.Unlock()
	wenv := wait.Env{Herdr: waitHerdr{w.fake}, Bindings: w.env.Bindings}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	results, err := Observe(wctx, w.env, wenv, "op-obs", wait.UntilComplete, time.Millisecond)
	if err != nil || len(results) != 1 || results[0].Outcome != wait.Complete {
		t.Fatalf("observe = %+v %v", results, err)
	}
	res, _ := Stored(ctx, w.db, "op-obs")
	c := res.Targets[0].Result.Completion
	if c == nil || c.Observation != ObsCompleted || c.Correlation != wait.Strong || *c.CompletedSeq != 12 {
		t.Fatalf("completion = %+v", c)
	}
	ack := AckOutput(res, results, []int{0}, time.Second, "json", time.Now(), time.Now())
	if !ack.Success || len(ack.Confirmations) != 1 || ack.Confirmations[0].AckType != robot.AckPromptReturned {
		t.Fatalf("ack = %+v", ack)
	}
}

// waitHerdr gives the fake the two calls wait needs beyond send's.
type waitHerdr struct{ *fakeHerdr }

func (w waitHerdr) AgentWait(context.Context, herdr.AgentWaitParams) (herdr.AgentInfo, error) {
	return herdr.AgentInfo{}, &herdr.Error{Code: herdr.CodeTimeout}
}

func (w waitHerdr) AgentSendKeys(context.Context, string, ...string) error { return nil }

func TestParseBead(t *testing.T) {
	b, err := parseBead([]byte(`[{"id":"acfs-1","title":"T","priority":2,"issue_type":"task","status":"open","description":"D"}]`), "acfs-1")
	if err != nil || b.Title != "T" || b.Priority != 2 || b.IssueType != "task" {
		t.Fatalf("= %+v %v", b, err)
	}
	if _, err := parseBead([]byte(`[]`), "acfs-1"); err == nil {
		t.Fatal("an empty answer was accepted")
	}
}
