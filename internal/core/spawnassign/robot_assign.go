package spawnassign

// nhm's side of --robot-assign (nhm-y55): the ports that ntm's GetAssign
// reads through (assign_ntm.go), the observation of the session's agents
// in place of ntm's tmux observer, and the ledger's occupancy by binding.
// --robot-assign writes nothing: it claims, mails and sends nothing, and
// reads the ledger without a lock file.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// RobotAssignDependencies are --robot-assign's ports: nhm's topology for
// ntm's session observer (ListOccupants, ObserveOccupant: a Topology's),
// and the ledger and Beads reads of ntm's GetAssign. robotAssignDeps fills
// the reads with production; the caller gives the topology.
type RobotAssignDependencies struct {
	ListOccupants   func(context.Context, string) ([]Occupant, error)
	ObserveOccupant func(context.Context, Occupant) (bool, error)
	// LoadStore reads the ledger that the name gives;
	// assignment.LoadStoreStrictReadOnly in production.
	LoadStore     func(name string) (*assignment.AssignmentStore, error)
	GetActionable func(context.Context, string, int) ([]bv.TriageRecommendation, error)
	GetInProgress func(context.Context, string, int) ([]bv.BeadInProgress, error)
	GetBlocked    func(context.Context, string, int) ([]bv.BeadPreview, error)
	GetDetails    func(context.Context, string, string) (*bv.BeadAssignmentDetails, error)
}

// robotAssignDeps fills the reads that custom leaves nil with production.
func robotAssignDeps(custom *RobotAssignDependencies) RobotAssignDependencies {
	var d RobotAssignDependencies
	if custom != nil {
		d = *custom
	}
	if d.LoadStore == nil {
		d.LoadStore = assignment.LoadStoreStrictReadOnly
	}
	if d.GetActionable == nil {
		d.GetActionable = bv.GetActionableRecommendationsContext
	}
	if d.GetInProgress == nil {
		d.GetInProgress = bv.GetInProgressListContext
	}
	if d.GetBlocked == nil {
		d.GetBlocked = bv.GetBlockedListContext
	}
	if d.GetDetails == nil {
		d.GetDetails = bv.GetBeadAssignmentDetailsContext
	}
	return d
}

// ConfigurePolicy registers the project's strict policy for planning, as
// ntm's configureAuthoritativeAssignmentPolicy does before GetAssign: the
// operator-gated labels and the work-source policy.
func ConfigurePolicy(projectDir string, policy Policy) error {
	if err := bv.ConfigureProjectOperatorGatedLabels(projectDir, policy.OperatorGatedLabels); err != nil {
		return fmt.Errorf("register assignment safety policy for %s: %w", projectDir, err)
	}
	if err := bv.ConfigureProjectWorkSourcePolicy(projectDir, policy.WorkSource); err != nil {
		return fmt.Errorf("register work-source policy for %s: %w", projectDir, err)
	}
	return nil
}

// observeAssignAgents is ntm's observeAssignAgents on nhm's topology: the
// session's bound agents in slot order, as ntm lists a session's panes,
// and the herdr pane ids of those that are safe to dispatch to now (the
// send core's delivery gate). The agent type is ntm's canonical short
// form, as ntm's observer gives it. An agent whose kind has no ntm type is
// left out, as ntm leaves out user and unknown panes.
func observeAssignAgents(ctx context.Context, session string, deps RobotAssignDependencies) ([]assignAgentInfo, []string, error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("assignment observation context is required")
	}
	if deps.ListOccupants == nil || deps.ObserveOccupant == nil {
		return nil, nil, fmt.Errorf("observe assignment session %s: no topology", session)
	}
	occupants, err := deps.ListOccupants(ctx, session)
	if err != nil {
		return nil, nil, fmt.Errorf("observe assignment session %s: %w", session, err)
	}
	occupants = append([]Occupant(nil), occupants...)
	sort.SliceStable(occupants, func(i, j int) bool { return slotBefore(occupants[i].Slot, occupants[j].Slot) })
	agents := make([]assignAgentInfo, 0, len(occupants))
	idleAgentPanes := make([]string, 0, len(occupants))
	for _, o := range occupants {
		// ntm's observer gives the canonical short type (cc, cod, agy, oc).
		agentType := strings.ToLower(normalizeAgentType(kinds.Lookup(o.Kind).NTMType))
		if agentType == "user" || agentType == "unknown" || agentType == "" {
			continue
		}
		safe, err := deps.ObserveOccupant(ctx, o)
		if err != nil {
			return nil, nil, fmt.Errorf("observe assignment candidate %s: %w", o.Slot, err)
		}
		state := "busy"
		if safe {
			state = "idle"
			idleAgentPanes = append(idleAgentPanes, o.PaneID)
		}
		agents = append(agents, assignAgentInfo{paneID: o.PaneID, paneTarget: o.Slot, agentType: agentType, model: o.Model,
			state: state, occupancy: o.BindingID})
	}
	return agents, idleAgentPanes, nil
}

// slotBefore orders slots N.M by tab, then pane; a slot that does not
// parse goes last.
func slotBefore(a, b string) bool {
	sa, aerr := bindings.ParseSlot(a)
	sb, berr := bindings.ParseSlot(b)
	if aerr != nil || berr != nil {
		return aerr == nil && berr != nil
	}
	return sa.Tab < sb.Tab || (sa.Tab == sb.Tab && sa.Pane < sb.Pane)
}

// excludeDurablyOccupiedAssignAgents is ntm's on nhm's ledger: one per
// instance (ledger, an instance id), keyed by binding id, read without a
// lock file or a new directory. It drops each idle agent (by herdr pane
// id) whose binding holds an active assignment.
func excludeDurablyOccupiedAssignAgents(ledger string, deps RobotAssignDependencies, agents []assignAgentInfo, idleAgentPanes []string) ([]string, error) {
	if err := ledgerUsable(ledger); err != nil {
		return nil, err
	}
	store, err := deps.LoadStore(ledger)
	if err != nil {
		return nil, err
	}
	occupancy := make(map[string]string, len(agents))
	for _, a := range agents {
		occupancy[a.paneID] = a.occupancy
	}
	keys := make([]string, 0, len(idleAgentPanes))
	for _, paneID := range idleAgentPanes {
		keys = append(keys, occupancy[paneID])
	}
	available, err := filterDurablyOccupiedAssignAgents(keys, store.ListActive())
	if err != nil {
		return nil, err
	}
	free := make(map[string]bool, len(available))
	for _, key := range available {
		free[key] = true
	}
	out := make([]string, 0, len(available))
	for _, paneID := range idleAgentPanes {
		if free[occupancy[paneID]] {
			out = append(out, paneID)
		}
	}
	return out, nil
}

// newRobotResponse and newErrorResponse are ntm's NewRobotResponse and
// NewErrorResponse on nhm's envelope: JSON, stamped now.
func newRobotResponse(success bool) robot.RobotResponse {
	return robot.NewResponse(success, "json", time.Now())
}

func newErrorResponse(err error, code, hint string) robot.RobotResponse {
	r := newRobotResponse(false)
	r.Error, r.ErrorCode, r.Hint = err.Error(), code, hint
	return r
}
