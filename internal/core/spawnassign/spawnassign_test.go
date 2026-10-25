package spawnassign

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

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/worksource"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

func u64(v uint64) *uint64 { return &v }

// fakeHerdr holds workspace w1's agents by pane (core/send's test fake).
type fakeHerdr struct {
	mu        sync.Mutex
	agents    map[string]herdr.AgentInfo
	promptErr map[string]error
	prompts   map[string][]string
}

func (f *fakeHerdr) add(pane, kind string, st herdr.AgentStatus) {
	a := herdr.AgentInfo{PaneID: pane, WorkspaceID: "w1", Agent: kind, AgentStatus: st, StateChangeSeq: u64(10)}
	if st == herdr.StatusIdle || st == herdr.StatusDone {
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
	if err := f.promptErr[p.Target]; err != nil {
		return herdr.AgentInfo{}, err
	}
	f.prompts[p.Target] = append(f.prompts[p.Target], p.Text)
	a := f.agents[p.Target]
	a.AgentStatus = herdr.StatusWorking
	*a.StateChangeSeq++
	a.CompletionSeq = nil
	f.agents[p.Target] = a
	return a, nil
}

// AgentRead shows an empty screen: no draft in the input box.
func (f *fakeHerdr) AgentRead(_ context.Context, p herdr.AgentReadParams) (herdr.PaneRead, error) {
	return herdr.PaneRead{PaneID: p.Target, Source: p.Source}, nil
}

func (f *fakeHerdr) settle(pane string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[pane]
	a.AgentStatus = herdr.StatusIdle
	a.CompletionSeq = a.StateChangeSeq
	f.agents[pane] = a
}

func (f *fakeHerdr) promptCount(pane string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts[pane])
}

// fakeTracker is the Beads side: guarded claims, details and status.
type fakeTracker struct {
	mu       sync.Mutex
	owner    map[string]string
	claims   int
	statuses map[string]string
	// labels and description are every bead's.
	labels      []string
	description string
}

func (f *fakeTracker) claim(_ context.Context, _, beadID, actor string, _ []string) (bv.BeadClaimResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	if owner := f.owner[beadID]; owner != "" && owner != actor {
		return bv.BeadClaimResult{}, fmt.Errorf("%w: issue %s already assigned to %s", bv.ErrBeadAlreadyClaimed, beadID, owner)
	}
	f.owner[beadID] = actor
	return bv.BeadClaimResult{ID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
}

func (f *fakeTracker) details(_ context.Context, _, beadID string) (*bv.BeadAssignmentDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := "open"
	if f.owner[beadID] != "" {
		status = "in_progress"
	}
	return &bv.BeadAssignmentDetails{ID: beadID, Title: "Task " + beadID, IssueType: "task", Status: status, Assignee: f.owner[beadID],
		Labels: f.labels, Description: f.description}, nil
}

func (f *fakeTracker) status(context.Context, string, string) (string, error) { return "open", nil }

func (f *fakeTracker) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

type world struct {
	t       *testing.T
	herdr   *fakeHerdr
	tracker *fakeTracker
	env     send.Env
	db      *store.DB
	agents  []robot.SpawnedAgent
	binds   []bindings.Binding
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
	w := &world{t: t, herdr: &fakeHerdr{agents: map[string]herdr.AgentInfo{}, promptErr: map[string]error{}, prompts: map[string][]string{}},
		tracker: &fakeTracker{owner: map[string]string{}}, db: db}
	w.env = send.Env{DB: db, Bindings: bindings.NewStore(db), Herdr: w.herdr, InstanceDir: inst.Dir}
	return w
}

// bind adds an agent: its binding (slot, generation 1) and herdr's agent.
func (w *world) bind(name, tab, pane, kind string, st herdr.AgentStatus) bindings.Binding {
	w.t.Helper()
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: name, MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: tab, PaneID: pane,
			HerdrAlias: strings.ToLower(name), Kind: kind}})
	if err != nil {
		w.t.Fatal(err)
	}
	w.herdr.add(pane, kind, st)
	w.agents = append(w.agents, robot.SpawnedAgent{Pane: b.Slot, PaneID: pane, AgentMailName: name, Type: kind})
	w.binds = append(w.binds, b)
	return b
}

