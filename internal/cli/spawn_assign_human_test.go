package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

// spawn --assign (nhm-cvw) runs the robot form's preflight before the
// spawn, in a dry run too, and asks for the yes only after it: a refusal
// is the preflight's, exit 2, and nothing is planned. As in ntm, the
// assignment flags without --assign change nothing.
func TestSpawnAssignPreflightComesFirst(t *testing.T) {
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	base := []string{"spawn", "swarm", "--cc=1", "--project", project, "--socket", s.SocketPath, "--json"}
	for _, extra := range [][]string{
		// No bv on PATH: the actionable plan cannot be verified. Without
		// --yes, and with no terminal, the refusal is still the
		// preflight's: the yes comes after it.
		{"--assign"},
		{"--assign", "--dry-run"},
		{"--assign", "--yes", "--strategy", "quality", "--limit", "2"},
	} {
		code, out, _ := run(append(append([]string(nil), base...), extra...)...)
		env := decodeEnvelope(t, out)
		if code != exitRefused || !strings.Contains(env["error"].(string), "verify actionable spawn work") || env["result"] != nil {
			t.Fatalf("%v: exit %d, %v", extra, code, env)
		}
	}
	code, out, _ := run(append(append([]string(nil), base...), "--dry-run", "--no-kickoff", "--strategy", "speed",
		"--limit", "3", "--assign-cc-only")...)
	// The flags parse and the spawn plans (admission may refuse it on a
	// host without the guards; that comes after the flags).
	env := decodeEnvelope(t, out)
	result, _ := env["result"].(map[string]any)
	if code == exitRefused || result == nil || result["dry_run"] != true {
		t.Fatalf("assignment flags without --assign: exit %d, %v", code, env)
	}
}

// The human form takes the kickoff out of the spawn into the assignment,
// with ntm's flags: the strategy as given, the limit, the init prompt,
// the agent type from --assign-agent or the first alias, and the user's
// CASS and CM choice for the work prompt.
func TestNewHumanAssignTakesTheKickoff(t *testing.T) {
	f := &spawnFlags{strategy: "rr", limit: 3, initPrompt: "Then check mail.", assignCodOnly: true, assignVerbose: true,
		assignTimeout: time.Second, withCASS: true, withMemory: false}
	req := spawn.Request{Session: "swarm", Prompt: "Read AGENTS.md first."}
	a := newHumanAssign(f, &req)
	if !req.NoKickoff || req.Prompt != "" {
		t.Fatalf("the spawn keeps a kickoff: %+v", req)
	}
	// The choices enter the spawn's fingerprint, the kickoff's prompt too.
	if !strings.Contains(req.AssignOptions, `"strategy":"rr"`) || !strings.Contains(req.AssignOptions, "Read AGENTS.md first.") {
		t.Fatalf("assign options = %q", req.AssignOptions)
	}
	if a.strategy != "rr" || a.limit != 3 || a.initPrompt != "Then check mail." || a.agentType != "codex" || !a.verbose ||
		a.kickoff.Prompt != "Read AGENTS.md first." || a.kickoff.NoKickoff || !a.context.WithCASS || a.context.WithMemory {
		t.Fatalf("assign = %+v", a)
	}
}

// An agent whose work prompt went, or may have gone, gets no kickoff: a
// claimed bead skips its slot, unless the ledger says its dispatch surely
// failed (pending again). Without the ledger, every claimed slot is
// skipped; a bead that was never claimed skips nothing.
func TestKickoffSkips(t *testing.T) {
	rows := []robot.SpawnAssignment{
		{Pane: "0.1", BeadID: "nhm-1", Claimed: true, PromptSent: true},
		{Pane: "0.2", BeadID: "nhm-2", Claimed: true, PromptError: "dispatch: refused"},
		{Pane: "0.3", BeadID: "nhm-3", Claimed: true, PromptError: "dispatch outcome unknown"},
		{Pane: "0.4", BeadID: "nhm-4", ClaimError: "claimed by another agent"},
		{Pane: "0.5", ClaimError: "no work item for this agent"},
	}
	ledger := map[string]*assignment.Assignment{
		"nhm-1": {DispatchState: assignment.DispatchSent},
		"nhm-2": {DispatchState: assignment.DispatchPending},
		"nhm-3": {DispatchState: assignment.DispatchSending},
	}
	got := kickoffSkips(rows, func(id string) *assignment.Assignment { return ledger[id] })
	if want := map[string]bool{"0.1": true, "0.3": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("skips = %v, want %v", got, want)
	}
	if got := kickoffSkips(rows, nil); !reflect.DeepEqual(got, map[string]bool{"0.1": true, "0.2": true, "0.3": true}) {
		t.Fatalf("skips without the ledger = %v", got)
	}
}

