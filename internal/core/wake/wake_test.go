package wake

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

type fakeHerdr struct {
	mu        sync.Mutex
	agents    map[string]herdr.AgentInfo
	promptErr map[string]error
	prompts   []herdr.AgentPromptParams
	// screens are the panes' last lines (none: an empty screen); readErr
	// fails every read.
	screens map[string]string
	readErr error
}

func (f *fakeHerdr) AgentRead(_ context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return herdr.PaneRead{}, f.readErr
	}
	return herdr.PaneRead{PaneID: p.Target, Source: p.Source, Text: f.screens[p.Target]}, nil
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
	f.prompts = append(f.prompts, p)
	if err := f.promptErr[p.Target]; err != nil {
		return herdr.AgentInfo{}, err
	}
	return f.agents[p.Target], nil
}

func (f *fakeHerdr) set(pane string, st herdr.AgentStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[pane]
	a.AgentStatus = st
	f.agents[pane] = a
}

func (f *fakeHerdr) promptsTo(pane string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.prompts {
		if p.Target == pane {
			n++
		}
	}
	return n
}

// fakeMail answers fetch_inbox from inbox (by agent) and records the
// other calls.
type fakeMail struct {
	inbox map[string][]InboxMessage
	calls []string
}

func (m *fakeMail) Call(_ context.Context, tool string, args, out any) error {
	a := args.(map[string]any)
	m.calls = append(m.calls, tool)
	switch tool {
	case "fetch_inbox":
		if a["mark_read"] != false || a["unread_only"] != true {
			return errors.New("fetch_inbox must peek")
		}
		msgs, ok := m.inbox[a["agent_name"].(string)]
		if !ok {
			return &agentmail.ToolError{Tool: tool, Message: "Agent not found"}
		}
		data, _ := json.Marshal(msgs)
		return json.Unmarshal(data, out)
	case "mark_message_read":
		return nil
	}
	return errors.New("unexpected tool " + tool)
}

type world struct {
	env  Env
	h    *fakeHerdr
	mail *fakeMail
	now  time.Time
	b    map[string]bindings.Binding
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
	w := &world{h: &fakeHerdr{agents: map[string]herdr.AgentInfo{}, promptErr: map[string]error{}},
		mail: &fakeMail{inbox: map[string][]InboxMessage{}}, now: time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC),
		b: map[string]bindings.Binding{}}
	w.env = Env{DB: db, Bindings: bindings.NewStore(db), Herdr: w.h, Mail: w.mail, Config: DefaultConfig(),
		Now: func() time.Time { return w.now }}
	return w
}

func (w *world) bind(t *testing.T, name, pane, kind string, st herdr.AgentStatus) bindings.Binding {
	t.Helper()
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "t-" + pane, PaneID: pane,
			HerdrAlias: strings.ToLower(name), Kind: kind}})
	if err != nil {
		t.Fatal(err)
	}
	w.h.agents[pane] = herdr.AgentInfo{PaneID: pane, WorkspaceID: "w1", Agent: kind, Name: strings.ToLower(name), AgentStatus: st}
	w.b[name] = b
	return b
}

func sent(id int64, from, thread string, to ...string) Sent {
	return Sent{ProjectKey: "/p", Message: Message{ID: id, Sender: from, ThreadID: thread}, Recipients: to}
}