func (w *world) deps() *SpawnAssignmentDependencies {
	topo := Topology{Bindings: w.env.Bindings, Herdr: w.herdr, WorkspaceID: "w1", ProjectKey: "/p"}
	return &SpawnAssignmentDependencies{
		ListOccupants: topo.ListOccupants, ObserveOccupant: topo.ObserveOccupant,
		Dispatch:                         SendPort{Env: w.env, Session: "swarm", WorkspaceID: "w1", ProjectRoot: "/p", ProjectKey: "/p"},
		ClaimBeadWithOperatorGatedLabels: w.tracker.claim, GetBeadDetails: w.tracker.details, GetBeadStatus: w.tracker.status,
	}
}

func (w *world) assign(plan Plan) *SpawnOutput {
	w.t.Helper()
	out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
	Assign(context.Background(), out, plan, "inst-000000000000", "", w.deps())
	return out
}

func planOf(ids ...string) Plan {
	recs := make([]bv.TriageRecommendation, 0, len(ids))
	for i, id := range ids {
		recs = append(recs, bv.TriageRecommendation{ID: id, Title: "Task " + id, Type: "task", Status: "open", Priority: 2, Score: 1 - float64(i)/10})
	}
	return Plan{ProjectDir: "/p", Strategy: "top-n", Triage: &bv.TriageResponse{Triage: bv.TriageData{Recommendations: recs}}}
}

func ledger(t *testing.T) *assignment.AssignmentStore {
	t.Helper()
	s, err := assignment.LoadStoreStrict("inst-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")
}

// Ordinary success: one task per agent, claimed under the stable suffixed
// actor of the agent's Mail identity, and one prompt per agent through the
// send core, as the receipt of operation assign:<key>:1.
func TestAssignClaimsAndDeliversOneTaskPerAgent(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind("BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusDone)
	out := w.assign(planOf("bd-1", "bd-2", "bd-3"))

	if !out.Success || out.ErrorCode != "" || out.Mode != "orchestrator" || out.AssignStrategy != "top-n" || len(out.Assignments) != 2 {
		t.Fatalf("out=%+v", out)
	}
	for i, a := range out.Assignments {
		if !a.Claimed || !a.PromptSent || a.ClaimError != "" || a.PromptError != "" || a.DispatchReceiptID == "" || a.DeliveredAt == "" {
			t.Fatalf("assignment %d = %+v", i, a)
		}
		mail := w.binds[i].MailName
		if !strings.HasPrefix(a.ClaimActor, mail+"/ntm-") || a.Pane != w.binds[i].Slot {
			t.Fatalf("assignment %d actor=%q pane=%q, want %s/ntm-… in %s", i, a.ClaimActor, a.Pane, mail, w.binds[i].Slot)
		}
	}
	for i, pane := range []string{"w1:p1", "w1:p2"} {
		if w.herdr.promptCount(pane) != 1 || !strings.Contains(w.herdr.prompts[pane][0], "Work on bead bd-"+fmt.Sprint(i+1)) {
			t.Fatalf("pane %s prompts=%v", pane, w.herdr.prompts[pane])
		}
	}
	s := ledger(t)
	for _, id := range []string{"bd-1", "bd-2"} {
		row := s.Get(id)
		if row == nil || row.DispatchState != assignment.DispatchSent || row.DispatchAttempts != 1 || !strings.HasPrefix(row.OccupancyKey, "bnd-") ||
			row.DispatchTarget != row.OccupancyKey+"@1" {
			t.Fatalf("ledger row %s = %+v", id, row)
		}
		if _, err := w.db.Operation(context.Background(), OperationID(row.IdempotencyKey, 1)); err != nil {
			t.Fatalf("operation of %s: %v", id, err)
		}
	}
}

// An agent that is not ready is never claimed for; the envelope fails.
func TestAssignBusyAgentIsNotClaimed(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusWorking)
	out := w.assign(planOf("bd-1"))
	if out.Success || out.ErrorCode != ErrorCodeAssignmentFailed || len(out.Assignments) != 1 ||
		!strings.Contains(out.Assignments[0].ClaimError, "not safe to dispatch") || out.Assignments[0].Claimed {
		t.Fatalf("out=%+v", out)
	}
	if w.tracker.claimCount() != 0 || w.herdr.promptCount("w1:p1") != 0 {
		t.Fatalf("claims=%d prompts=%d", w.tracker.claimCount(), w.herdr.promptCount("w1:p1"))
	}
}

