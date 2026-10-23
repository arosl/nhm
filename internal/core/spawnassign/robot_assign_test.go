package spawnassign

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/worksource"
	"github.com/arosl/nhm/internal/robot"
)

// The session's bound agents in slot order, as ntm lists a session's
// panes, with ntm's canonical short types (cc, cod, oc); a binding whose
// kind has no ntm type is left out, as ntm leaves out user and unknown
// panes; idle are the ones the delivery gate admits now (nhm-y55).
func TestObserveAssignAgentsOrdersBySlotAndKeepsSafeOnes(t *testing.T) {
	occupants := []Occupant{
		{BindingID: "bnd-0000000000000003", Slot: "1.1", PaneID: "w1:p3", Kind: "codex", Model: "gpt-5"},
		{BindingID: "bnd-0000000000000001", Slot: "0.1", PaneID: "w1:p1", Kind: "claude"},
		{BindingID: "bnd-0000000000000002", Slot: "0.2", PaneID: "w1:p2", Kind: ""},
		{BindingID: "bnd-0000000000000004", Slot: "0.3", PaneID: "w1:p4", Kind: "opencode"},
	}
	var asked string
	deps := RobotAssignDependencies{
		ListOccupants: func(_ context.Context, session string) ([]Occupant, error) {
			asked = session
			return occupants, nil
		},
		ObserveOccupant: func(_ context.Context, o Occupant) (bool, error) { return o.PaneID != "w1:p4", nil },
	}
	agents, idle, err := observeAssignAgents(t.Context(), "swarm", deps)
	if err != nil || asked != "swarm" {
		t.Fatalf("observe = %v (session %q)", err, asked)
	}
	want := []assignAgentInfo{
		{paneID: "w1:p1", paneTarget: "0.1", agentType: "cc", state: "idle", occupancy: "bnd-0000000000000001"},
		{paneID: "w1:p4", paneTarget: "0.3", agentType: "oc", state: "busy", occupancy: "bnd-0000000000000004"},
		{paneID: "w1:p3", paneTarget: "1.1", agentType: "cod", model: "gpt-5", state: "idle", occupancy: "bnd-0000000000000003"},
	}
	if !reflect.DeepEqual(agents, want) || !reflect.DeepEqual(idle, []string{"w1:p1", "w1:p3"}) {
		t.Fatalf("agents = %+v\nidle = %v", agents, idle)
	}

	deps.ObserveOccupant = func(context.Context, Occupant) (bool, error) { return false, errors.New("herdr went away") }
	if _, _, err := observeAssignAgents(t.Context(), "swarm", deps); err == nil || !strings.Contains(err.Error(), "herdr went away") {
		t.Fatalf("failed observation = %v", err)
	}
	if _, _, err := observeAssignAgents(t.Context(), "swarm", RobotAssignDependencies{}); err == nil {
		t.Fatal("no topology was accepted")
	}
}

// The ledger keys occupancy by binding id; the answer lists herdr pane
// ids. An agent whose binding holds an active assignment is not idle. The
// ledger must be the instance's own.
func TestExcludeDurablyOccupiedAssignAgentsKeysByBinding(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	agents := []assignAgentInfo{
		{paneID: "w1:p1", occupancy: "bnd-0000000000000001"},
		{paneID: "w1:p2", occupancy: "bnd-0000000000000002"},
	}
	ledger := "inst-0123456789ab"
	deps := RobotAssignDependencies{LoadStore: func(name string) (*assignment.AssignmentStore, error) {
		if name != ledger {
			t.Fatalf("ledger = %q", name)
		}
		return &assignment.AssignmentStore{Assignments: map[string]*assignment.Assignment{
			"nhm-1": {BeadID: "nhm-1", Status: assignment.StatusAssigned, OccupancyKey: "bnd-0000000000000002"},
			"nhm-2": {BeadID: "nhm-2", Status: assignment.StatusCompleted, OccupancyKey: "bnd-0000000000000001"},
		}}, nil
	}}
	got, err := excludeDurablyOccupiedAssignAgents(ledger, deps, agents, []string{"w1:p1", "w1:p2"})
	if err != nil || !reflect.DeepEqual(got, []string{"w1:p1"}) {
		t.Fatalf("available = %v, %v", got, err)
	}
	if _, err := excludeDurablyOccupiedAssignAgents("swarm", deps, agents, []string{"w1:p1"}); err == nil {
		t.Fatal("a ledger that is not an instance id was read")
	}
}

