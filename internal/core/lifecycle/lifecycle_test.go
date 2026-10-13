package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mu       sync.Mutex
	agents   map[string]herdr.AgentInfo
	panes    map[string]bool
	closed   []string
	wsClosed []string
	prompts  []string
	starts   []string
	renamed  []string
	// labels and tokens are the panes' marks (nhm-hae).
	labels    map[string]string
	tokens    map[string]map[string]string
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
		s.Panes = append(s.Panes, herdr.PaneInfo{PaneID: p, WorkspaceID: "w1", TabID: "w1:t1", Label: f.labels[p], Tokens: f.tokens[p]})
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

func (f *fakeHerdr) PaneGet(_ context.Context, pane string) (herdr.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.panes[pane] {
		return herdr.PaneInfo{}, errors.New("pane_not_found: " + pane)
	}
	return herdr.PaneInfo{PaneID: pane, Label: f.labels[pane], Tokens: f.tokens[pane]}, nil
}

func (f *fakeHerdr) PaneRename(_ context.Context, pane, label string) (herdr.PaneInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.labels == nil {
		f.labels = map[string]string{}
	}
	f.labels[pane] = label
	return herdr.PaneInfo{PaneID: pane, Label: label}, nil
}

func (f *fakeHerdr) PaneReportMetadata(_ context.Context, p herdr.ReportMetadataParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokens == nil {
		f.tokens = map[string]map[string]string{}
	}
	f.tokens[p.PaneID] = p.Tokens
	return nil
}

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
	ignored      map[string][]string
	fetchErr     error
	// unknown agents have no bead in the databases searched.
	unknown  map[string]bool
	assigned map[string][]Bead
	// beadErr is br's answer to every bead read (ErrNoBeadDB: no
	// database to search).
	beadErr error
	closed  []string
	fail    error
	removed []string
}

func (f *fakeFacts) Reservations(context.Context, string) ([]Reservation, error) {
	return f.reservations, f.fail
}
func (f *fakeFacts) ReservedEver(_ context.Context, _, agent string) ([]string, error) {
	return f.ever[agent], nil
}
func (f *fakeFacts) DirtyFiles(context.Context, string) ([]string, error) { return f.dirty, nil }
func (f *fakeFacts) Worktrees(context.Context, string) ([]string, error)  { return f.worktrees, nil }
func (f *fakeFacts) Fetch(context.Context, string) error                  { return f.fetchErr }
func (f *fakeFacts) WorktreeState(_ context.Context, wt string) (WorktreeInfo, error) {
	s := f.wtState[wt]
	return WorktreeInfo{Dirty: s[0], Merged: s[1], Ignored: f.ignored[wt]}, nil
}
func (f *fakeFacts) RemoveWorktree(_ context.Context, _, wt string) error {
	f.removed = append(f.removed, wt)
	return nil
}
func (f *fakeFacts) Assigned(_ context.Context, _, agent string) ([]Bead, error) {
	if f.beadErr != nil {
		return nil, f.beadErr
	}
	return f.assigned[agent], nil
}
func (f *fakeFacts) Closed(context.Context, string, string) ([]string, error) { return f.closed, nil }
func (f *fakeFacts) Known(_ context.Context, _, agent string) (bool, error) {
	if f.beadErr != nil {
		return false, f.beadErr
	}
	return !f.unknown[agent], nil
}

type fakeMail struct {
	retired []string
	down    bool
	// current is Agent Mail's token of an identity; one it lacks takes
	// any token.
	current map[string]string
	// gone are the identities Agent Mail has retired already.
	gone  map[string]bool
	calls []string
	// afterCheck runs after an unretire_agent that accepted the token.
	afterCheck func()
}

func (m *fakeMail) Call(_ context.Context, tool string, args, out any) error {
	if m.down {
		return errors.New("connection refused")
	}
	m.calls = append(m.calls, tool)
	a, _ := args.(map[string]any)
	name, _ := a["agent_name"].(string)
	switch tool {
	case "whois":
		if m.gone[name] {
			return json.Unmarshal([]byte(`{"name":"`+name+`","retired_at":"2026-10-12T05:00:00Z"}`), out)
		}
	case "unretire_agent", "retire_agent":
		if cur, ok := m.current[name]; ok && a["registration_token"] != cur {
			return &agentmail.ToolError{Tool: tool, Message: `{"error":{"type":"AUTHENTICATION_REQUIRED","data":{"reason":"token_mismatch"}}}`}
		}
		if tool == "unretire_agent" && m.afterCheck != nil {
			m.afterCheck()
		}
		if tool == "retire_agent" {
			m.retired = append(m.retired, fmt.Sprint(a["agent_name"], ":", a["registration_token"]))
		}
	}
	return nil
}