// Fewer tasks than agents: the agent without a task is a coverage failure,
// so the successful-envelope partial is ASSIGNMENT_FAILED (ntm's rule).
func TestAssignFewerTasksThanAgentsIsAssignmentFailed(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind("BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	out := w.assign(planOf("bd-1"))
	if out.Success || out.ErrorCode != ErrorCodeAssignmentFailed || len(out.Assignments) != 2 {
		t.Fatalf("out=%+v", out)
	}
	if !out.Assignments[0].Claimed || !out.Assignments[0].PromptSent ||
		out.Assignments[1].ClaimError != "no work assignment was produced for this eligible agent" {
		t.Fatalf("assignments=%+v", out.Assignments)
	}
	if w.herdr.promptCount("w1:p2") != 0 {
		t.Fatal("the agent without a task got a prompt")
	}
}

// An empty verified plan claims and sends nothing; every agent is a
// coverage failure.
func TestAssignEmptyPlanTouchesNothing(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	out := w.assign(planOf())
	if out.Success || out.ErrorCode != ErrorCodeAssignmentFailed || len(out.Assignments) != 1 || out.Assignments[0].Claimed {
		t.Fatalf("out=%+v", out)
	}
	if w.tracker.claimCount() != 0 || w.herdr.promptCount("w1:p1") != 0 {
		t.Fatalf("claims=%d prompts=%d", w.tracker.claimCount(), w.herdr.promptCount("w1:p1"))
	}
}

// A rerun of the same plan replays the sent assignment: no second claim
// write path, no second prompt.
func TestAssignRerunReplaysWithoutResend(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	first := w.assign(planOf("bd-1"))
	if !first.Success {
		t.Fatalf("first=%+v", first)
	}
	w.herdr.settle("w1:p1")
	second := w.assign(planOf("bd-1"))
	if !second.Success || !second.Assignments[0].PromptSent || second.Assignments[0].DeliveredAt != "" ||
		second.Assignments[0].IdempotencyKey != first.Assignments[0].IdempotencyKey {
		t.Fatalf("second=%+v", second)
	}
	if w.herdr.promptCount("w1:p1") != 1 {
		t.Fatalf("prompts=%d, want 1", w.herdr.promptCount("w1:p1"))
	}
}

// A refusal before anything was typed leaves the assignment pending; the
// next run recovers the same intent as attempt 2, a new send operation.
func TestAssignKnownUnsentRetriesAsANewAttempt(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.herdr.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	first := w.assign(planOf("bd-1"))
	if first.Success || first.Assignments[0].PromptSent || !first.Assignments[0].Claimed {
		t.Fatalf("first=%+v", first)
	}
	row := ledger(t).Get("bd-1")
	if row == nil || row.DispatchState != assignment.DispatchPending || row.DispatchAttempts != 1 {
		t.Fatalf("after refusal row=%+v", row)
	}
	delete(w.herdr.promptErr, "w1:p1")
	second := w.assign(planOf("bd-1"))
	if !second.Success || !second.Assignments[0].PromptSent || second.Assignments[0].IdempotencyKey != first.Assignments[0].IdempotencyKey {
		t.Fatalf("second=%+v", second)
	}
	row = ledger(t).Get("bd-1")
	if row.DispatchAttempts != 2 || w.herdr.promptCount("w1:p1") != 1 {
		t.Fatalf("row=%+v prompts=%d", row, w.herdr.promptCount("w1:p1"))
	}
	if _, err := w.db.Operation(context.Background(), OperationID(row.IdempotencyKey, 2)); err != nil {
		t.Fatalf("attempt 2 operation: %v", err)
	}
}

// fakeCASS answers every search with its hits and counts the searches.
type fakeCASS struct {
	mu       sync.Mutex
	hits     []cass.Hit
	searches []cass.Search
}

func (f *fakeCASS) Search(_ context.Context, s cass.Search) ([]cass.Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches = append(f.searches, s)
	return f.hits, nil
}

func (f *fakeCASS) IndexSession(context.Context, string, string, string) (string, error) {
	return cass.Indexed, nil
}

func (f *fakeCASS) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.searches)
}

