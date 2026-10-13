// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/tui/dashboard/panels/spawn_wizard_flow_test.go at commit
// 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: package forms; nhm's kinds and steps: Enter through the
// counts reaches the options, and the options reach the review (the dry
// run). The tests from TestSpawnWizardReviewThenSpawnOnce on are nhm's.

package forms

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/spawn"
)

// wizardCmdTimeout bounds how long a single command may run while a test
// pumps the wizard. Cursor blink ticks sleep for hundreds of milliseconds and
// carry nothing the wizard's step logic depends on, so they are dropped.
const wizardCmdTimeout = 250 * time.Millisecond

// collectWizardMsgs runs cmd the way tea.Program would - expanding batches
// and sequences - and returns the messages it produced.
func collectWizardMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(wizardCmdTimeout):
		return nil
	}
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectWizardMsgs(c)...)
		}
		return out
	}
	// tea.Sequence yields an unexported []tea.Cmd message type.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				out = append(out, collectWizardMsgs(c)...)
			}
		}
		return out
	}
	return []tea.Msg{msg}
}

// pumpWizard delivers every message produced by cmd (and by the commands
// those deliveries return) to the wizard, bounded by depth so a self-renewing
// command cannot spin forever.
func pumpWizard(t *testing.T, sw *SpawnWizard, cmd tea.Cmd, depth int) *SpawnWizard {
	t.Helper()
	if depth > 16 {
		return sw
	}
	for _, msg := range collectWizardMsgs(cmd) {
		if _, ok := msg.(SpawnWizardDoneMsg); ok {
			continue
		}
		model, next := sw.Update(msg)
		sw = model.(*SpawnWizard)
		sw = pumpWizard(t, sw, next, depth+1)
	}
	return sw
}

func pressWizard(t *testing.T, sw *SpawnWizard, key tea.KeyMsg) *SpawnWizard {
	t.Helper()
	model, cmd := sw.Update(key)
	sw = model.(*SpawnWizard)
	return pumpWizard(t, sw, cmd, 0)
}

// The huh form behind each wizard step only completes through its own
// internal messages (next-field, next-group), which arrive as commands rather
// than key presses. Given those messages, choosing Minimal and pressing Enter
// must land on the counts step pre-filled with one Claude agent (#318).
func TestSpawnWizardMinimalEnterAdvancesToCountsWhenFormMessagesDelivered(t *testing.T) {
	t.Parallel()

	sw := newTestWizard(t, "spawn.add", &spawnCore{})
	sw = pumpWizard(t, sw, sw.Init(), 0)

	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyDown})
	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyDown})
	if sw.step != SpawnStepMethod {
		t.Fatalf("navigating options must not leave the method step, got %v", sw.step)
	}

	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})

	if sw.step != SpawnStepCounts {
		t.Fatalf("expected Enter on Minimal to advance to counts step, got %v", sw.step)
	}
	if sw.configMethod != "minimal" {
		t.Fatalf("expected configMethod=minimal, got %q", sw.configMethod)
	}
	if strings.Join(sw.countStrs, ",") != "1,0,0" {
		t.Fatalf("expected minimal pre-fill claude=1 codex=0 agy=0, got %v", sw.countStrs)
	}
	if view := sw.View(); !strings.Contains(view, "claude agents (cc)") {
		t.Fatalf("expected counts form in view, got:\n%s", view)
	}
}