// GetAssign through its ports: each failed read gives ntm's code, message
// and hint, after the reads before it; the answer keeps what it had.
func TestGetAssignMapsEachFailedRead(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	occupant := Occupant{BindingID: "bnd-0000000000000001", Generation: 1, Slot: "0.1", PaneID: "w1:p1", Kind: "claude"}
	ready := []bv.TriageRecommendation{{ID: "nhm-1", Title: "Fix the crash", Type: "bug", Status: "open", Priority: 1}}
	healthy := func() RobotAssignDependencies {
		return RobotAssignDependencies{
			ListOccupants:   func(context.Context, string) ([]Occupant, error) { return []Occupant{occupant}, nil },
			ObserveOccupant: func(context.Context, Occupant) (bool, error) { return true, nil },
			LoadStore: func(string) (*assignment.AssignmentStore, error) {
				return &assignment.AssignmentStore{Assignments: map[string]*assignment.Assignment{}}, nil
			},
			GetActionable: func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return ready, nil },
			GetInProgress: func(context.Context, string, int) ([]bv.BeadInProgress, error) { return nil, nil },
			GetBlocked: func(context.Context, string, int) ([]bv.BeadPreview, error) {
				return []bv.BeadPreview{{ID: "nhm-3", Title: "Wait"}}, nil
			},
			GetDetails: func(_ context.Context, _, id string) (*bv.BeadAssignmentDetails, error) {
				return &bv.BeadAssignmentDetails{ID: id, BlockedBy: []string{"nhm-1"}}, nil
			},
		}
	}
	opts := AssignOptions{Session: "swarm", ProjectDir: t.TempDir(), Ledger: "inst-0123456789ab"}
	boom := errors.New("boom")
	stale := &worksource.StaleError{Reason: "tracker changed"}
	for _, tc := range []struct {
		name             string
		break_           func(*RobotAssignDependencies)
		code, says, hint string
	}{
		{"observe", func(d *RobotAssignDependencies) {
			d.ObserveOccupant = func(context.Context, Occupant) (bool, error) { return false, boom }
		}, robot.ErrCodeInternalError, "failed to observe assignment candidates", "Retry after pane state can be observed freshly and confidently"},
		{"ledger", func(d *RobotAssignDependencies) {
			d.LoadStore = func(string) (*assignment.AssignmentStore, error) { return nil, boom }
		}, robot.ErrCodeInternalError, "failed to exclude active assignment occupancy", "Repair or migrate the durable assignment ledger before distributing more work"},
		{"actionable", func(d *RobotAssignDependencies) {
			d.GetActionable = func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return nil, boom }
		}, robot.ErrCodeInternalError, "read actionable Beads work", "Ensure bv and br can verify the target project's actionable work and labels"},
		{"stale", func(d *RobotAssignDependencies) {
			d.GetActionable = func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return nil, stale }
		}, ErrCodeStaleWorkCoordination, "tracker changed", staleWorkHint},
		{"in progress", func(d *RobotAssignDependencies) {
			d.GetInProgress = func(context.Context, string, int) ([]bv.BeadInProgress, error) { return nil, boom }
		}, robot.ErrCodeInternalError, "read in-progress Beads work", "Ensure br can read the target project's Beads database"},
		{"blocked", func(d *RobotAssignDependencies) {
			d.GetBlocked = func(context.Context, string, int) ([]bv.BeadPreview, error) { return nil, boom }
		}, robot.ErrCodeInternalError, "read blocked Beads work", "Ensure br can read the target project's Beads database"},
		{"details", func(d *RobotAssignDependencies) {
			d.GetDetails = func(context.Context, string, string) (*bv.BeadAssignmentDetails, error) { return nil, boom }
		}, robot.ErrCodeInternalError, "read blocked bead nhm-3 details", "Ensure br can read blocked-bead dependencies"},
	} {
		deps := healthy()
		tc.break_(&deps)
		out, err := GetAssign(t.Context(), opts, &deps)
		if err != nil || out.Success || out.ErrorCode != tc.code || !strings.Contains(out.Error, tc.says) || out.Hint != tc.hint {
			t.Fatalf("%s: %v %+v", tc.name, err, out.RobotResponse)
		}
		if (tc.name == "stale") != (out.WorkSourceMismatch != nil) {
			t.Fatalf("%s: work_source_mismatch = %+v", tc.name, out.WorkSourceMismatch)
		}
	}
	deps := healthy()
	out, err := GetAssign(t.Context(), opts, &deps)
	if err != nil || !out.Success || len(out.Recommendations) != 1 || out.Recommendations[0].AgentType != "cc" ||
		len(out.BlockedBeads) != 1 || out.Summary.ReadyBeads != 1 {
		t.Fatalf("healthy = %v %+v", err, out)
	}
}

// --robot-assign writes nothing: the production ledger read makes no
// directory and no lock file.
func TestRobotAssignReadsTheLedgerWithoutWriting(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	agents := []assignAgentInfo{{paneID: "w1:p1", occupancy: "bnd-0000000000000001"}}
	got, err := excludeDurablyOccupiedAssignAgents("inst-0123456789ab", robotAssignDeps(nil), agents, []string{"w1:p1"})
	if err != nil || !reflect.DeepEqual(got, []string{"w1:p1"}) {
		t.Fatalf("available = %v, %v", got, err)
	}
	var made []string
	_ = filepath.WalkDir(state, func(path string, _ os.DirEntry, err error) error {
		if path != state {
			made = append(made, path)
		}
		return err
	})
	if len(made) != 0 {
		t.Fatalf("the ledger read wrote %v", made)
	}
}