// enriching gives the world CASS history and its deps --with-cass's port.
func (w *world) enriching(t *testing.T) (*fakeCASS, *SpawnAssignmentDependencies) {
	t.Helper()
	cs := &fakeCASS{hits: []cass.Hit{{SourcePath: "/sessions/old.jsonl", Agent: "claude",
		Content: "Bucket per API key at the gateway; refill on a ticker."}}}
	w.env.CASS = cs
	w.tracker.labels, w.tracker.description = []string{"gateway"}, "Use a token bucket per API key."
	deps := w.deps()
	port := deps.Dispatch.(SendPort)
	port.Env, port.PreparedDir = w.env, PreparedDir("inst-000000000000")
	deps.Dispatch, deps.Enrich = port, port
	return cs, deps
}

// ntm's TestGetSpawnAssignWorkEnrichesWorkPromptAndReplaysWithoutRequery on
// nhm's enrichment: one CASS search over the bead's title, labels and
// description; the history above the work prompt; the ledger records the
// enriched prompt with its template checksum; a re-run replays it with no
// second delivery and no second search. nhm also records the exposure, with
// its bead, under the delivering operation.
func TestAssignEnrichesWorkPromptAndReplaysWithoutRequery(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	cs, deps := w.enriching(t)
	plan := planOf("bd-ctx")
	plan.Triage.Triage.Recommendations[0].Title = "Rate limiting middleware"
	plan.Context = PromptContextOptions{WithCASS: true}
	run := func() *SpawnOutput {
		out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
		Assign(context.Background(), out, plan, "inst-000000000000", "", deps)
		return out
	}
	out := run()
	got := out.Assignments[0]
	if !out.Success || !got.Claimed || !got.PromptSent || got.CASSInjection == nil || got.CASSInjection.ItemsInjected != 1 {
		t.Fatalf("assignment = %+v (cass_injection %+v), want an enriched delivered prompt", got, got.CASSInjection)
	}
	if w.herdr.promptCount("w1:p1") != 1 {
		t.Fatalf("deliveries = %d, want 1", w.herdr.promptCount("w1:p1"))
	}
	prompt := w.herdr.prompts["w1:p1"][0]
	history, task := strings.Index(prompt, "Bucket per API key"), strings.Index(prompt, "Work on bead bd-ctx")
	if history < 0 || task < 0 || history > task {
		t.Fatalf("delivered prompt history=%d task=%d, want the history above the work prompt:\n%s", history, task, prompt)
	}
	if cs.count() != 1 || !strings.Contains(cs.searches[0].Query, "gateway") || !strings.Contains(cs.searches[0].Query, "bucket") ||
		!strings.Contains(cs.searches[0].Query, "limiting") {
		t.Fatalf("cass searches = %+v, want one over the bead's title, labels and description", cs.searches)
	}
	row := ledger(t).Get("bd-ctx")
	if row == nil || row.PromptSent != prompt || row.BaseIntentSHA256 == "" || row.BaseIntentSHA256 == row.IntentSHA256 {
		t.Fatalf("ledger row = %+v, want the enriched prompt recorded with its template checksum", row)
	}
	ex, err := enrich.Exposures(context.Background(), w.db, OperationID(row.IdempotencyKey, 1))
	if err != nil || len(ex) != 1 || ex[0].Source != enrich.SourceCASS || len(ex[0].Items) != 1 || ex[0].Items[0].Status != enrich.Included {
		t.Fatalf("exposure under the delivering operation = %+v %v", ex, err)
	}

	replayed := run()
	if r := replayed.Assignments[0]; !r.PromptSent || r.IdempotencyKey != got.IdempotencyKey || r.CASSInjection != nil {
		t.Fatalf("replay = %+v, want the recorded key %s replayed without enrichment", r, got.IdempotencyKey)
	}
	if w.herdr.promptCount("w1:p1") != 1 || cs.count() != 1 {
		t.Fatalf("replay delivered %d prompts and searched %d times", w.herdr.promptCount("w1:p1"), cs.count())
	}
}