func pendingIDs(t *testing.T, w *world, name string) []string {
	t.Helper()
	rows, err := pending(context.Background(), w.env.DB.SQL(), w.b[name].ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}

func TestMailSentNudgesIdleOnceAndLeavesWorkingPending(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:p2", "claude", herdr.StatusWorking)
	rep, err := MailSent(ctx, w.env, []Sent{sent(7, "SageOak", "acfs-1", "IcyKnoll", "BlueLake", "Nobody")})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recorded != 2 || len(rep.Unbound) != 1 || rep.Unbound[0] != "Nobody" || len(rep.Decisions) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	icy, blue := rep.Decisions[0], rep.Decisions[1]
	if icy.Action != ActionNudged || icy.Outcome != send.StatusSubmitted || icy.OperationID == "" || icy.Trigger != TriggerMailSent {
		t.Fatalf("idle decision = %+v", icy)
	}
	if blue.Action != ActionSkipped || !strings.Contains(blue.Reason, "busy") {
		t.Fatalf("working decision = %+v", blue)
	}
	// One line, no wait: the sender spends nothing.
	if len(w.h.prompts) != 1 || w.h.prompts[0].Text != Prompt || w.h.prompts[0].Wait != nil || w.h.prompts[0].Target != "w1:p1" {
		t.Fatalf("prompts = %+v", w.h.prompts)
	}
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 0 {
		t.Fatalf("IcyKnoll still pending: %v", got)
	}
	if got := pendingIDs(t, w, "BlueLake"); len(got) != 1 || got[0] != "7" {
		t.Fatalf("BlueLake pending = %v", got)
	}
	// The same message again (a hook retry): no new row, no second nudge.
	w.now = w.now.Add(10 * time.Minute)
	rep, err = MailSent(ctx, w.env, []Sent{sent(7, "SageOak", "acfs-1", "IcyKnoll")})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recorded != 0 || rep.Decisions[0].Action != ActionSkipped || w.h.promptsTo("w1:p1") != 1 {
		t.Fatalf("retry = %+v, prompts %d", rep, w.h.promptsTo("w1:p1"))
	}
	// BlueLake finishes: the maintenance pass delivers.
	w.h.set("w1:p2", herdr.StatusDone)
	w.env.Mail = nil
	rep, err = Pass(ctx, w.env, nil, TriggerMaintain)
	if err != nil {
		t.Fatal(err)
	}
	if w.h.promptsTo("w1:p2") != 1 || len(rep.Decisions) != 1 || rep.Decisions[0].MailName != "BlueLake" {
		t.Fatalf("pass = %+v", rep)
	}
}

func TestCooldownSetsWakeAfterAndMaintenanceDelivers(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	if _, err := MailSent(ctx, w.env, []Sent{sent(1, "SageOak", "t1", "IcyKnoll")}); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(30 * time.Second)
	rep, err := MailSent(ctx, w.env, []Sent{sent(2, "SageOak", "t1", "IcyKnoll")})
	if err != nil {
		t.Fatal(err)
	}
	d := rep.Decisions[0]
	want := w.now.Add(90 * time.Second)
	if d.Action != ActionSkipped || !strings.HasPrefix(d.Reason, "cooldown") || d.WakeAfter == nil || !d.WakeAfter.Equal(want) {
		t.Fatalf("decision = %+v", d)
	}
	var after string
	if err := w.env.DB.SQL().QueryRow(`SELECT wake_after FROM wake WHERE message_id = '2'`).Scan(&after); err != nil || after != store.FormatTime(want) {
		t.Fatalf("wake_after = %q, %v", after, err)
	}
	w.env.Mail = nil
	w.now = w.now.Add(60 * time.Second)
	if rep, _ := Pass(ctx, w.env, nil, TriggerMaintain); rep.Decisions[0].Action != ActionSkipped || w.h.promptsTo("w1:p1") != 1 {
		t.Fatalf("early pass = %+v", rep)
	}
	w.now = w.now.Add(31 * time.Second)
	if rep, _ := Pass(ctx, w.env, nil, TriggerMaintain); rep.Decisions[0].Action != ActionNudged || w.h.promptsTo("w1:p1") != 2 {
		t.Fatalf("due pass = %+v", rep)
	}
}

func TestRateLimitPerSenderAndThread(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	for i := int64(1); i <= 3; i++ {
		rep, err := MailSent(ctx, w.env, []Sent{sent(i, "SageOak", "t1", "IcyKnoll")})
		if err != nil || rep.Decisions[0].Action != ActionNudged {
			t.Fatalf("nudge %d = %+v, %v", i, rep, err)
		}
		w.now = w.now.Add(3 * time.Minute)
	}
	rep, err := MailSent(ctx, w.env, []Sent{sent(4, "SageOak", "t1", "IcyKnoll")})
	if err != nil {
		t.Fatal(err)
	}
	d := rep.Decisions[0]
	if d.Action != ActionSkipped || !strings.HasPrefix(d.Reason, "rate limit") || d.WakeAfter == nil {
		t.Fatalf("fourth = %+v", d)
	}
	// Another thread from the same sender is not limited.
	rep, err = MailSent(ctx, w.env, []Sent{sent(5, "SageOak", "t2", "IcyKnoll")})
	if err != nil || rep.Decisions[0].Action != ActionNudged {
		t.Fatalf("other thread = %+v, %v", rep, err)
	}
	// It carried message 4 too: one nudge per message id.
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 0 {
		t.Fatalf("pending = %v", got)
	}
}

func TestPossiblySubmittedIsAnnouncedAndVisible(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	w.h.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: "timeout"}
	rep, err := MailSent(ctx, w.env, []Sent{sent(1, "SageOak", "t1", "IcyKnoll")})
	if err != nil {
		t.Fatal(err)
	}
	if d := rep.Decisions[0]; d.Outcome != send.StatusPossiblySubmitted {
		t.Fatalf("decision = %+v", d)
	}
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 0 {
		t.Fatalf("pending = %v", got)
	}
	u, err := PossiblySubmitted(ctx, w.env)
	if err != nil || len(u) != 1 || u[0].MailName != "IcyKnoll" {
		t.Fatalf("uncertain = %+v, %v", u, err)
	}
}

