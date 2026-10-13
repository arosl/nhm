package spawnassign

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/robot"
)

// SelectWork replaces the robot strategy (nhm-cvw): the agents are served
// in its order, each of the first ones gets its item, and an agent past
// the items gets none, so the envelope is ASSIGNMENT_FAILED as for any
// agent without work. The item's MatchReason is the assign_reason.
func TestAssignSelectWorkPicksTheWork(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind("BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	deps := w.deps()
	var saw []string
	deps.SelectWork = func(triage *bv.TriageResponse, agents []SpawnedAgent) ([]SpawnedAgent, []WorkItem) {
		for _, a := range agents {
			saw = append(saw, a.Pane)
		}
		// The second agent first, and the bead the robot's top-n would not
		// pick (bd-2 scores below bd-1).
		return []SpawnedAgent{agents[1], agents[0]},
			[]WorkItem{{ID: "bd-2", Title: "Task bd-2", Priority: 2, Score: 0.9, Type: "task", MatchReason: "ntm balanced"}}
	}
	out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: w.agents}
	Assign(context.Background(), out, planOf("bd-1", "bd-2"), "inst-000000000000", "", deps)
	if len(saw) != 2 {
		t.Fatalf("SelectWork saw agents %v", saw)
	}
	if out.Success || out.ErrorCode != ErrorCodeAssignmentFailed {
		t.Fatalf("an agent without work did not fail the envelope: %+v", out)
	}
	if w.herdr.promptCount("w1:p2") != 1 || !strings.Contains(w.herdr.prompts["w1:p2"][0], "Work on bead bd-2") ||
		w.herdr.promptCount("w1:p1") != 0 {
		t.Fatalf("prompts=%v", w.herdr.prompts)
	}
	var got *SpawnAssignment
	for i := range out.Assignments {
		if out.Assignments[i].BeadID == "bd-2" {
			got = &out.Assignments[i]
		}
	}
	if got == nil || !got.Claimed || !got.PromptSent || got.AssignReason != "ntm balanced" {
		t.Fatalf("assignments=%+v", out.Assignments)
	}
}