// A refused first attempt keeps its prepared context: the next run sends
// the recorded prompt as attempt 2, searches nothing, and records the
// exposure under attempt 2's operation.
func TestAssignEnrichedRetryKeepsItsPreparedContext(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	cs, deps := w.enriching(t)
	plan := planOf("bd-ctx")
	plan.Context = PromptContextOptions{WithCASS: true}
	w.herdr.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	first := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
	Assign(context.Background(), first, plan, "inst-000000000000", "", deps)
	if first.Success || first.Assignments[0].PromptSent || cs.count() != 1 {
		t.Fatalf("first = %+v, searches %d", first, cs.count())
	}
	delete(w.herdr.promptErr, "w1:p1")
	second := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
	Assign(context.Background(), second, plan, "inst-000000000000", "", deps)
	if !second.Success || !second.Assignments[0].PromptSent || cs.count() != 1 {
		t.Fatalf("second = %+v, searches %d", second, cs.count())
	}
	row := ledger(t).Get("bd-ctx")
	if row.DispatchAttempts != 2 || !strings.Contains(w.herdr.prompts["w1:p1"][0], "Bucket per API key") {
		t.Fatalf("row = %+v, prompts %q", row, w.herdr.prompts["w1:p1"])
	}
	ex, err := enrich.Exposures(context.Background(), w.db, OperationID(row.IdempotencyKey, 2))
	if err != nil || len(ex) != 1 || len(ex[0].Items) != 1 {
		t.Fatalf("attempt 2 exposure = %+v %v", ex, err)
	}
	// Delivered: the operation holds the exposure, and the kept file is gone.
	path, _ := preparedPath(PreparedDir("inst-000000000000"), row.IdempotencyKey)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the prepared context stays after its delivery: %v", err)
	}
}

// A kept context is its prompt's: a key that comes back with another
// prompt finds an error, never the old record (review of 6ab2808).
func TestPreparedContextIsItsPromptsOnly(t *testing.T) {
	dir := t.TempDir()
	ex := enrich.Exposure{BindingID: "bnd-0000000000000001", Generation: 1, BeadID: "bd-1"}
	if err := writePrepared(dir, "k1", assignment.PromptSHA256("ctx\n\nWork on bead bd-1"), ex); err != nil {
		t.Fatal(err)
	}
	if got, err := readPrepared(dir, "k1", "ctx\n\nWork on bead bd-1"); err != nil || got == nil || got.BeadID != "bd-1" {
		t.Fatalf("its prompt: %+v %v", got, err)
	}
	if got, err := readPrepared(dir, "k1", "Work on bead bd-2"); err == nil || got != nil {
		t.Fatalf("another prompt: %+v %v", got, err)
	}
	if got, err := readPrepared(dir, "k2", "anything"); err != nil || got != nil {
		t.Fatalf("no kept context: %+v %v", got, err)
	}
}

// A kept context that cannot be read does not stop the delivery (the
// prompt is fixed in the ledger), and the operation says why it has no
// record (review of 6ab2808).
func TestAssignUnreadablePreparedContextStillDelivers(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	_, deps := w.enriching(t)
	plan := planOf("bd-ctx")
	plan.Context = PromptContextOptions{WithCASS: true}
	w.herdr.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	first := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
	Assign(context.Background(), first, plan, "inst-000000000000", "", deps)
	key := first.Assignments[0].IdempotencyKey
	path, _ := preparedPath(PreparedDir("inst-000000000000"), key)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(w.herdr.promptErr, "w1:p1")
	second := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
	Assign(context.Background(), second, plan, "inst-000000000000", "", deps)
	if !second.Success || !second.Assignments[0].PromptSent {
		t.Fatalf("second = %+v", second)
	}
	op, err := w.db.Operation(context.Background(), OperationID(key, 2))
	if err != nil || !strings.Contains(op.ScopeJSON, "prepared_error") {
		t.Fatalf("attempt 2 operation scope = %q %v", op.ScopeJSON, err)
	}
	if ex, err := enrich.Exposures(context.Background(), w.db, OperationID(key, 2)); err != nil || len(ex) != 0 {
		t.Fatalf("an unreadable context was recorded: %+v %v", ex, err)
	}
	// The intent is delivered, so the unreadable file goes too (nhm-nca).
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unreadable kept context stays after delivery: %v", err)
	}
}