// A nudge that herdr took gets a receipt, as a send's target does
// (nhm-qaa): the binding, its generation and the baseline read before the
// prompt. A refused or possibly submitted nudge gets none, and nor does
// one without herdr's sequence. Prune removes the receipt with its
// operation.
func TestASubmittedNudgeHasAReceipt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.env.Inode = func() uint64 { return 77 }
	withSeq := func(pane string, seq uint64) {
		a := w.h.agents[pane]
		a.StateChangeSeq = &seq
		w.h.agents[pane] = a
	}
	icy := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusDone)
	withSeq("w1:p1", 9)
	w.bind(t, "BlueLake", "w1:p2", "pi", herdr.StatusIdle)
	withSeq("w1:p2", 4)
	w.bind(t, "SageOak", "w1:p3", "pi", herdr.StatusIdle)
	withSeq("w1:p3", 4)
	w.bind(t, "RedFox", "w1:p4", "pi", herdr.StatusIdle)
	w.h.promptErr["w1:p2"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	w.h.promptErr["w1:p3"] = &herdr.Error{Method: "agent.prompt", Code: "timeout"}
	rep, err := MailSent(ctx, w.env, []Sent{sent(1, "GoldLark", "t1", "IcyKnoll", "BlueLake", "SageOak", "RedFox")})
	if err != nil || len(rep.Decisions) != 4 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
	ops := map[string]Decision{}
	for _, d := range rep.Decisions {
		ops[d.MailName] = d
	}
	if ops["IcyKnoll"].Outcome != send.StatusSubmitted || ops["BlueLake"].Outcome != send.StatusRefused ||
		ops["SageOak"].Outcome != send.StatusPossiblySubmitted || ops["RedFox"].Outcome != send.StatusSubmitted {
		t.Fatalf("decisions = %+v", rep.Decisions)
	}
	opID := ops["IcyKnoll"].OperationID
	rs, err := wait.Receipts(ctx, w.env.DB.SQL(), opID)
	if err != nil || len(rs) != 1 {
		t.Fatalf("receipts = %+v, %v", rs, err)
	}
	r := rs[0]
	if r.BindingID != icy.ID || r.Generation != icy.Generation || r.Status != send.StatusSubmitted || r.Baseline == nil ||
		r.Baseline.StateChangeSeq != 9 || r.Baseline.State != herdr.StatusDone || r.Baseline.SocketInode != 77 ||
		r.Baseline.Generation != icy.Generation {
		t.Fatalf("receipt = %+v, baseline %+v", r, r.Baseline)
	}
	// The recent-operations view names the agent from the result.
	var name string
	if err := w.env.DB.SQL().QueryRow(`SELECT json_extract(result_json, '$.mail_name') FROM receipts WHERE operation_id = ?`,
		opID).Scan(&name); err != nil || name != "IcyKnoll" {
		t.Fatalf("result mail_name = %q, %v", name, err)
	}
	for _, n := range []string{"BlueLake", "SageOak", "RedFox"} {
		if _, err := wait.Receipts(ctx, w.env.DB.SQL(), ops[n].OperationID); !errors.Is(err, wait.ErrNoReceipts) {
			t.Errorf("%s (%s): receipts err = %v, want none", n, ops[n].Outcome, err)
		}
	}
	// Past Keep, Prune deletes the operation and its receipt.
	w.now = w.now.Add(w.env.Config.Keep + time.Hour)
	if _, err := Prune(ctx, w.env); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := w.env.DB.SQL().QueryRow(`SELECT COUNT(*) FROM receipts`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("receipts left = %d, %v", left, err)
	}
	if _, err := w.env.DB.Operation(ctx, opID); err == nil {
		t.Fatal("the nudge's operation was not pruned")
	}
}