func (m *fakeMail) called(tool string) int {
	n := 0
	for _, c := range m.calls {
		if c == tool {
			n++
		}
	}
	return n
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

// acfs-9hkh.92: an agent that registered again after nhm kept its token
// has a newer one, and Agent Mail refuses the kept token. Retire checks
// the token first: the pane still closes, the identity stays active, and
// the warning says how to finish.
func TestRetireChecksTheKeptToken(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.mail.current = map[string]string{"IcyKnoll": "tok-newer"}
	res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", DryRun: true})
	if err != nil || len(res.Actions) != 2 || !strings.Contains(strings.Join(res.Warnings, "\n"), "IcyKnoll is stale") {
		t.Fatalf("dry run = %+v %v", res, err)
	}
	res, err = Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll"})
	if err != nil || !res.Done || res.IdentityRetired || len(w.h.closed) != 1 || w.mail.called("retire_agent") != 0 {
		t.Fatalf("retire = %+v %v (retire_agent calls %d)", res, err, w.mail.called("retire_agent"))
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "call retire_agent itself before you retire it") {
		t.Fatalf("warnings = %q", res.Warnings)
	}
	if got, _ := w.env.Bindings.Get(ctx, b.ID); got.Status != bindings.Retired {
		t.Fatalf("binding = %s", got.Status)
	}

	// The agent retired its identity itself first: retire sends no
	// unretire_agent (it would bring the identity back) and no
	// retire_agent, and drops the token.
	w = newWorld(t)
	b = w.bind(t, "FoxRun", "w1:p2", herdr.StatusIdle)
	w.mail.gone = map[string]bool{"FoxRun": true}
	res, err = Retire(ctx, w.env, RetireRequest{Target: "FoxRun"})
	if err != nil || !res.Done || !res.IdentityRetired || w.mail.called("unretire_agent") != 0 || w.mail.called("retire_agent") != 0 {
		t.Fatalf("retire = %+v %v, calls %v", res, err, w.mail.calls)
	}
	if _, err := store.ReadSecret(w.env.InstanceDir, b.ID); !errors.Is(err, store.ErrNoSecret) {
		t.Fatalf("the token stays: %v", err)
	}

	// The agent registers again between the check and retire_agent.
	w = newWorld(t)
	w.bind(t, "OwlPeak", "w1:p3", herdr.StatusIdle)
	w.mail.current = map[string]string{"OwlPeak": "tok-OwlPeak"}
	w.mail.afterCheck = func() { w.mail.current["OwlPeak"] = "tok-later" }
	res, err = Retire(ctx, w.env, RetireRequest{Target: "OwlPeak"})
	if err != nil || !res.Done || res.IdentityRetired || !strings.Contains(strings.Join(res.Warnings, "\n"), "OwlPeak is stale") {
		t.Fatalf("retire = %+v %v", res, err)
	}

	// Agent Mail cannot check the token: retire refuses before it closes
	// anything.
	w = newWorld(t)
	w.bind(t, "ElmBay", "w1:p4", herdr.StatusIdle)
	w.env.Mail = failingCheck{w.mail}
	res, err = Retire(ctx, w.env, RetireRequest{Target: "ElmBay"})
	if !errors.Is(err, ErrRefused) || len(w.h.closed) != 0 || !strings.Contains(strings.Join(res.Refusals, "\n"), "cannot check ElmBay's registration token") {
		t.Fatalf("retire = %+v %v", res, err)
	}
}

// failingCheck is Agent Mail up, with whois failing.
type failingCheck struct{ *fakeMail }

func (f failingCheck) Call(ctx context.Context, tool string, args, out any) error {
	if tool == "whois" {
		return errors.New("database is locked")
	}
	return f.fakeMail.Call(ctx, tool, args, out)
}

// A cass run that holds the lock (ACFS's watcher) leaves the session to
// that run: the warning says so instead of an exit status (acfs-9hkh.92).
func TestRetireIndexWarnings(t *testing.T) {
	ctx := context.Background()
	for outcome, want := range map[string]string{
		"queued":        "another cass index run holds cass's lock",
		"not_found_yet": "cass does not list",
		"indexed":       "",
	} {
		w := newWorld(t)
		w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
		w.env.Index = func(context.Context, string, string, string) (string, error) { return outcome, nil }
		res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll"})
		if err != nil || !res.Done {
			t.Fatalf("%s: retire = %+v %v", outcome, res, err)
		}
		var got []string
		for _, wn := range res.Warnings {
			if strings.HasPrefix(wn, "cass index:") {
				got = append(got, wn)
			}
		}
		if want == "" && len(got) != 0 || want != "" && (len(got) != 1 || !strings.Contains(got[0], want)) {
			t.Fatalf("%s: warnings = %q", outcome, got)
		}
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
		{"no bead in any db searched", func(w *world) {
			w.facts.unknown = map[string]bool{"IcyKnoll": true}
		}, "cannot verify the handoff"},
		{"no bead database", func(w *world) { w.facts.beadErr = ErrNoBeadDB }, "br cannot be read"},
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
	// --no-beads: an agent that worked no beads (a reviewer) retires.
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.facts.unknown = map[string]bool{"IcyKnoll": true}
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", NoBeads: true, DryRun: true}); err != nil {
		t.Fatalf("--no-beads = %v", err)
	}
	// It also retires one in a project with no bead database (ru's
	// repos), but a br that fails still refuses.
	w.facts.beadErr = ErrNoBeadDB
	if _, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", NoBeads: true, DryRun: true}); err != nil {
		t.Fatalf("--no-beads without a bead database = %v", err)
	}
	w.facts.beadErr = errors.New("br: database is locked")
	if res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", NoBeads: true, DryRun: true}); !errors.Is(err, ErrRefused) ||
		!strings.Contains(strings.Join(res.Refusals, "|"), "br cannot be read") {
		t.Fatalf("--no-beads with br failing = %v %v", res.Refusals, err)
	}
	w.facts.beadErr = nil
	// --no-beads waives no handoff: a bead in progress still needs one.
	w.facts.assigned["IcyKnoll"] = []Bead{{ID: "acfs-9"}}
	if res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll", NoBeads: true, DryRun: true}); !errors.Is(err, ErrRefused) ||
		!strings.Contains(strings.Join(res.Refusals, "|"), "no handoff comment") {
		t.Fatalf("--no-beads with a bead in progress = %v %v", res.Refusals, err)
	}
	w.facts.assigned["IcyKnoll"] = nil
	// A dirty file another agent holds now is not the retiring agent's.
	w = newWorld(t)
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
	if _, err := Kill(ctx, w.env, "w1", true, false); !errors.Is(err, ErrRefused) || len(w.h.wsClosed) != 0 {
		t.Fatalf("kill with a refusal = %v", err)
	}
	w.facts.assigned["RedStone"] = nil
	// Agents with no bead in any db searched: kill refuses unless
	// --no-beads names them all bead-less.
	w.facts.unknown = map[string]bool{"IcyKnoll": true, "RedStone": true, "GreenPike": true}
	if _, err := Kill(ctx, w.env, "w1", false, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "cannot verify the handoff") {
		t.Fatalf("kill of bead-less agents = %v", err)
	}
	if res, err := Kill(ctx, w.env, "w1", false, true); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--yes") || !res.Agents[0].NoBeads {
		t.Fatalf("kill --no-beads (dry) = %+v %v", res, err)
	}
	w.facts.unknown = nil
	// What retire cannot check refuses too: an agent nhm has no binding
	// for, and a job in a shell's foreground.
	w.h.add("w1:p9", "codex", "", "s9", herdr.StatusIdle)
	if _, err := Kill(ctx, w.env, "w1", true, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no binding for") {
		t.Fatalf("kill over an unbound agent = %v", err)
	}
	delete(w.h.agents, "w1:p9")
	w.h.jobs = map[string]bool{"w1:p9": true}
	if _, err := Kill(ctx, w.env, "w1", true, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "job in the foreground") {
		t.Fatalf("kill over a job = %v", err)
	}
	w.h.jobs = nil
	if _, err := Kill(ctx, w.env, "w1", false, false); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("kill without --yes = %v", err)
	}
	res, err := Kill(ctx, w.env, "w1", true, false)
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
	blue := w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	setEffort(t, w, blue.ID, "high")
	if _, err := w.db.SQL().Exec(`UPDATE bindings SET role = 'reviewer' WHERE binding_id = ?`, blue.ID); err != nil {
		t.Fatal(err)
	}
	// An existing directory that is not an empty 0700 one is refused.
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportTo(ctx, w.env, "/p", shared, nil); err == nil || !strings.Contains(err.Error(), "mode 775") {
		t.Fatalf("export into a shared dir = %v", err)
	}
	dir := filepath.Join(t.TempDir(), "export")
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
	if list[0].Effort != "high" || list[0].Role != "reviewer" {
		t.Fatalf("effort = %q, role = %q", list[0].Effort, list[0].Role)
	}
}

