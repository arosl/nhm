package spawnassign

// nhm's side of the --spawn-assign-work port (nhm-i85, ACFS acfs-f2mc.2):
// the names of ntm's robot package that the ported code calls, the occupant
// identity that replaces ntm's tmux pane, and the ports of the assignment
// loop. The ported files are spawn_ntm.go, bulk_assign_ntm.go and
// assign_ntm.go; see their headers.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/worksource"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// ntm's robot types, nhm's robot package.
type (
	SpawnOutput       = robot.SpawnOutput
	SpawnedAgent      = robot.SpawnedAgent
	SpawnAssignment   = robot.SpawnAssignment
	CASSInjectionInfo = robot.CASSInjectionInfo
	CMInjectionInfo   = robot.CMInjectionInfo
)

// ErrorCodeAssignmentFailed is ntm's error code of a spawn whose work
// assignment did not reach every eligible agent.
const ErrorCodeAssignmentFailed = "ASSIGNMENT_FAILED"

// Occupant is one spawned agent's verified identity: its binding, which is
// the occupancy key (stable across the identity's occupants, as a tmux pane
// id is across restarts in one pane), and the binding's generation, frozen
// into the dispatch target so a recorded intent never reaches a later
// occupant.
type Occupant struct {
	BindingID  string
	Generation int64
	// Slot is the binding's slot "N.M", ntm's pane reference.
	Slot string
	// PaneID is the herdr pane the binding is in.
	PaneID   string
	MailName string
	Kind     string
}

// Target is the occupant's dispatch target: "<binding id>@<generation>".
func (o Occupant) Target() string {
	return o.BindingID + "@" + strconv.FormatInt(o.Generation, 10)
}

// ParseTarget splits a dispatch target into its binding id and generation.
func ParseTarget(target string) (bindingID string, generation int64, err error) {
	id, gen, ok := strings.Cut(strings.TrimSpace(target), "@")
	if !ok || id == "" || gen == "" {
		return "", 0, fmt.Errorf("assignment target %q is not <binding id>@<generation>", target)
	}
	generation, err = strconv.ParseInt(gen, 10, 64)
	if err != nil || generation < 1 {
		return "", 0, fmt.Errorf("assignment target %q has no valid generation", target)
	}
	return id, generation, nil
}

// resolveOccupant finds the one occupant in the agent's slot (ntm's
// tmux.ResolvePaneSelectors with one selector, required to match).
func resolveOccupant(occupants []Occupant, slot string) (Occupant, error) {
	slot = strings.TrimSpace(slot)
	var found []Occupant
	for _, o := range occupants {
		if o.Slot == slot {
			found = append(found, o)
		}
	}
	switch len(found) {
	case 0:
		return Occupant{}, fmt.Errorf("no active binding holds slot %s", slot)
	case 1:
		return found[0], nil
	}
	return Occupant{}, fmt.Errorf("slot %s names %d active bindings", slot, len(found))
}

// Policy is the strictly loaded assignment policy ntm reads from
// config.Assign: the extra operator-gated labels and the work-source policy.
type Policy struct {
	OperatorGatedLabels []string
	WorkSource          worksource.ProjectPolicy
}

// DispatchPort delivers one assignment prompt and preflights it, ntm's
// robotAtomicPaneDispatchPort. nhm's is SendPort (dispatch.go).
type DispatchPort interface {
	assignment.DispatchPort
	assignment.PromptPreflightPort
}