func TestRefusedStaysPendingAndIsRetried(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	w.h.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	rep, err := MailSent(ctx, w.env, []Sent{sent(1, "SageOak", "t1", "IcyKnoll")})
	if err != nil || rep.Decisions[0].Outcome != send.StatusRefused {
		t.Fatalf("decision = %+v, %v", rep, err)
	}
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 1 {
		t.Fatalf("pending = %v", got)
	}
	// A refused attempt does not start the cooldown.
	delete(w.h.promptErr, "w1:p1")
	w.env.Mail = nil
	rep, err = Pass(ctx, w.env, nil, TriggerMaintain)
	if err != nil || rep.Decisions[0].Outcome != send.StatusSubmitted {
		t.Fatalf("retry = %+v, %v", rep, err)
	}
}

// A "!" that a person left in the input box would make the nudge a shell
// command (nhm-mowp): wake skips the recipient before its claim, with the
// reason, and the mail stays pending for the next pass.
func TestADraftInTheInputBoxHoldsTheNudge(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "codex", herdr.StatusIdle)
	w.h.screens = map[string]string{"w1:p1": "›  !\n\n  mock-model xhigh · ~/proj\n                Shell mode\n"}
	rep, err := MailSent(ctx, w.env, []Sent{sent(1, "SageOak", "t1", "IcyKnoll")})
	if err != nil {
		t.Fatal(err)
	}
	if d := rep.Decisions[0]; d.Action != ActionSkipped || d.OperationID != "" ||
		!strings.HasPrefix(d.Reason, `shell_draft: the input box shows Codex's shell mode (a "!" draft)`) || w.h.promptsTo("w1:p1") != 0 {
		t.Fatalf("decision = %+v, %d prompts", d, w.h.promptsTo("w1:p1"))
	}
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 1 {
		t.Fatalf("pending = %v", got)
	}
	// A box that herdr cannot read holds the nudge too.
	delete(w.h.screens, "w1:p1")
	w.h.readErr = errors.New("read failed")
	w.env.Mail = nil
	rep, err = Pass(ctx, w.env, nil, TriggerMaintain)
	if err != nil || rep.Decisions[0].Action != ActionSkipped ||
		!strings.HasPrefix(rep.Decisions[0].Reason, "screen_unread: nhm could not read the agent's input box (read failed)") {
		t.Fatalf("an unread box = %+v, %v", rep, err)
	}
	w.h.readErr = nil
	rep, err = Pass(ctx, w.env, nil, TriggerMaintain)
	if err != nil || rep.Decisions[0].Outcome != send.StatusSubmitted || w.h.promptsTo("w1:p1") != 1 {
		t.Fatalf("after the draft = %+v, %v", rep, err)
	}
}