// The human form's preamble is the kickoff's identity preamble, from the
// occupant's binding: its Mail name and project key, and its herdr name
// (a suffixed one too) and workspace. A binding that cannot be read gets
// none.
func TestIdentityPreambleFromTheBinding(t *testing.T) {
	_, inst := cliInstance(t)
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p",
		ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p1", HerdrAlias: "icyknoll-2", Kind: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	pre := identityPreamble(context.Background(), &instanceEnv{inst: inst, db: db})
	if got, want := pre(spawnassign.Occupant{BindingID: b.ID}), spawn.IdentityPreamble("IcyKnoll", "/p", "icyknoll-2", "w1"); got != want {
		t.Fatalf("preamble = %q, want %q", got, want)
	}
	if got := pre(spawnassign.Occupant{BindingID: "bnd-unknown"}); got != "" {
		t.Fatalf("unknown binding = %q", got)
	}
}

// assignTools installs a fake bv and br for one ready bead, nhm-1, in a
// new project, and leaves PATH with them alone. br's claim marks the bead
// in progress for the actor.
func assignTools(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	writeAll(t, filepath.Join(project, ".beads", "issues.jsonl"),
		`{"id":"nhm-1","title":"Fix the thing","status":"open","issue_type":"task","priority":1}`+"\n")
	claimed := filepath.Join(t.TempDir(), "claimed")
	bvScript := `#!/bin/sh
case "$*" in
  *--robot-plan*) printf '%s\n' '{"plan":{"tracks":[{"track_id":"one","items":[{"id":"nhm-1","title":"Fix the thing","status":"open","priority":1}]}],"summary":{"total_actionable":1}}}' ;;
  *--robot-triage*) printf '%s\n' '{"triage":{"recommendations":[{"id":"nhm-1","title":"Fix the thing","type":"task","status":"open","priority":1,"score":0.9}]}}' ;;
  *--robot-insights*) printf '%s\n' '{"Cycles":[]}' ;;
  *) exit 1 ;;
esac
`
	brScript := `#!/bin/sh
S=` + claimed + `
case " $* " in
  *' ready '*|*' list '*) printf '%s\n' '[{"id":"nhm-1","title":"Fix the thing","status":"open","issue_type":"task","priority":1,"labels":[]}]' ;;
  *' show '*)
    if [ -f "$S" ]; then
      read -r actor < "$S"
      printf '[{"id":"nhm-1","title":"Fix the thing","issue_type":"task","status":"in_progress","priority":1,"assignee":"%s","updated_at":"2026-10-13T00:00:01Z"}]\n' "$actor"
    else
      printf '%s\n' '[{"id":"nhm-1","title":"Fix the thing","issue_type":"task","status":"open","priority":1,"updated_at":"2026-10-13T00:00:00Z"}]'
    fi ;;
  *' update '*)
    actor=""; prev=""
    for a in "$@"; do [ "$prev" = "--actor" ] && actor="$a"; prev="$a"; done
    printf '%s\n' "$actor" > "$S"
    printf '[{"id":"nhm-1","title":"Fix the thing","issue_type":"task","status":"in_progress","priority":1,"assignee":"%s"}]\n' "$actor" ;;
  *) printf '%s\n' '{}' ;;
esac
`
	bin := t.TempDir()
	writeAll(t, filepath.Join(bin, "bv"), bvScript)
	writeAll(t, filepath.Join(bin, "br"), brScript)
	for _, name := range []string{"bv", "br"} {
		if err := os.Chmod(filepath.Join(bin, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return project
}

// spawn --assign end to end (nhm-cvw). When an agent fails to start, the
// agents that are up get the kickoff alone and nothing is assigned, as ntm
// stops at its ready wait. With two agents up and one ready bead, one
// agent gets its identity, the kickoff, --init-prompt and ntm's work
// prompt in one delivery; the other gets the identity, the kickoff and
// --init-prompt; the text is ntm's. A rerun of the operation assigns
// nothing; other choices under its id are refused.
func TestSpawnAssignEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	s, inst, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	spawnAgentMail(t, "GreenPike", "RedFox", "TanOwl", "GoldHill")
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAll(t, filepath.Join(home, ".acfs/onboard/docs/ntm/command_palette.md"),
		"## A\n### default_new_agent | Default\nRead AGENTS.md.\n")
	project := assignTools(t)
	ownNhm(t, inst)

	// The stub cannot split a pane: the second agent of a shared tab fails.
	code, out, errOut := run("spawn", "swarm", "--cc=2", "--project", project, "--allow-unguarded", "--assign", "--yes",
		"--init-prompt", "Then check your mail.", "--socket", s.SocketPath)
	if code != exitError || !strings.Contains(errOut, "assignment failed: ready wait failed: 1 of 2 agents ready") ||
		!strings.Contains(out, "GreenPike (claude, default model) slot 2.1 pane w1:p11 kickoff submitted") {
		t.Fatalf("partial spawn = %d\n%s\n%s", code, out, errOut)
	}
	if sent := h.sent(); len(sent) != 1 || !strings.HasSuffix(sent[0], "Read AGENTS.md.") || !strings.Contains(sent[0], "GreenPike") {
		t.Fatalf("partial spawn prompts = %q", sent)
	}

	args := []string{"spawn", "swarm", "--cc=2", "--per-tab=1", "--project", project, "--allow-unguarded", "--assign", "--yes",
		"--init-prompt", "Then check your mail.", "--op-id", "sp-a1", "--socket", s.SocketPath}
	code, out, errOut = run(args...)
	if code != 0 {
		t.Fatalf("spawn --assign = %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"ℹ 2 agents ready\n", "ℹ Init prompt sent to 2 agents\n", "Executing assignments...\n",
		"  Assigned nhm-1 to pane ", "✓ Successfully assigned 1 beads\n", "ℹ Assigned 1 tasks (strategy: balanced)\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	sent := h.sent()[1:]
	if len(sent) != 2 {
		t.Fatalf("prompts = %q", sent)
	}
	work := "Read AGENTS.md.\n\nThen check your mail.\n\nWork on bead nhm-1: Fix the thing. Check dependencies first with `br dep tree nhm-1`."
	var worked, kicked int
	for _, p := range sent {
		switch {
		case !strings.Contains(p, "project key "+project):
			t.Fatalf("a prompt without the identity: %q", p)
		case strings.Contains(p, work):
			worked++
		case strings.HasSuffix(p, "Read AGENTS.md.\n\nThen check your mail.") && !strings.Contains(p, "nhm-1"):
			kicked++
		default:
			t.Fatalf("prompt = %q", p)
		}
	}
	if worked != 1 || kicked != 1 {
		t.Fatalf("worked %d, kicked %d: %q", worked, kicked, sent)
	}

	// The same operation again: nothing started, nothing assigned.
	code, out, _ = run(args...)
	if code != 0 || !strings.Contains(out, "nothing is assigned again") || len(h.sent()) != 3 {
		t.Fatalf("rerun = %d %s %q", code, out, h.sent())
	}
	// Other choices under the same id are refused.
	if code, _, _ = run(append(append([]string(nil), args...), "--strategy", "speed")...); code != exitRefused {
		t.Fatalf("other choices = %d", code)
	}
}

// ntm's account, in plan order: a claimed and sent bead is assigned; a
// planned bead that is not, that the coordinator refuses, or that the loop
// never reached, is failed, and fails the command; an agent that got no
// bead is neither. The phase's own failure fails the command too.
func TestHumanOutcome(t *testing.T) {
	out := &robot.SpawnOutput{Assignments: []robot.SpawnAssignment{
		{Pane: "0.1", AgentType: "claude", BeadID: "nhm-1", Claimed: true, PromptSent: true},
		{Pane: "0.2", AgentType: "codex", BeadID: "nhm-2", Claimed: true, PromptError: "submission possibly_submitted is not known"},
		{Pane: "0.3", ClaimError: "no work item for this agent"},
	}}
	out.Success, out.ErrorCode, out.Error = false, spawnassign.ErrorCodeAssignmentFailed, "1 agent unassigned"
	sel := spawnassign.HumanSelection{
		Agents:   []robot.SpawnedAgent{{Pane: "0.1", Type: "claude"}, {Pane: "0.2", Type: "codex"}, {Pane: "0.4", Type: "claude"}},
		Items:    []spawnassign.WorkItem{{ID: "nhm-1"}, {ID: "nhm-2"}, {ID: "nhm-4"}},
		Failures: []spawnassign.HumanFailure{{BeadID: "nhm-3", Pane: "0.1", Error: "assignment target already has active work"}},
		Planned:  []string{"nhm-1", "nhm-3", "nhm-2", "nhm-4"},
	}
	o := newHumanOutcome(out, sel)
	want := []humanResult{
		{BeadID: "nhm-1", Pane: "0.1", AgentType: "claude", Assigned: true},
		{BeadID: "nhm-3", Pane: "0.1", AgentType: "", Error: "assignment target already has active work"},
		{BeadID: "nhm-2", Pane: "0.2", AgentType: "codex", Error: "submission possibly_submitted is not known"},
		{BeadID: "nhm-4", Pane: "0.4", AgentType: "claude", Error: "1 agent unassigned"},
	}
	if !reflect.DeepEqual(o.results, want) || o.assigned != 1 || o.failed != 3 || o.phaseErr != "" {
		t.Fatalf("outcome = %+v", o)
	}
	if err := o.err(); err == nil || err.Error() != "assignment failed: failed to execute assignments: 3 of 4 assignments failed" {
		t.Fatalf("err = %v", err)
	}
	ok := newHumanOutcome(&robot.SpawnOutput{Assignments: out.Assignments[:1]},
		spawnassign.HumanSelection{Agents: sel.Agents[:1], Items: sel.Items[:1], Planned: []string{"nhm-1"}})
	if err := ok.err(); err != nil {
		t.Fatalf("all assigned: %v", err)
	}
	lockOut := &robot.SpawnOutput{}
	lockOut.Success, lockOut.ErrorCode, lockOut.Error = false, robot.ErrCodeInternalError, "lock wait"
	if err := newHumanOutcome(lockOut, spawnassign.HumanSelection{}).err(); err == nil || err.Error() != "assignment failed: lock wait" {
		t.Fatalf("phase failure: %v", err)
	}
}

// ntm's text: the ready and init prompt lines always; the executed beads in
// plan order and the totals unless --assign-quiet; the strategy line only
// without a failure. The [DEP] lines go to stderr, by default as ntm's
// text mode is verbose, never with --assign-quiet alone.
func TestHumanPrint(t *testing.T) {
	o := humanOutcome{assigned: 1, failed: 1, results: []humanResult{
		{BeadID: "nhm-1", Pane: "0.1", AgentType: "claude", Assigned: true, CM: &robot.CMInjectionInfo{TokensAdded: 40}},
		{BeadID: "nhm-3", Pane: "0.1", Error: "assignment target already has active work"},
	}}
	sel := spawnassign.HumanSelection{Planned: []string{"nhm-1", "nhm-3"}, Actionable: 3, Skipped: []spawnassign.HumanSkip{
		{BeadID: "nhm-7", Reason: "blocked_by_dependency", BlockedBy: []string{"nhm-6"}},
		{BeadID: "nhm-8", Reason: "already_in_progress"},
		{BeadID: "nhm-9", Reason: spawnassign.HumanSkipCycle},
		{BeadID: "nhm-5", Reason: spawnassign.HumanSkipLimit},
	}}
	var b, e bytes.Buffer
	(&humanAssign{strategy: "balanced", initPrompt: "Then check mail."}).print(&b, &e, 2, 2, o, sel)
	if got, want := b.String(), "ℹ 2 agents ready\nℹ Init prompt sent to 2 agents\n\nExecuting assignments...\n"+
		"  CM rules injected for nhm-1: ~40 tokens\n  Assigned nhm-1 to pane 0.1 (claude)\n  Failed to assign nhm-3 to pane 0.1: assignment target already has active work\n"+
		"\nAssigned 1 beads (1 failed)\n"; got != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", got, want)
	}
	if got, want := e.String(), "[DEP] Skipping nhm-7 - blocked_by_dependency: [nhm-6]\n[DEP] Skipping nhm-8 - already_in_progress\n"+
		"[DEP] Filtered 2 non-actionable beads, 3 actionable\n[DEP] Excluding nhm-9 from assignment (in dependency cycle)\n"; got != want {
		t.Fatalf("stderr:\n%s\nwant:\n%s", got, want)
	}

	b.Reset()
	e.Reset()
	done := humanOutcome{assigned: 1, results: o.results[:1]}
	(&humanAssign{strategy: "speed", quiet: true}).print(&b, &e, 2, 0, done, sel)
	if got, want := b.String(), "ℹ 2 agents ready\nℹ Assigned 2 tasks (strategy: speed)\n"; got != want || e.Len() != 0 {
		t.Fatalf("quiet: stdout %q stderr %q", got, e.String())
	}
	b.Reset()
	(&humanAssign{strategy: "speed", quiet: true, verbose: true}).print(&b, &e, 2, 0, done, sel)
	if !strings.Contains(e.String(), "[DEP] Skipping nhm-8") || strings.Contains(b.String(), "Executing") {
		t.Fatalf("quiet and verbose: stdout %q stderr %q", b.String(), e.String())
	}
}