func setEffort(t *testing.T, w *world, id, effort string) {
	t.Helper()
	if _, err := w.db.SQL().Exec(`UPDATE bindings SET effort = ? WHERE binding_id = ?`, effort, id); err != nil {
		t.Fatal(err)
	}
}

// An export comes from another host: a bad field refuses the whole
// import, and a planted link is never followed.
func TestImportValidation(t *testing.T) {
	ctx := context.Background()
	good := func() Export {
		return Export{Version: 1, Project: "/p", Keep: []string{},
			Bindings: []ExportedBinding{{BindingID: "bnd-1", MailName: "IcyKnoll", Kind: "claude", Model: "opus", ProjectID: "proj",
				NativeSession: &bindings.NativeSession{Value: "sess-1"}}},
			Transcripts: []ExportTranscript{}}
	}
	write := func(t *testing.T, x Export) string {
		dir := t.TempDir()
		data, _ := json.Marshal(x)
		writeFile(t, filepath.Join(dir, "export.json"), string(data))
		return dir
	}
	for name, mutate := range map[string]func(*Export){
		"session climbs out":    func(x *Export) { x.Bindings[0].NativeSession.Value = "../../../.ssh" },
		"model reads as a flag": func(x *Export) { x.Bindings[0].Model = "--dangerously-skip-permissions" },
		"unknown role":          func(x *Export) { x.Bindings[0].Role = "boss" },
		"relative project":      func(x *Export) { x.Project = "p" },
		"unclean project":       func(x *Export) { x.Project = "/p/../etc" },
		"unknown kind":          func(x *Export) { x.Bindings[0].Kind = "bash" },
		"transcript path": func(x *Export) {
			x.Transcripts = []ExportTranscript{{BindingID: "bnd-1", File: "../x", Reason: "retire"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			x := good()
			mutate(&x)
			w := newWorld(t)
			if _, _, err := ImportFrom(ctx, w.env, write(t, x)); err == nil || !strings.Contains(err.Error(), "nothing imported") {
				t.Fatalf("= %v", err)
			}
			if list, _ := w.env.Bindings.List(ctx, bindings.Filter{}); len(list) != 0 {
				t.Fatalf("imported %d bindings", len(list))
			}
		})
	}
	// A transcript in the export that is a link: not followed, not copied.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, secret, "do not copy\n")
	x := good()
	x.Transcripts = []ExportTranscript{{BindingID: "bnd-1", File: "link.jsonl", Reason: "retire"}}
	dir := write(t, x)
	if err := os.MkdirAll(filepath.Join(dir, "transcripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "transcripts", "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t)
	_, notes, err := ImportFrom(ctx, w.env, dir)
	if err != nil || !strings.Contains(strings.Join(notes, "|"), "link.jsonl") {
		t.Fatalf("import with a linked transcript = %v %v", notes, err)
	}
	var n int
	if err := w.db.SQL().QueryRow(`SELECT COUNT(*) FROM transcripts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transcripts = %d %v", n, err)
	}
	// A transcript over the size limit is not copied (a sparse file).
	x = good()
	x.Transcripts = []ExportTranscript{{BindingID: "bnd-1", File: "big.jsonl", Reason: "retire"}}
	dir = write(t, x)
	if err := os.MkdirAll(filepath.Join(dir, "transcripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(dir, "transcripts", "big.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxImportTranscript + 1); err != nil {
		t.Fatal(err)
	}
	_ = big.Close()
	w = newWorld(t)
	if _, notes, err := ImportFrom(ctx, w.env, dir); err != nil || !strings.Contains(strings.Join(notes, "|"), "over the") {
		t.Fatalf("oversized transcript = %v %v", notes, err)
	}
	// A resume argv from the other host is not kept.
	x = good()
	x.Bindings[0].NativeSession.ResumeArgv = []string{"bash", "-c", "x"}
	w = newWorld(t)
	if _, _, err := ImportFrom(ctx, w.env, write(t, x)); err != nil {
		t.Fatal(err)
	}
	if list, _ := w.env.Bindings.List(ctx, bindings.Filter{}); len(list) != 1 || list[0].NativeSession.ResumeArgv != nil {
		t.Fatalf("imported argv = %+v", list)
	}
}

// Reap retires a bead-less agent only when it was spawned as a reviewer;
// retire records a waived bead check in its operation.
func TestReapReviewerAndNoBeadsAudit(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	rev := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	if _, err := w.db.SQL().Exec(`UPDATE bindings SET role = 'reviewer' WHERE binding_id = ?`, rev.ID); err != nil {
		t.Fatal(err)
	}
	w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	w.facts.unknown = map[string]bool{"IcyKnoll": true, "BlueLake": true}
	none := func(context.Context, string, string) (int, int, error) { return 0, 0, nil }
	req := ReapRequest{Idle: time.Minute, WorkspaceID: "w1"}
	if _, err := Reap(ctx, w.env, none, req); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(2 * time.Minute)
	items, err := Reap(ctx, w.env, none, req)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.MailName] = it.Action + ": " + it.Reason
	}
	if !strings.HasPrefix(got["IcyKnoll"], "retired") || !strings.Contains(got["BlueLake"], "cannot verify the handoff") {
		t.Fatalf("reap = %v", got)
	}
	var request string
	if err := w.db.SQL().QueryRow(`SELECT request_json FROM operations WHERE verb = 'reap'`).Scan(&request); err != nil ||
		!strings.Contains(request, `"no_beads":true`) {
		t.Fatalf("reap's operation = %s %v", request, err)
	}
}

func TestBatchSSH(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	core := func(s string) func() (string, bool) { return func() (string, bool) { return s, true } }
	for _, c := range []struct {
		env  map[string]string
		core string
		want string
	}{
		{nil, "", "ssh -o BatchMode=yes"},
		{map[string]string{"GIT_SSH_COMMAND": "ssh -i ~/.ssh/deploy"}, "ssh -i other", "ssh -i ~/.ssh/deploy -o BatchMode=yes"},
		{nil, "ssh -J jump", "ssh -J jump -o BatchMode=yes"},
		{nil, "/usr/bin/ssh -p 2222", "/usr/bin/ssh -p 2222 -o BatchMode=yes"},
		{map[string]string{"GIT_SSH": "/usr/bin/plink"}, "", ""},
		{map[string]string{"GIT_SSH_COMMAND": "plink -batch"}, "", ""},
		{map[string]string{"GIT_SSH_COMMAND": "ssh -i k # deploy key"}, "", ""},
		{map[string]string{"GIT_SSH_COMMAND": `ssh -i k \`}, "", ""},
	} {
		if got := batchSSH(env(c.env), core(c.core)); got != c.want {
			t.Errorf("%v %q: %q, want %q", c.env, c.core, got, c.want)
		}
	}
	// core.sshCommand that cannot be read: the user's command is kept.
	if got := batchSSH(env(nil), func() (string, bool) { return "", false }); got != "" {
		t.Errorf("unreadable core.sshCommand: %q", got)
	}
}

// InProgress reads each database once, read only, and groups the beads in
// progress by assignee (a fake br on PATH answers and logs its argv).
func TestInProgress(t *testing.T) {
	bin, root := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "beads.db"), "")
	logf := filepath.Join(bin, "argv.log")
	writeFile(t, filepath.Join(bin, "br"), "#!/bin/sh\necho \"$*\" >> "+logf+"\n"+
		`case "$2" in */beads.db) id=own ;; *) id=other ;; esac`+"\n"+
		`echo "{\"issues\":[{\"id\":\"acfs-$id\",\"status\":\"in_progress\",\"assignee\":\"IcyKnoll\"},{\"id\":\"acfs-x\",\"status\":\"in_progress\",\"assignee\":\"\"}],\"has_more\":false}"`+"\n")
	if err := os.Chmod(filepath.Join(bin, "br"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	got, err := CLI{BeadDBs: []string{"/acfs/beads2.db"}}.InProgress(context.Background(), root)
	if err != nil || strings.Join(got["IcyKnoll"], ",") != "acfs-own,acfs-other" || len(got) != 1 {
		t.Fatalf("InProgress = %v %v", got, err)
	}
	argv, _ := os.ReadFile(logf)
	if lines := strings.Split(strings.TrimSpace(string(argv)), "\n"); len(lines) != 2 ||
		!strings.Contains(lines[0], "--no-auto-import --no-auto-flush --allow-stale list --status in_progress") {
		t.Fatalf("br argv = %q", argv)
	}
}

func TestBrReadArgs(t *testing.T) {
	got := strings.Join(brRead([]string{"--db", "/x/beads.db"}), " ")
	if got != "--db /x/beads.db --no-auto-import --no-auto-flush --allow-stale" {
		t.Fatalf("brRead = %q", got)
	}
}

// copyFile never reads through a link, and never writes through one at
// its destination.
func TestCopyFileNoFollow(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "bashrc")
	writeFile(t, victim, "keep me\n")
	src := filepath.Join(dir, "src.jsonl")
	writeFile(t, src, "data\n")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyFile(link, filepath.Join(dir, "out")); err == nil {
		t.Fatal("a linked source was read")
	}
	dst := filepath.Join(dir, "dst.jsonl")
	if err := os.Symlink(victim, dst); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, dst+".tmp"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(victim); string(data) != "keep me\n" {
		t.Fatalf("the link's target was written: %q", data)
	}
	if fi, err := os.Lstat(dst); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("dst = %v %v", fi, err)
	}
}

func TestRetireWorktreeChecks(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ path, alias string }{{"/data/tmp/icyknoll-check.x1", "icyknoll"}, {"/w/icyknoll/wt", "icyknoll"}} {
		if !namesAgent(c.path, c.alias) {
			t.Errorf("%s does not name %s", c.path, c.alias)
		}
	}
	if namesAgent("/data/tmp/icyknollx", "icyknoll") || namesAgent("/data/tmp/xicyknoll-y", "icyknoll") {
		t.Error("a name inside another word matched")
	}
	// A suffixed alias (icyknoll-2) is matched whole, and the mail name
	// still names the agent's worktrees (nhm-4xt).
	for _, c := range []struct {
		path  string
		names []string
		want  bool
	}{
		{"/w/icyknoll-2-wt", []string{"IcyKnoll", "icyknoll-2"}, true},
		{"/data/tmp/icyknoll-check", []string{"IcyKnoll", "icyknoll-2"}, true},
		{"/w/icyknoll-3", []string{"icyknoll-2"}, false},
		{"/w/x-icyknoll-2", []string{"icyknoll-2"}, true},
		{"/w/icyknoll-22", []string{"icyknoll-2"}, false},
	} {
		if got := namesAgent(c.path, c.names...); got != c.want {
			t.Errorf("namesAgent(%s, %v) = %v, want %v", c.path, c.names, got, c.want)
		}
	}
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.facts.worktrees = []string{"/tmp/icyknoll-wt"}
	w.facts.wtState["/tmp/icyknoll-wt"] = [2]bool{false, true}
	w.facts.fetchErr = errors.New("network unreachable")
	if res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll"}); !errors.Is(err, ErrRefused) ||
		!strings.Contains(strings.Join(res.Refusals, "|"), "merged is unknown") {
		t.Fatalf("no fetch = %v %v", res.Refusals, err)
	}
	w.facts.fetchErr = nil
	w.facts.ignored = map[string][]string{"/tmp/icyknoll-wt": {".beads/"}}
	if res, err := Retire(ctx, w.env, RetireRequest{Target: "IcyKnoll"}); !errors.Is(err, ErrRefused) ||
		!strings.Contains(strings.Join(res.Refusals, "|"), ".beads/") {
		t.Fatalf("ignored files = %v %v", res.Refusals, err)
	}

	// An agent whose herdr alias has a suffix (bluelake-2: another
	// project's BlueLake held bluelake) still has its worktrees found, by
	// its mail name and by its alias (nhm-4xt).
	b, err := w.env.Bindings.Create(ctx, bindings.New{MailName: "BlueLake", MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p2",
			HerdrAlias: "bluelake-2", Kind: "claude", Model: "opus", NativeSession: &bindings.NativeSession{Value: "sess-bl"}}})
	if err != nil {
		t.Fatal(err)
	}
	w.h.add("w1:p2", "claude", "bluelake-2", "sess-bl", herdr.StatusIdle)
	if err := store.WriteSecret(w.env.InstanceDir, b.ID, "tok-BlueLake"); err != nil {
		t.Fatal(err)
	}
	w.facts.fetchErr = errors.New("network unreachable")
	for _, wt := range []string{"/tmp/bluelake-wt", "/tmp/bluelake-2-wt"} {
		w.facts.worktrees = []string{wt}
		w.facts.wtState[wt] = [2]bool{false, true}
		if res, err := Retire(ctx, w.env, RetireRequest{Target: "BlueLake"}); !errors.Is(err, ErrRefused) ||
			!strings.Contains(strings.Join(res.Refusals, "|"), "merged is unknown") || !slices.Contains(res.Worktrees, wt) {
			t.Errorf("%s: retire of bluelake-2 = %v %v %v", wt, res.Worktrees, res.Refusals, err)
		}
	}
}

// The effort a binding was launched with goes to its resume and its
// recycle; a resume --run waits for the binding's lock.
func TestEffortAndResumeLock(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusDone)
	setEffort(t, w, b.ID, "high")
	p, err := Resume(ctx, w.env, b.ID, false)
	if err != nil || !strings.Contains(strings.Join(p.Argv, " "), "--effort high") {
		t.Fatalf("resume argv = %v %v", p.Argv, err)
	}
	delete(w.h.agents, "w1:p1")
	unlock, err := send.LockTarget(ctx, w.env.InstanceDir, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(ctx, w.env, b.ID, true); err == nil || !strings.Contains(err.Error(), "being recycled") || len(w.h.starts) != 0 {
		t.Fatalf("resume under the lock = %v, starts %v", err, w.h.starts)
	}
	unlock()
	if _, err := Resume(ctx, w.env, b.ID, true); err != nil || !strings.Contains(w.h.starts[0], "--effort high") {
		t.Fatalf("resume = %v, starts %v", err, w.h.starts)
	}
	if nb, _ := w.env.Bindings.Get(ctx, b.ID); nb.Effort != "high" {
		t.Fatalf("effort after resume = %q", nb.Effort)
	}

	w = newWorld(t)
	b = w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusDone)
	setEffort(t, w, b.ID, "high")
	if _, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"}); err != nil || !strings.Contains(w.h.starts[0], "--effort high") {
		t.Fatalf("recycle = %v, starts %v", err, w.h.starts)
	}
	if nb, _ := w.env.Bindings.Get(ctx, b.ID); nb.Effort != "high" {
		t.Fatalf("effort after recycle = %q", nb.Effort)
	}
}

func TestBeadDBs(t *testing.T) {
	root := t.TempDir()
	if _, err := (CLI{}).beadDBs(root); !errors.Is(err, ErrNoBeadDB) {
		t.Fatalf("no db = %v", err)
	}
	dbs, err := CLI{BeadDBs: []string{"/acfs/.beads/beads.db"}}.beadDBs(root)
	if err != nil || len(dbs) != 1 || strings.Join(dbs[0], " ") != "--db /acfs/.beads/beads.db" {
		t.Fatalf("configured = %v %v", dbs, err)
	}
	writeFile(t, filepath.Join(root, ".beads", "beads.db"), "")
	own := filepath.Join(root, ".beads", "beads.db")
	if dbs, _ = (CLI{BeadDBs: []string{"/x.db"}}).beadDBs(root); len(dbs) != 2 || strings.Join(dbs[0], " ") != "--db "+own {
		t.Fatalf("own and configured = %v", dbs)
	}
	// The config's databases are read only when beads are looked up, and
	// a bad config fails that lookup.
	bad := CLI{LoadBeadDBs: func() ([]string, error) { return nil, errors.New("relative path") }}
	if _, err := bad.beadDBs(root); err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("bad config = %v", err)
	}
	cfg := filepath.Join(root, "config.toml")
	writeFile(t, cfg, "[lifecycle]\nbead_dbs = [\"/acfs/.beads/beads.db\"]\n")
	if list, err := LoadBeadDBs(cfg); err != nil || len(list) != 1 {
		t.Fatalf("LoadBeadDBs = %v %v", list, err)
	}
	writeFile(t, cfg, "[lifecycle]\nbead_dbs = [\"rel.db\"]\n")
	if _, err := LoadBeadDBs(cfg); err == nil {
		t.Fatal("a relative bead db was accepted")
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