func TestClaimInFlightBlocksASecondPathAndStaleSettles(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	if _, err := Record(ctx, w.env, b, []Message{{ID: 1, Sender: "SageOak"}}); err != nil {
		t.Fatal(err)
	}
	opID, claimed, h, err := claim(ctx, w.env, b, TriggerSettle, w.now)
	if err != nil || h != nil || len(claimed) != 1 || opID == "" {
		t.Fatalf("claim = %s %v %+v %v", opID, claimed, h, err)
	}
	// New mail arrives while the first attempt is in flight.
	if _, err := Record(ctx, w.env, b, []Message{{ID: 2, Sender: "SageOak"}}); err != nil {
		t.Fatal(err)
	}
	d, err := Nudge(ctx, w.env, b, TriggerMaintain)
	if err != nil || d.Action != ActionSkipped || !strings.Contains(d.Reason, "in flight") || len(w.h.prompts) != 0 {
		t.Fatalf("second path = %+v, %v", d, err)
	}
	// The first process died: after InFlight it counts as possibly submitted.
	w.now = w.now.Add(31 * time.Second)
	n, err := ResolveStale(ctx, w.env)
	if err != nil || n != 1 {
		t.Fatalf("stale = %d, %v", n, err)
	}
	op, err := w.env.DB.Operation(ctx, opID)
	if err != nil || op.Status != send.StatusPossiblySubmitted || op.CompletedAt == nil {
		t.Fatalf("op = %+v, %v", op, err)
	}
	// It started the cooldown; message 2 waits for it.
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 1 || got[0] != "2" {
		t.Fatalf("pending = %v", got)
	}
	d, _ = Nudge(ctx, w.env, b, TriggerMaintain)
	if d.Action != ActionSkipped || !strings.HasPrefix(d.Reason, "cooldown") {
		t.Fatalf("after stale = %+v", d)
	}
}