// reachReview fills a minimal add and Enters through the counts and the
// options, which starts the review.
func reachReview(t *testing.T, sw *SpawnWizard) *SpawnWizard {
	t.Helper()
	sw.configMethod = "minimal"
	sw.step = SpawnStepCounts
	sw.initCountsForm()
	sw = pumpWizard(t, sw, sw.countsForm.Init(), 0)

	// claude: count and model; codex: count and model; agy: count.
	for i := 0; i < 5; i++ {
		sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if sw.step != SpawnStepOptions {
		t.Fatalf("expected five Enters to reach the options step, got %v", sw.step)
	}
	// Tab label, group, kickoff, memory; the prompt group is hidden.
	for i := 0; i < 4; i++ {
		sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
	}
	return sw
}

// Enter through each count input and the options reaches the review,
// whose dry run admits the request and offers the confirm.
func TestSpawnWizardEnterThroughCountsReachesConfirm(t *testing.T) {
	t.Parallel()

	sc := &spawnCore{review: spawn.Result{Admission: spawn.Admission{Decision: spawn.Admit},
		Targets: []spawn.Target{{Kind: "claude", Status: "planned"}}}}
	sw := reachReview(t, newTestWizard(t, "spawn.add", sc))
	if sw.step != SpawnStepReview || sw.confirmForm == nil {
		t.Fatalf("expected the review with a confirm, got %v (%v)", sw.step, sw.reviewErr)
	}
	if view := sw.View(); !strings.Contains(view, "Start 1 agent(s)") || !strings.Contains(view, "admission: admit") {
		t.Fatalf("expected confirm summary for one agent, got:\n%s", view)
	}
	if r := sc.asked[0]; !r.DryRun || !r.Add || r.Session != "w1" || len(r.Agents) != 1 || r.Agents[0].Kind != "claude" ||
		!r.Memory || r.CASS || sc.opIDs[0] != "op-1" {
		t.Fatalf("the review asked %+v under %v", r, sc.opIDs)
	}
}

// The confirm starts the reviewed spawn once, under the review's id.
func TestSpawnWizardReviewThenSpawnOnce(t *testing.T) {
	t.Parallel()

	sc := &spawnCore{review: spawn.Result{Admission: spawn.Admission{Decision: spawn.Admit}}}
	sw := reachReview(t, newTestWizard(t, "spawn.add", sc))
	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if sw.step != SpawnStepResult || sw.outcome == nil || sw.outcome.Status != "started" {
		t.Fatalf("expected the spawn's result, got %v %+v %v", sw.step, sw.outcome, sw.runErr)
	}
	if len(sc.asked) != 2 || sc.asked[1].DryRun || sc.opIDs[1] != "op-1" {
		t.Fatalf("the spawn asked %+v under %v", sc.asked, sc.opIDs)
	}
	if sw.submit() != nil || len(sc.asked) != 2 {
		t.Fatal("a second submit ran")
	}
	if view := sw.View(); !strings.Contains(view, "IcyKnoll: ready") {
		t.Fatalf("expected the outcome in view, got:\n%s", view)
	}
}

// A refused admission offers no confirm; going back drops the review.
func TestSpawnWizardRefusedReviewAndBack(t *testing.T) {
	t.Parallel()

	sc := &spawnCore{review: spawn.Result{Admission: spawn.Admission{Decision: spawn.Refuse, Reasons: []string{"claude: quota at 95%"}}},
		err: spawn.ErrRefused}
	sw := reachReview(t, newTestWizard(t, "spawn.add", sc))
	if sw.step != SpawnStepReview || sw.confirmForm != nil || !errors.Is(sw.reviewErr, spawn.ErrRefused) {
		t.Fatalf("expected a refused review, got %v %v", sw.step, sw.reviewErr)
	}
	if view := sw.View(); !strings.Contains(view, "quota at 95%") || !strings.Contains(view, "Nothing can start") {
		t.Fatalf("expected the refusal in view, got:\n%s", view)
	}
	sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyShiftTab})
	if sw.step != SpawnStepOptions || sw.review != nil || sw.reviewKey != "" {
		t.Fatalf("going back kept the review: %v %+v", sw.step, sw.review)
	}
}

// A new session needs a valid label before the review.
func TestSpawnWizardNewSessionNeedsALabel(t *testing.T) {
	t.Parallel()

	sc := &spawnCore{review: spawn.Result{Admission: spawn.Admission{Decision: spawn.Admit}}}
	sw := newTestWizard(t, "spawn.new", sc)
	sw.configMethod = "minimal"
	sw.step = SpawnStepCounts
	sw.initCountsForm()
	sw = pumpWizard(t, sw, sw.countsForm.Init(), 0)
	for i := 0; i < 5; i++ {
		sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
	}
	// Session (empty), tab label, group, kickoff, memory.
	for i := 0; i < 5; i++ {
		sw = pressWizard(t, sw, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if sw.step != SpawnStepOptions || sw.err == nil || len(sc.asked) != 0 {
		t.Fatalf("an empty session reached the review: %v %v", sw.step, sw.err)
	}
	sw.session = "fresh"
	if r := sw.SpawnRequest(); r.Session != "fresh" || r.Add {
		t.Fatalf("request = %+v", r)
	}
}