// A failed send proves no actuation only when the store has no operation of
// the attempt: an error before the claim does; an error over an operation
// that exists (here a replay that does not match) does not, and nothing is
// typed.
func TestDispatchNoActuationNeedsNoOperation(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	b := w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	port := w.deps().Dispatch
	key := assignment.AssignmentIdempotencyKey("bd-1")
	req := assignment.DispatchRequest{BeadID: "bd-1", Prompt: "Work on bead bd-1", IdempotencyKey: key, Attempt: 1,
		Target: "bnd-00000000000000ff@1"}
	if _, err := port.Dispatch(context.Background(), req); err == nil || !assignment.IsGuaranteedNoActuation(err) {
		t.Fatalf("no such binding: err=%v, want no actuation", err)
	}
	if _, err := w.db.ClaimOperation(context.Background(), store.ClaimRequest{ID: OperationID(key, 2), Verb: send.Verb,
		Request: map[string]string{"other": "request"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	req.Attempt, req.Target = 2, fmt.Sprintf("%s@%d", b.ID, b.Generation)
	_, err := port.Dispatch(context.Background(), req)
	if err == nil || assignment.IsGuaranteedNoActuation(err) || w.herdr.promptCount("w1:p1") != 0 {
		t.Fatalf("existing operation: err=%v prompts=%d, want unknown and none", err, w.herdr.promptCount("w1:p1"))
	}
}

// The ledger is the instance's: a binding keeps its id and its assignment
// when its pane moves to another workspace, so an assignment run there does
// not give the agent a second task (review of 58c5e27).
func TestAssignMovedBindingKeepsItsExclusion(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	b := w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	if first := w.assign(planOf("bd-1")); !first.Success || !first.Assignments[0].PromptSent {
		t.Fatalf("first=%+v", first)
	}
	ctx := context.Background()
	moved, err := w.env.Bindings.Relocate(ctx, b.ID, b.Generation, bindings.Location{WorkspaceID: "w2", TabID: "w2:t1", PaneID: "w2:p1"},
		bindings.Evidence{Kind: "test", Detail: "moved to w2"})
	if err != nil {
		t.Fatal(err)
	}
	w.herdr.mu.Lock()
	delete(w.herdr.agents, "w1:p1")
	w.herdr.agents["w2:p1"] = herdr.AgentInfo{PaneID: "w2:p1", WorkspaceID: "w2", Agent: "claude", AgentStatus: herdr.StatusIdle,
		StateChangeSeq: u64(10), CompletionSeq: u64(10)}
	w.herdr.mu.Unlock()
	topo := Topology{Bindings: w.env.Bindings, Herdr: w.herdr, WorkspaceID: "w2", ProjectKey: "/p"}
	deps := w.deps()
	deps.ListOccupants, deps.ObserveOccupant = topo.ListOccupants, topo.ObserveOccupant
	deps.Dispatch = SendPort{Env: w.env, Session: "swarm", WorkspaceID: "w2", ProjectRoot: "/p", ProjectKey: "/p"}
	claims := w.tracker.claimCount()
	out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()),
		Agents: []robot.SpawnedAgent{{Pane: moved.Slot, PaneID: "w2:p1", AgentMailName: "IcyKnoll", Type: "claude"}}}
	Assign(ctx, out, planOf("bd-2"), "inst-000000000000", "", deps)
	if out.Success || len(out.Assignments) != 1 || out.Assignments[0].Claimed || out.Assignments[0].PromptSent ||
		!strings.Contains(out.Assignments[0].ClaimError, assignment.ErrTargetOccupied.Error()) {
		t.Fatalf("second=%+v", out)
	}
	if w.tracker.claimCount() != claims || w.herdr.promptCount("w2:p1") != 0 {
		t.Fatalf("claims %d -> %d, prompts in w2 %d", claims, w.tracker.claimCount(), w.herdr.promptCount("w2:p1"))
	}
}

// A ledger that is not an instance's own refuses before anything is read.
func TestAssignRefusesALedgerThatIsNotTheInstances(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	for _, name := range []string{"inst-000000000000.w1", "../x", ""} {
		out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: append([]robot.SpawnedAgent(nil), w.agents...)}
		Assign(context.Background(), out, planOf("bd-1"), name, "", w.deps())
		if out.Success || out.ErrorCode != robot.ErrCodeInternalError || len(out.Assignments) != 1 || w.tracker.claimCount() != 0 {
			t.Fatalf("%q: out=%+v", name, out)
		}
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "relative")
	if _, err := Preflight(context.Background(), "/p", "top-n", Policy{}, nil); err == nil {
		t.Fatal("preflight without a state root passed")
	}
}