// A hook killed between its claim and its outcome, with no maintenance
// runner: the next path settles the attempt itself, and the recipient is
// nudged again for new mail only.
func TestAKilledAttemptDoesNotHoldTheRecipient(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	if _, err := Record(ctx, w.env, b, []Message{{ID: 1, Sender: "SageOak"}}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, _, err := claim(ctx, w.env, b, TriggerMailSent, w.now); err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	// The hard stop: no prompt outcome, no finish.
	w.now = w.now.Add(w.env.Config.InFlight + time.Second)
	rep, err := MailSent(ctx, w.env, []Sent{sent(2, "SageOak", "", "IcyKnoll")})
	if err != nil {
		t.Fatal(err)
	}
	// The killed attempt possibly went out: it starts the cooldown.
	if d := rep.Decisions[0]; d.Action != ActionSkipped || !strings.HasPrefix(d.Reason, "cooldown") || d.WakeAfter == nil {
		t.Fatalf("decision = %+v", d)
	}
	w.now = *rep.Decisions[0].WakeAfter
	w.env.Mail = nil
	rep, err = Pass(ctx, w.env, nil, TriggerMaintain)
	if err != nil {
		t.Fatal(err)
	}
	// Message 1 is never prompted again; message 2 is.
	if d := rep.Decisions[0]; d.Action != ActionNudged || strings.Join(d.Pending, ",") != "2" || len(w.h.prompts) != 1 {
		t.Fatalf("decision = %+v, prompts %d", d, len(w.h.prompts))
	}
}

// acfs-9hkh.77: Prune never drops the row of mail that is still unread
// for an active agent, however old: the next inbox read would nudge for
// it again. Rows of read mail, and of a binding that is gone, go after
// Keep.
func TestPruneKeepsUnreadMailAnnounced(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	gone := w.bind(t, "BlueLake", "w1:p2", "pi", herdr.StatusIdle)
	old := store.FormatTime(w.now.Add(-8 * 24 * time.Hour))
	for _, r := range []struct{ binding, id, outcome string }{
		{b.ID, "1", "submitted"}, {b.ID, "2", OutcomeRead}, {gone.ID, "3", "submitted"},
	} {
		if _, err := w.env.DB.SQL().Exec(`INSERT INTO wake (binding_id, message_id, first_seen_at, announced_at, last_outcome)
			VALUES (?, ?, ?, ?, ?)`, r.binding, r.id, old, old, r.outcome); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.env.Bindings.Retire(ctx, gone.ID, gone.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	if n, err := Prune(ctx, w.env); err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left []string
	rows, _ := w.env.DB.SQL().Query(`SELECT message_id FROM wake ORDER BY message_id`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	_ = rows.Close()
	if strings.Join(left, ",") != "1" {
		t.Fatalf("left = %v", left)
	}
	// The unread mail is still announced: no second nudge.
	w.mail.inbox["IcyKnoll"] = []InboxMessage{{ID: 1, From: "A", Kind: "to"}}
	if added, _, err := ReadInbox(ctx, w.env, b); err != nil || added != 0 {
		t.Fatalf("re-read added %d, %v", added, err)
	}
}

// A prompt that outlives InFlight: resolveStale settles it first, then
// its own outcome comes in. A refusal makes the mail pending again and
// starts no cooldown; nothing was typed.
func TestFinishAfterAStaleSettleKeepsTheTruth(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	if _, err := Record(ctx, w.env, b, []Message{{ID: 1, Sender: "SageOak"}}); err != nil {
		t.Fatal(err)
	}
	opID, claimed, _, err := claim(ctx, w.env, b, TriggerMailSent, w.now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	w.now = w.now.Add(w.env.Config.InFlight + time.Second)
	if n, err := ResolveStale(ctx, w.env); err != nil || n != 1 {
		t.Fatalf("stale = %d, %v", n, err)
	}
	if err := finish(ctx, w.env, opID, b.ID, claimed, send.StatusRefused, nil, w.now); err != nil {
		t.Fatal(err)
	}
	if op, _ := w.env.DB.Operation(ctx, opID); op.Status != send.StatusRefused {
		t.Fatalf("op = %s", op.Status)
	}
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 1 || got[0] != "1" {
		t.Fatalf("pending = %v", got)
	}
	if d, _ := Nudge(ctx, w.env, b, TriggerMaintain); d.Action != ActionNudged {
		t.Fatalf("retry = %+v", d)
	}
}

// The other order: the attempt failed, then a stale pass runs. The failed
// operation stays failed and starts no cooldown.
func TestStaleNeverOverwritesAnOutcome(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	if _, err := Record(ctx, w.env, b, []Message{{ID: 1, Sender: "SageOak"}}); err != nil {
		t.Fatal(err)
	}
	opID, claimed, _, err := claim(ctx, w.env, b, TriggerMailSent, w.now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := finish(ctx, w.env, opID, b.ID, claimed, send.StatusFailed, nil, w.now); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(w.env.Config.InFlight + time.Second)
	if _, err := ResolveStale(ctx, w.env); err != nil {
		t.Fatal(err)
	}
	if op, _ := w.env.DB.Operation(ctx, opID); op.Status != send.StatusFailed {
		t.Fatalf("op = %s", op.Status)
	}
	if d, _ := Nudge(ctx, w.env, b, TriggerMaintain); d.Action != ActionNudged {
		t.Fatalf("no false cooldown: %+v", d)
	}
}

// claim checks the cooldown again inside its transaction: a path that
// passed Nudge's first check before another path's nudge finished does
// not prompt.
func TestClaimRechecksTheCooldown(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	if _, err := MailSent(ctx, w.env, []Sent{sent(1, "SageOak", "t1", "IcyKnoll")}); err != nil {
		t.Fatal(err)
	}
	if _, err := Record(ctx, w.env, b, []Message{{ID: 2, Sender: "BlueLake", ThreadID: "t2"}}); err != nil {
		t.Fatal(err)
	}
	opID, claimed, h, err := claim(ctx, w.env, b, TriggerMailSent, w.now.Add(time.Second))
	if err != nil || opID != "" || len(claimed) != 0 || h == nil || !strings.HasPrefix(h.reason, "cooldown") {
		t.Fatalf("claim = %q %v %+v %v", opID, claimed, h, err)
	}
	if got := pendingIDs(t, w, "IcyKnoll"); len(got) != 1 || got[0] != "2" {
		t.Fatalf("pending = %v", got)
	}
}

// Many mail-sent hooks at once for one idle recipient: one nudge.
func TestConcurrentMailSentNudgesOnce(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", "pi", herdr.StatusIdle)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := int64(1); i <= 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := MailSent(ctx, w.env, []Sent{sent(i, "Sender"+strconv.FormatInt(i, 10), "t", "IcyKnoll")}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := w.h.promptsTo("w1:p1"); n != 1 {
		t.Fatalf("prompts = %d, want 1", n)
	}
}

func TestReadInboxRecordsUnseenAndMarksRead(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	b := w.bind(t, "IcyKnoll", "w1:p1", "claude", herdr.StatusWorking)
	if _, err := Record(ctx, w.env, b, []Message{{ID: 1, Sender: "A"}, {ID: 2, Sender: "B"}}); err != nil {
		t.Fatal(err)
	}
	// 1 was read (the Stop hook consumed it); 3 came from the am CLI.
	w.mail.inbox["IcyKnoll"] = []InboxMessage{{ID: 2, From: "B", Kind: "to"}, {ID: 3, From: "C", ThreadID: "t9", Kind: "cc"}}
	added, read, err := ReadInbox(ctx, w.env, b)
	if err != nil || added != 1 || read != 1 {
		t.Fatalf("added %d read %d, %v", added, read, err)
	}
	if got := pendingIDs(t, w, "IcyKnoll"); strings.Join(got, ",") != "2,3" {
		t.Fatalf("pending = %v", got)
	}
	// A listing at the limit may be cut short: nothing is marked read.
	w.env.Config.InboxLimit = 1
	w.mail.inbox["IcyKnoll"] = []InboxMessage{{ID: 3, From: "C", Kind: "cc"}}
	if _, read, err := ReadInbox(ctx, w.env, b); err != nil || read != 0 {
		t.Fatalf("full listing read %d, %v", read, err)
	}
	// An unknown agent says so.
	w.mail.inbox = map[string][]InboxMessage{}
	if _, _, err := ReadInbox(ctx, w.env, b); !errors.Is(err, agentmail.ErrNotRegistered) {
		t.Fatalf("err = %v", err)
	}
}

func TestSettleOnlyForKindsWithoutAStopHook(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "PiAgent", "w1:p2", "pi", herdr.StatusDone)
	w.mail.inbox["IcyKnoll"] = []InboxMessage{{ID: 1, From: "A", Kind: "to"}}
	w.mail.inbox["PiAgent"] = []InboxMessage{{ID: 2, From: "A", Kind: "to"}}
	d, err := Settle(ctx, w.env, "w1:p1", herdr.StatusIdle)
	if err != nil || d.Action != ActionSkipped || !strings.Contains(d.Reason, "Stop hook") {
		t.Fatalf("claude = %+v, %v", d, err)
	}
	d, err = Settle(ctx, w.env, "w1:p2", herdr.StatusWorking)
	if err != nil || d.Action != ActionSkipped {
		t.Fatalf("working = %+v, %v", d, err)
	}
	d, err = Settle(ctx, w.env, "w1:p2", herdr.StatusDone)
	if err != nil || d.Action != ActionNudged || d.Trigger != TriggerSettle || w.h.promptsTo("w1:p2") != 1 {
		t.Fatalf("pi = %+v, %v", d, err)
	}
	if d, _ := Settle(ctx, w.env, "w1:p9", herdr.StatusIdle); d.Reason != "the pane has no binding" {
		t.Fatalf("unbound = %+v", d)
	}
}

func TestParseSent(t *testing.T) {
	sendAnswer := `{"deliveries":[{"project":"/p","payload":{"id":1705,"thread_id":"acfs-9hkh","from":"SageOak",
		"to":["TealHorizon"],"cc":["BlueLake"],"bcc":["Quiet@other"]}}],"count":1}`
	// Claude Code's PostToolUse gives an MCP tool's answer as content blocks.
	blocks, _ := json.Marshal([]any{map[string]any{"type": "text", "text": sendAnswer}})
	got, err := ParseSent(json.RawMessage(`{"project_key":"/p"}`), blocks)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Message.ID != 1705 || got[0].Message.ThreadID != "acfs-9hkh" || got[0].ProjectKey != "/p" ||
		strings.Join(got[0].Recipients, ",") != "TealHorizon,BlueLake,Quiet" {
		t.Fatalf("send = %+v", got)
	}
	// reply_message answers the payload; the project comes from the input.
	reply := `{"id":1710,"thread_id":null,"from":"TealHorizon","to":["SageOak","sageoak"],"reply_to":1705}`
	got, err = ParseSent(json.RawMessage(`{"project_key":"/q","message_id":1705}`), json.RawMessage(reply))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ProjectKey != "/q" || got[0].Message.ThreadID != "" || len(got[0].Recipients) != 1 {
		t.Fatalf("reply = %+v", got)
	}
	if _, err := ParseSent(nil, json.RawMessage(`{"error":"refused"}`)); err == nil {
		t.Fatal("a refusal parsed")
	}
	if SentTool("mcp__mcp-agent-mail__send_message") != "send_message" || SentTool("reply_message") != "reply_message" ||
		SentTool("mcp__mcp-agent-mail__fetch_inbox") != "" {
		t.Fatal("SentTool")
	}
}

func TestInboxListsToAndBccOldestFirstAndMarksRead(t *testing.T) {
	mail := &fakeMail{inbox: map[string][]InboxMessage{"IcyKnoll": {
		{ID: 9, From: "B", ThreadID: "t2", Kind: "to", AckRequired: true},
		{ID: 4, From: "A", ThreadID: "t1", Kind: "bcc"},
		{ID: 5, From: "A", ThreadID: "t1", Kind: "cc"},
		{ID: 6, From: "A", ThreadID: "t2", Kind: "to", AckRequired: true, AckTS: "2026-10-12T05:00:00Z"},
	}}}
	l, err := Inbox(context.Background(), mail, "/p", "IcyKnoll", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, m := range l.Messages {
		ids = append(ids, m.ID)
	}
	if len(ids) != 3 || ids[0] != 4 || ids[2] != 9 || l.CCUnread != 1 || l.Marked != 3 || len(l.AckPending) != 1 || l.AckPending[0] != 9 {
		t.Fatalf("listing = %+v", l)
	}
	if th := l.Threads(); len(th) != 2 || th[0][0].ThreadID != "t1" || len(th[1]) != 2 {
		t.Fatalf("threads = %+v", th)
	}
	if strings.Join(l.MarkedIDs(), ",") != "4,6,9" {
		t.Fatalf("marked = %v", l.MarkedIDs())
	}
	mail.calls = nil
	l, err = Inbox(context.Background(), mail, "/p", "IcyKnoll", 50, true)
	if err != nil || l.Marked != 0 || len(l.MarkedIDs()) != 0 || len(mail.calls) != 1 {
		t.Fatalf("keep unread = %+v, %v, calls %v", l, err, mail.calls)
	}
	if _, err := Inbox(context.Background(), mail, "/p", "IcyKnoll", 4, false); err == nil {
		t.Fatal("a listing at the limit was accepted")
	}
}