// SpawnAssignmentDependencies are the assignment loop's ports, ntm's, with
// nhm's topology, observation and delivery in place of tmux's: ListOccupants
// for ListPanes, ObserveOccupant for ObserveSession, Dispatch for the tmux
// deliverer. Production fills the Beads and ledger ports
// (spawnAssignmentDeps); the caller supplies the nhm ports.
type SpawnAssignmentDependencies struct {
	FetchTriage                      func(context.Context, string) (*bv.TriageResponse, error)
	AssignmentLedgerExists           func(session string) (bool, error)
	ListOccupants                    func(context.Context, string) ([]Occupant, error)
	LoadStore                        func(session string) (*assignment.AssignmentStore, error)
	ClaimBeadWithOperatorGatedLabels func(context.Context, string, string, string, []string) (bv.BeadClaimResult, error)
	GetBeadStatus                    func(context.Context, string, string) (string, error)
	GetBeadDetails                   func(context.Context, string, string) (*bv.BeadAssignmentDetails, error)
	NewIdempotencyKey                func() (string, error)
	// ObserveOccupant reports whether the occupant is safe to dispatch to
	// now (ntm's SessionObservation.SafeToDispatch); an error is a failed
	// observation.
	ObserveOccupant func(context.Context, Occupant) (bool, error)
	Dispatch        DispatchPort
	// Enrich prepares a fresh work prompt's context (--with-cass,
	// --with-memory); nil prepares none.
	Enrich EnrichmentPort
}

// spawnAssignmentDeps fills the Beads and ledger ports with production and
// keeps every port the caller gave.
func spawnAssignmentDeps(custom *SpawnAssignmentDependencies) SpawnAssignmentDependencies {
	deps := SpawnAssignmentDependencies{
		FetchTriage:                      bv.GetTriageContext,
		AssignmentLedgerExists:           spawnAssignmentLedgerExists,
		LoadStore:                        assignment.LoadStoreStrict,
		ClaimBeadWithOperatorGatedLabels: bv.ClaimBeadForAssignmentWithOperatorGatedLabels,
		GetBeadStatus:                    bv.GetBeadStatusContext,
		GetBeadDetails:                   bv.GetBeadAssignmentDetailsContext,
		NewIdempotencyKey:                assignment.NewAssignmentIdempotencyKey,
	}
	if custom == nil {
		return deps
	}
	if custom.FetchTriage != nil {
		deps.FetchTriage = custom.FetchTriage
	}
	if custom.AssignmentLedgerExists != nil {
		deps.AssignmentLedgerExists = custom.AssignmentLedgerExists
	}
	if custom.LoadStore != nil {
		deps.LoadStore = custom.LoadStore
	}
	if custom.ClaimBeadWithOperatorGatedLabels != nil {
		deps.ClaimBeadWithOperatorGatedLabels = custom.ClaimBeadWithOperatorGatedLabels
	}
	if custom.GetBeadStatus != nil {
		deps.GetBeadStatus = custom.GetBeadStatus
	}
	if custom.GetBeadDetails != nil {
		deps.GetBeadDetails = custom.GetBeadDetails
	}
	if custom.NewIdempotencyKey != nil {
		deps.NewIdempotencyKey = custom.NewIdempotencyKey
	}
	deps.ListOccupants, deps.ObserveOccupant, deps.Dispatch = custom.ListOccupants, custom.ObserveOccupant, custom.Dispatch
	deps.Enrich = custom.Enrich
	return deps
}

// spawnStaggerPacer is ntm's delivery pacer (spawn_pacing.go). nhm paces
// the agents' starts in internal/core/spawn and gives the assignment loop no
// delivery pacer; like ntm's nil pacer, it never waits.
type spawnStaggerPacer struct{}

func (p *spawnStaggerPacer) await(ctx context.Context, _ int) error { return ctx.Err() }

func (p *spawnStaggerPacer) stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// normalizeAgentType is ntm's alias normalization of agent types.
func normalizeAgentType(t string) string {
	return string(agent.AgentType(strings.TrimSpace(t)).Canonical())
}

// errReservationsNotTaken refuses ntm's --spawn-require-reservation: nhm
// does not take ntm's Agent Mail reservation runtime (no caller needs it).
var errReservationsNotTaken = errors.New("nhm does not reserve files for spawn assignments (--spawn-require-reservation is not supported)")