// An outcome nobody can know (the reply was lost after the prompt) stays
// sending; no later run sends again.
func TestAssignUnknownOutcomeIsNeverResent(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.herdr.promptErr["w1:p1"] = errors.New("connection reset after write")
	first := w.assign(planOf("bd-1"))
	if first.Success || !strings.Contains(first.Assignments[0].PromptError, assignment.ErrDispatchOutcomeUnknown.Error()) {
		t.Fatalf("first=%+v", first)
	}
	delete(w.herdr.promptErr, "w1:p1")
	second := w.assign(planOf("bd-1"))
	if second.Success || !strings.Contains(second.Assignments[0].PromptError, assignment.ErrDispatchOutcomeUnknown.Error()) {
		t.Fatalf("second=%+v", second)
	}
	row := ledger(t).Get("bd-1")
	if row.DispatchState != assignment.DispatchSending || row.DispatchAttempts != 1 || w.herdr.promptCount("w1:p1") != 0 {
		t.Fatalf("row=%+v prompts=%d", row, w.herdr.promptCount("w1:p1"))
	}
}

// A recorded intent for one occupant never reaches the next: after a
// refusal, the binding's new generation gets no prompt and no second claim.
func TestAssignRecycledOccupantGetsNoOldIntent(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	b := w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.herdr.promptErr["w1:p1"] = &herdr.Error{Method: "agent.prompt", Code: herdr.CodeAgentNotReady}
	if first := w.assign(planOf("bd-1")); first.Success {
		t.Fatalf("first=%+v", first)
	}
	delete(w.herdr.promptErr, "w1:p1")
	if _, err := w.env.Bindings.Occupy(context.Background(), b.ID, b.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
		PaneID: "w1:p1", HerdrAlias: "icyknoll", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	second := w.assign(planOf("bd-1"))
	if second.Success || second.Assignments[0].PromptSent || second.Assignments[0].ClaimError == "" {
		t.Fatalf("second=%+v", second)
	}
	if w.herdr.promptCount("w1:p1") != 0 {
		t.Fatalf("the new occupant got the old intent: %v", w.herdr.prompts["w1:p1"])
	}
}

// ntm's reservation-backed assignment is refused before anything happens.
func TestAssignRefusesReservations(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	out := &SpawnOutput{Agents: w.agents}
	plan := planOf("bd-1")
	got, err := assignWorkToAgentsWithError(context.Background(), out, "/p", "inst-000000000000", "top-n", &plan.Policy,
		true, nil, w.deps(), plan.Triage, "", PromptContextOptions{}, nil)
	if err != nil || len(got) != 1 || got[0].ClaimError != errReservationsNotTaken.Error() || w.tracker.claimCount() != 0 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

// --spawn-prompt prefixes the work prompt in one delivery.
func TestAssignInitialPromptRidesInTheWorkPrompt(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: w.agents}
	Assign(context.Background(), out, planOf("bd-1"), "inst-000000000000", "Read AGENTS.md first.", w.deps())
	if !out.Success || w.herdr.promptCount("w1:p1") != 1 ||
		!strings.HasPrefix(w.herdr.prompts["w1:p1"][0], "Read AGENTS.md first.\n\nWork on bead bd-1") {
		t.Fatalf("out=%+v prompts=%v", out, w.herdr.prompts)
	}
}

func TestPreflightRefusesBadStrategyAndStaleWork(t *testing.T) {
	ctx := context.Background()
	if _, err := Preflight(ctx, t.TempDir(), "fastest", Policy{}, nil); !isCode(err, robot.ErrCodeInvalidFlag) {
		t.Fatalf("strategy error=%v", err)
	}
	stale := &worksource.StaleError{Reason: "export moved"}
	fetch := func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return nil, stale }
	_, err := Preflight(ctx, t.TempDir(), "top-n", Policy{}, fetch)
	var perr *PreflightError
	if !errors.As(err, &perr) || perr.Code != ErrCodeStaleWorkCoordination || perr.WorkSourceMismatch != stale {
		t.Fatalf("stale error=%v", err)
	}
}

// The preflight keeps only assignable work: blocked, gated, non-open and
// duplicate recommendations drop out before any spawn.
func TestPreflightFiltersUnassignableWork(t *testing.T) {
	dir := t.TempDir()
	fetch := func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{
			{ID: "bd-ok", Title: "ok", Type: "task", Status: "open"},
			{ID: "bd-blocked", Title: "blocked", Type: "task", Status: "open", BlockedBy: []string{"bd-x"}},
			{ID: "bd-gated", Title: "gated", Type: "task", Status: "open", Labels: []string{"project-approval"}},
			{ID: "bd-done", Title: "done", Type: "task", Status: "closed"},
			{ID: "bd-ok", Title: "ok again", Type: "task", Status: "open"},
		}, nil
	}
	plan, err := Preflight(context.Background(), dir, "top", Policy{OperatorGatedLabels: []string{"project-approval"}}, fetch)
	if err == nil {
		t.Fatalf("an unknown strategy spelling was accepted: %+v", plan)
	}
	plan, err = Preflight(context.Background(), dir, "top-n", Policy{OperatorGatedLabels: []string{"project-approval"}}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	recs := plan.Triage.Triage.Recommendations
	if len(recs) != 1 || recs[0].ID != "bd-ok" || plan.Strategy != "top-n" {
		t.Fatalf("plan=%+v", recs)
	}
}

func isCode(err error, code string) bool {
	var perr *PreflightError
	return errors.As(err, &perr) && perr.Code == code
}

// The policy: nhm's [assign] plus the project's .ntm/config.toml, by ntm's
// rules (gates extend, the work source tightens); a bad file refuses.
func TestLoadPolicyMergesByNtmRules(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "config.toml")
	project := filepath.Join(root, "proj")
	writeFile(t, global, "[assign]\noperator_gated_labels = [\"Needs-Legal\"]\n[assign.work_source]\nprogram_labels = [\"program:a\"]\n")
	writeFile(t, filepath.Join(project, ".ntm", "config.toml"),
		"[assign]\noperator_gated_labels = [\"project-approval\", \"needs-legal\"]\n[assign.work_source]\nrequire_clean = true\nprogram_labels = [\"program:b\"]\n")
	sub := filepath.Join(project, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(sub, global)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.OperatorGatedLabels, ",") != "project-approval,needs-legal" || !p.WorkSource.RequireClean ||
		strings.Join(p.WorkSource.ProgramLabels, ",") != "program:a" {
		t.Fatalf("policy=%+v", p)
	}
	if _, err := LoadPolicy(sub, filepath.Join(root, "absent.toml")); err != nil {
		t.Fatalf("an absent global config is the defaults: %v", err)
	}
	writeFile(t, filepath.Join(project, ".ntm", "config.toml"), "[assign\n")
	if _, err := LoadPolicy(sub, global); err == nil {
		t.Fatal("a broken project config was accepted")
	}
	// A misspelled key under [assign] refuses, in either file; keys of
	// other sections of nhm's config are not the policy's.
	writeFile(t, filepath.Join(project, ".ntm", "config.toml"), "[assign]\noperator_gate_labels = [\"prod\"]\n")
	if _, err := LoadPolicy(sub, global); err == nil || !strings.Contains(err.Error(), "operator_gate_labels") {
		t.Fatalf("a misspelled project gate: %v", err)
	}
	writeFile(t, filepath.Join(project, ".ntm", "config.toml"), "[assign.work_source]\nrequire_cleen = true\n")
	if _, err := LoadPolicy(sub, global); err == nil {
		t.Fatal("a misspelled work-source key was accepted")
	}
	writeFile(t, filepath.Join(project, ".ntm", "config.toml"), "[assign]\n")
	writeFile(t, global, "[quota]\nadmit_unknown = [\"x\"]\n[assign]\noperator_gated_labels = [\"prod\"]\n")
	if p, err := LoadPolicy(sub, global); err != nil || strings.Join(p.OperatorGatedLabels, ",") != "prod" {
		t.Fatalf("nhm's other sections: %+v %v", p, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
