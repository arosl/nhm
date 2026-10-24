package cli

// --robot-spawn --spawn-assign-work: ntm's consented one-shot assignment
// (nhm-i85, ACFS acfs-f2mc.2). The flags, the preflight that runs before
// anything is created, and the assignment phase after the agents are up;
// the logic is internal/core/spawnassign. spawn --assign, the human form,
// runs the same preflight and phase after the user's yes (nhm-cvw).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/spawnassign"
	"github.com/arosl/nhm/internal/core/spawnassign/assign"
	"github.com/arosl/nhm/internal/integrations/bv"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// robotAssignFlags are ntm's --spawn-assign-work flags of --robot-spawn.
type robotAssignFlags struct {
	fs                                               *flag.FlagSet
	strategy, legacyStrategy, reservationPaths       *string
	requireReservation, withCASS, noCASS, withMemory *bool
}

func addRobotAssignFlags(fs *flag.FlagSet) robotAssignFlags {
	return robotAssignFlags{fs: fs,
		strategy:       fs.String("strategy", "", ""),
		legacyStrategy: fs.String("spawn-assign-strategy", "top-n", ""),
		// ntm's reservation flags (its root flags, nhm-2p6.4.3): nhm
		// reserves no files for agents, so the preflight refuses them.
		reservationPaths:   fs.String("reservation-paths", "", ""),
		requireReservation: fs.Bool("require-reservation", false, ""),
		withCASS:           fs.Bool("with-cass", false, ""),
		noCASS:             fs.Bool("no-cass", false, ""),
		withMemory:         fs.Bool("with-memory", false, ""),
	}
}

// promptContext is ntm's resolvePromptContextOptions for the work
// prompts: CASS unless --no-cass, when --with-cass or the config asks for
// it; CM when --with-memory or the config asks for it. nhm's [cass]
// context and [memory] cm stand in for ntm's [cass.context] enabled and
// [memory] send_injection, as for --robot-send.
func (f robotAssignFlags) promptContext(cfg enrich.Config) spawnassign.PromptContextOptions {
	return spawnassign.PromptContextOptions{WithCASS: !*f.noCASS && (*f.withCASS || cfg.CASS.Context != enrich.Off),
		WithMemory: *f.withMemory || cfg.Memory.CM != enrich.Off}
}

// strategyValue is ntm's resolveRobotSharedFlag: an explicit
// --spawn-assign-strategy wins, then an explicit --strategy, then
// --spawn-assign-strategy's default.
func (f robotAssignFlags) strategyValue() string {
	set := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	switch {
	case set["spawn-assign-strategy"]:
		return *f.legacyStrategy
	case set["strategy"]:
		return *f.strategy
	}
	return *f.legacyStrategy
}

// options are the robot form's assignment choices for the spawn's
// fingerprint, as spawn --assign's are (humanAssign.options): a rerun of
// the operation with other choices, or without --spawn-assign-work, is
// refused, since a replay may assign (nhm-88n). prompt is --spawn-prompt,
// which goes ahead of every work prompt.
func (f robotAssignFlags) options(prompt string) string {
	opts := map[string]any{"strategy": f.strategyValue(), "prompt": prompt, "with_cass": *f.withCASS, "with_memory": *f.withMemory}
	// Only when set, so an operation recorded before --no-cass keeps its
	// fingerprint.
	if *f.noCASS {
		opts["no_cass"] = true
	}
	b, _ := json.Marshal(opts)
	return string(b)
}

// robotReplayPlan is the plan of a replayed --robot-spawn
// --spawn-assign-work, by spawn --assign's rule (nhm-88n, nhm-vcf). A
// first run that reached none of its agents (replayUntouched finds every
// one) ended between the spawn and the assignment: the verified plan runs,
// as on a first run. Otherwise the plan holds no work, so the phase claims
// and sends nothing and reports only the ledger's complete assignments
// (ntm's durable replay): an agent that the first run did not reach gets
// what ntm's robot spawn gives an agent without work, no prompt. It fills
// res.WorkspaceID, which the occupants need. The caller holds the
// operation's lock.
func robotReplayPlan(ctx context.Context, ie *instanceEnv, res *spawn.Result, plan spawnassign.Plan) (spawnassign.Plan, error) {
	untouched, err := replayUntouched(ctx, ie, res)
	if err != nil {
		return plan, err
	}
	if len(res.Targets) == 0 || len(untouched) < len(res.Targets) {
		plan.Triage = &bv.TriageResponse{}
	}
	return plan, nil
}

// robotAssignPreflight runs ntm's assignment preflight for project before
// the spawn creates anything: no reservations (nhm does not take ntm's
// reservation runtime), nhm's config for the prompt context, the strict
// policy, the verified actionable plan. A refusal is the envelope to
// write, with ntm's code.
func robotAssignPreflight(ctx context.Context, ie *instanceEnv, f robotAssignFlags, project string) (spawnassign.Plan, core.Scope, *robot.RobotResponse) {
	if *f.requireReservation || strings.TrimSpace(*f.reservationPaths) != "" {
		return spawnassign.Plan{}, core.Scope{}, &robot.RobotResponse{Success: false, ErrorCode: robot.ErrCodeInvalidFlag,
			Error: "nhm does not reserve files for spawn assignments (--require-reservation, --reservation-paths)",
			Hint:  "drop the reservation flags; agents reserve their own files"}
	}
	cfg, err := loadEnrichConfig()
	if err != nil {
		return spawnassign.Plan{}, core.Scope{}, &robot.RobotResponse{Success: false, ErrorCode: robot.ErrCodeInvalidFlag,
			Error: err.Error(), Hint: "fix ~/.config/nhm/config.toml"}
	}
	plan, s, refusal := assignPreflight(ctx, ie, f.strategyValue(), project, "give --spawn-dir a project directory", nil)
	if refusal != nil {
		return plan, s, refusal
	}
	plan.Context = f.promptContext(cfg)
	return plan, s, nil
}

// assignPreflight is the preflight that the robot and the human form
// share: the project's scope, the strict policy (nhm's config and the
// project's .ntm/config.toml), and the verified actionable plan for
// strategy. scopeHint is the refusal's hint for a project that has no
// scope; fetch reads the actionable work (nil: all of it). The caller sets
// the plan's prompt context.
func assignPreflight(ctx context.Context, ie *instanceEnv, strategy, project, scopeHint string,
	fetch func(context.Context, string, int) ([]bv.TriageRecommendation, error)) (spawnassign.Plan, core.Scope, *robot.RobotResponse) {
	refuse := func(code, msg, hint string) (spawnassign.Plan, core.Scope, *robot.RobotResponse) {
		return spawnassign.Plan{}, core.Scope{}, &robot.RobotResponse{Success: false, ErrorCode: code, Error: msg, Hint: hint}
	}
	s, err := projectScope(ctx, ie.inst.ID, project)
	if err != nil {
		return refuse(robot.ErrCodeInvalidFlag, err.Error(), scopeHint)
	}
	globalPath, err := kinds.ConfigPath()
	if err != nil {
		return refuse(robot.ErrCodeInvalidFlag, "locate nhm's config for the spawn assignment safety policy: "+err.Error(),
			"Set HOME or XDG_CONFIG_HOME")
	}
	policy, err := spawnassign.LoadPolicy(s.ProjectRoot, globalPath)
	if err != nil {
		return refuse(robot.ErrCodeInvalidFlag, "load spawn assignment safety policy: "+err.Error(),
			"Fix [assign] in nhm's config and the project's .ntm/config.toml")
	}
	plan, err := spawnassign.Preflight(ctx, s.ProjectRoot, strategy, policy, fetch)
	if err != nil {
		var perr *spawnassign.PreflightError
		if errors.As(err, &perr) {
			return refuse(perr.Code, perr.Error(), perr.Hint)
		}
		return refuse(robot.ErrCodeInternalError, err.Error(), "")
	}
	return plan, s, nil
}

// assignHooks are the human form's spawnassign hooks; the robot form sets
// none.
type assignHooks struct {
	// preamble goes ahead of the prompt in each work prompt.
	preamble func(spawnassign.Occupant) string
	// selectWork picks the work in place of the robot strategy.
	selectWork func(*bv.TriageResponse, []robot.SpawnedAgent) ([]robot.SpawnedAgent, []spawnassign.WorkItem)
}

// assignPhase runs the assignment after a spawn whose agents are all up,
// under the owner lock as every send runs, and applies ntm's output rules
// to out. scope is the preflight's project scope; prompt goes ahead of
// each work prompt.
func assignPhase(ctx context.Context, ie *instanceEnv, res spawn.Result, scope core.Scope, plan spawnassign.Plan, prompt string,
	hooks assignHooks, out *robot.SpawnOutput) {
	senv := ie.sendEnv()
	topo := spawnassign.Topology{Bindings: bindings.NewStore(ie.db), Herdr: ie.herdr, WorkspaceID: res.WorkspaceID,
		ProjectKey: scope.MailProjectKey}
	port := spawnassign.SendPort{Env: senv, Session: res.Session, WorkspaceID: res.WorkspaceID,
		ProjectRoot: res.ProjectRoot, ProjectKey: topo.ProjectKey, PreparedDir: spawnassign.PreparedDir(ie.inst.ID)}
	deps := &spawnassign.SpawnAssignmentDependencies{
		ListOccupants: topo.ListOccupants, ObserveOccupant: topo.ObserveOccupant, Dispatch: port, Enrich: port,
		Preamble: hooks.preamble, SelectWork: hooks.selectWork,
	}
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		spawnassign.Assign(ctx, out, plan, ie.inst.ID, prompt, deps)
		return nil
	})
	if err != nil {
		out.Success, out.Error = false, err.Error()
		out.ErrorCode = robot.ErrCodeInternalError
		if errors.Is(err, core.ErrNotOwner) {
			out.ErrorCode = robot.ErrCodeNotOwner
		}
	}
}

// humanAssign is spawn --assign, the human form (nhm-cvw): ntm's spawn
// --assign for the user (its flags, phase order, text, and bead-to-agent
// choices, spawnassign.SelectHuman), on the robot form's preflight, claim,
// ledger and send core. The spawn sends no kickoff then: each agent that
// gets a bead gets its identity, the kickoff and --init-prompt ahead of
// its work prompt, in one delivery, since a working agent takes no second
// prompt; every other agent gets them after the assignment, as its
// kickoff.
type humanAssign struct {
	strategy   string
	limit      int
	initPrompt string
	// agentType is --assign-agent or an --assign-*-only alias, as ntm's
	// long name; "" assigns to every agent.
	agentType      string
	verbose, quiet bool
	// reviewer: --role reviewer, nhm's promise that the agents work no
	// beads (ntm has no such role), so they get the kickoff only.
	reviewer bool
	timeout  time.Duration
	context  spawnassign.PromptContextOptions
	// kickoff is the request as the user gave it, for AssignPrefix.
	kickoff spawn.Request
	// ask asks for the user's yes after the preflight; nil with --yes or
	// in a dry run.
	ask func() bool
}

// humanActionableLimit is ntm's count of actionable beads that spawn
// --assign verifies before the spawn (spawn.go, fetchActionable).
const humanActionableLimit = 100

// spawnAssignResult is spawn --assign's result: the spawn's, with the
// strategy, each planned bead's outcome, and the beads left out.
type spawnAssignResult struct {
	spawn.Result
	AssignStrategy string                  `json:"assign_strategy"`
	Assignments    []robot.SpawnAssignment `json:"assignments"`
	Skipped        []spawnassign.HumanSkip `json:"skipped"`
}

// newHumanAssign takes the kickoff out of req into the assignment: the
// spawn then sends none.
func newHumanAssign(f *spawnFlags, req *spawn.Request) *humanAssign {
	a := &humanAssign{strategy: f.strategy, limit: f.limit, initPrompt: f.initPrompt, verbose: f.assignVerbose, quiet: f.assignQuiet,
		timeout: f.assignTimeout, reviewer: req.Role == spawn.RoleReviewer,
		agentType: spawnassign.ResolveSpawnAssignAgentType(f.assignAgent, f.assignCCOnly, f.assignCodOnly, f.assignGmiOnly, f.assignAgyOnly),
		context:   spawnassign.PromptContextOptions{WithCASS: f.withCASS, WithMemory: f.withMemory}, kickoff: *req}
	req.NoKickoff, req.Prompt, req.AssignOptions = true, "", a.options()
	return a
}

// preflight runs the shared preflight and composes the kickoff (the
// palette prompt or --prompt), before anything is created. The robot
// strategy it names is never used: SelectWork picks.
func (a *humanAssign) preflight(ctx context.Context, ie *instanceEnv, project string) (spawnassign.Plan, core.Scope, string, error) {
	pctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	// ntm's spawn --assign verifies its first 100 actionable beads.
	fetch := func(ctx context.Context, dir string, _ int) ([]bv.TriageRecommendation, error) {
		return bv.GetActionableRecommendationsContext(ctx, dir, humanActionableLimit)
	}
	plan, s, refusal := assignPreflight(pctx, ie, "top-n", project, "give --project a project directory", fetch)
	if refusal != nil {
		msg := "--assign: " + refusal.Error
		if refusal.Hint != "" {
			msg += " (" + refusal.Hint + ")"
		}
		return plan, s, "", errors.New(msg)
	}
	plan.Context, plan.Strategy = a.context, a.strategy
	k := a.kickoff
	k.ProjectRoot = s.ProjectRoot
	kickoff, err := spawn.AssignPrefix(k)
	return plan, s, kickoff, err
}

// prefix is what an agent gets ahead of its work, or as its kickoff: the
// kickoff, then --init-prompt.
func (a *humanAssign) prefix(kickoff string) string {
	if strings.TrimSpace(a.initPrompt) == "" {
		return kickoff
	}
	return strings.TrimSpace(strings.Join([]string{kickoff, a.initPrompt}, "\n\n"))
}

// options are the choices for the spawn's fingerprint: a rerun of the
// operation with other choices is refused.
func (a *humanAssign) options() string {
	b, _ := json.Marshal(map[string]any{"strategy": a.strategy, "limit": a.limit, "init_prompt": a.initPrompt,
		"agent_type": a.agentType, "prompt": a.kickoff.Prompt, "no_kickoff": a.kickoff.NoKickoff,
		"with_cass": a.context.WithCASS, "with_memory": a.context.WithMemory})
	return string(b)
}

// selectWork is the human form's SelectWork: ntm's selection over the
// agents of --assign-agent's type, with the ledger's active beads and
// bv's dependency cycles; the other agents, and every reviewer, get no
// bead. It keeps the selection in sel for the text.
func (a *humanAssign) selectWork(ctx context.Context, ie *instanceEnv, session, project string, sel *spawnassign.HumanSelection) func(*bv.TriageResponse, []robot.SpawnedAgent) ([]robot.SpawnedAgent, []spawnassign.WorkItem) {
	return func(triage *bv.TriageResponse, agents []robot.SpawnedAgent) ([]robot.SpawnedAgent, []spawnassign.WorkItem) {
		var matching, others []robot.SpawnedAgent
		for _, ag := range agents {
			if !a.reviewer && (a.agentType == "" || assign.LegacyAgentType(agent.AgentType(ag.Type)) == a.agentType) {
				matching = append(matching, ag)
			} else {
				others = append(others, ag)
			}
		}
		opts := spawnassign.HumanOptions{Session: session, Strategy: a.strategy, Limit: a.limit, Active: map[string]struct{}{},
			OperatorGated: func(label string) bool { return bv.IsOperatorGatedLabelForProject(project, label) }, Now: time.Now}
		// The ledger is the instance's, so other sessions' active beads
		// count too, where ntm reads only its session's store. Nothing the
		// user sees moves: the new agents have no active assignment, the
		// fairness map has one session, and a bead active elsewhere is in
		// progress in br already.
		if store, err := assignment.LoadStoreStrictReadOnly(ie.inst.ID); err == nil && store != nil {
			for _, item := range store.ListActive() {
				opts.Active[item.BeadID] = struct{}{}
				opts.ActiveTotal++
			}
		}
		// ntm's bv client falls back to insights built from the triage,
		// which carry no cycles, and fails only when the triage fails too;
		// nhm holds the verified triage, so a failed read filters none.
		ictx, cancel := context.WithTimeout(ctx, a.timeout)
		if insights, err := bv.GetInsightsContext(ictx, project); err == nil && insights != nil {
			opts.Cycles = insights.Cycles
		}
		cancel()
		*sel = spawnassign.SelectHuman(triage, matching, opts)
		return append(sel.Agents, others...), sel.Items
	}
}

// humanResult is one planned bead's outcome, ntm's per-item line, and its
// work prompt's enrichment.
type humanResult struct {
	BeadID, Pane, AgentType, Error string
	Assigned                       bool
	CASS                           *robot.CASSInjectionInfo
	CM                             *robot.CMInjectionInfo
}

// humanOutcome is ntm's account of the planned beads, in plan order:
// assigned (claimed and sent), or failed with why. An agent that got no
// bead is neither. phaseErr is the phase's own failure (the owner lock,
// the ledger), apart from the beads.
type humanOutcome struct {
	results          []humanResult
	assigned, failed int
	phaseErr         string
}

func newHumanOutcome(out *robot.SpawnOutput, sel spawnassign.HumanSelection) humanOutcome {
	rows := map[string]robot.SpawnAssignment{}
	for _, a := range out.Assignments {
		if a.BeadID != "" {
			rows[a.BeadID] = a
		}
	}
	refused := map[string]spawnassign.HumanFailure{}
	for _, f := range sel.Failures {
		refused[f.BeadID] = f
	}
	agentOf := map[string]robot.SpawnedAgent{}
	for i, item := range sel.Items {
		if i < len(sel.Agents) {
			agentOf[item.ID] = sel.Agents[i]
		}
	}
	var o humanOutcome
	for _, id := range sel.Planned {
		r := humanResult{BeadID: id, Pane: agentOf[id].Pane, AgentType: agentOf[id].Type}
		if f, ok := refused[id]; ok {
			r.Pane, r.Error = f.Pane, f.Error
		} else if row, ok := rows[id]; ok {
			r.Pane, r.AgentType = firstOf(row.Pane, r.Pane), firstOf(row.AgentType, r.AgentType)
			r.CASS, r.CM = row.CASSInjection, row.MemoryInjection
			r.Assigned = row.Claimed && row.PromptSent
			if !r.Assigned {
				r.Error = firstOf(row.PromptError, row.ClaimError, "not sent")
			}
		} else {
			// The loop stopped before the bead (a timeout, a cancel).
			r.Error = firstOf(out.Error, "not attempted")
		}
		if r.Assigned {
			o.assigned++
		} else {
			o.failed++
		}
		o.results = append(o.results, r)
	}
	if !out.Success && out.ErrorCode != spawnassign.ErrorCodeAssignmentFailed {
		o.phaseErr = firstOf(out.Error, out.ErrorCode)
	}
	return o
}

// err is ntm's: any failed planned bead fails the command, and so does the
// phase's own failure.
func (o humanOutcome) err() error {
	switch {
	case o.failed > 0:
		return fmt.Errorf("assignment failed: failed to execute assignments: %d of %d assignments failed", o.failed, o.assigned+o.failed)
	case o.phaseErr != "":
		return fmt.Errorf("assignment failed: %s", o.phaseErr)
	}
	return nil
}

// print writes ntm's spawn --assign text to w, and its [DEP] lines to errw.
// The ready and init prompt lines always print; the execution lines and
// totals not with --assign-quiet; the [DEP] lines (the beads ntm's
// partition leaves out, and those in a cycle) unless --assign-quiet
// without --assign-verbose, as ntm's text mode is verbose by default. The
// strategy line prints only when no bead failed, as ntm returns before it.
func (a *humanAssign) print(w, errw io.Writer, ready, inited int, o humanOutcome, sel spawnassign.HumanSelection) {
	fmt.Fprintf(w, "ℹ %d agents ready\n", ready)
	if strings.TrimSpace(a.initPrompt) != "" {
		fmt.Fprintf(w, "ℹ Init prompt sent to %d agents\n", inited)
	}
	if a.verbose || !a.quiet {
		filtered := 0
		for _, k := range sel.Skipped {
			switch k.Reason {
			case spawnassign.HumanSkipCycle, spawnassign.HumanSkipLimit, spawnassign.HumanSkipPressure, spawnassign.HumanSkipNoPlaceable:
				continue
			}
			filtered++
			if len(k.BlockedBy) > 0 {
				fmt.Fprintf(errw, "[DEP] Skipping %s - %s: %v\n", k.BeadID, k.Reason, k.BlockedBy)
			} else {
				fmt.Fprintf(errw, "[DEP] Skipping %s - %s\n", k.BeadID, k.Reason)
			}
		}
		if filtered > 0 {
			fmt.Fprintf(errw, "[DEP] Filtered %d non-actionable beads, %d actionable\n", filtered, sel.Actionable)
		}
		for _, k := range sel.Skipped {
			if k.Reason == spawnassign.HumanSkipCycle {
				fmt.Fprintf(errw, "[DEP] Excluding %s from assignment (in dependency cycle)\n", k.BeadID)
			}
		}
	}
	if !a.quiet {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Executing assignments...")
		for _, r := range o.results {
			for _, line := range spawnassign.HumanPromptContextLines(r.BeadID, r.CASS, r.CM) {
				fmt.Fprintln(w, line)
			}
			if r.Assigned {
				fmt.Fprintf(w, "  Assigned %s to pane %s (%s)\n", r.BeadID, r.Pane, r.AgentType)
			} else {
				fmt.Fprintf(w, "  Failed to assign %s to pane %s: %s\n", r.BeadID, r.Pane, r.Error)
			}
		}
		fmt.Fprintln(w)
		if o.failed == 0 {
			fmt.Fprintf(w, "✓ Successfully assigned %d beads\n", o.assigned)
		} else {
			fmt.Fprintf(w, "Assigned %d beads (%d failed)\n", o.assigned, o.failed)
		}
	}
	if o.err() == nil {
		fmt.Fprintf(w, "ℹ Assigned %d tasks (strategy: %s)\n", len(sel.Planned), a.strategy)
	}
}

// kickoffSkips are the slots whose agent gets no kickoff: its work prompt
// went, or may have gone. A claimed bead's slot is skipped unless the
// ledger (get, nil when it cannot be read) says its dispatch surely failed:
// pending again after a dispatch that did not actuate.
func kickoffSkips(rows []robot.SpawnAssignment, get func(beadID string) *assignment.Assignment) map[string]bool {
	skip := map[string]bool{}
	for _, r := range rows {
		if !r.Claimed {
			continue
		}
		if !r.PromptSent && get != nil {
			if a := get(r.BeadID); a != nil && a.DispatchState == assignment.DispatchPending {
				continue
			}
		}
		skip[r.Pane] = true
	}
	return skip
}

// kickoffs sends the kickoff, prefix, to each ready agent of res that is
// not in kickoffSkips of rows (nil: every ready agent), under the owner
// lock as every send runs, in any state of the agent. With only, a
// replay's untouched agents (replayUntouched, nhm-vcf), it sends to those
// alone, and through the delivery gate (spawn.KickoffGated). It returns
// how many were submitted.
func (a *humanAssign) kickoffs(ctx context.Context, ie *instanceEnv, req spawn.Request, res *spawn.Result, rows []robot.SpawnAssignment,
	prefix string, only []int) (int, error) {
	if strings.TrimSpace(prefix) == "" {
		return 0, nil
	}
	var get func(string) *assignment.Assignment
	if store, err := assignment.LoadStoreStrictReadOnly(ie.inst.ID); err == nil && store != nil {
		get = store.Get
	}
	claimed := kickoffSkips(rows, get)
	var targets []int
	for i, t := range res.Targets {
		if t.Ready && !claimed[t.Slot] && (only == nil || slices.Contains(only, i)) {
			targets = append(targets, i)
		}
	}
	// No agent to kick off (a spawn that failed before any start): no lock,
	// no second owner check.
	if len(targets) == 0 {
		return 0, nil
	}
	senv := ie.sendEnv()
	env := spawn.Env{DB: ie.db, Send: func(ctx context.Context, r send.Request) (send.Result, error) { return send.Send(ctx, senv, r) }}
	sent := 0
	lctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	err := core.WithOwnerShared(lctx, ie.inst.Dir, func(o core.Owner) error {
		if err := core.RequireOwner(o, core.OwnerNhm); err != nil {
			return err
		}
		for _, i := range targets {
			t := &res.Targets[i]
			if only != nil {
				spawn.KickoffGated(ctx, env, req, res, t, prefix)
			} else {
				spawn.Kickoff(ctx, env, req, res, t, prefix)
			}
			if t.KickoffStatus == send.StatusSubmitted || t.KickoffStatus == send.StatusPossiblySubmitted {
				sent++
			}
		}
		return nil
	})
	return sent, err
}

// replayUntouched is the target indexes of a replayed spawn --assign whose
// agents nothing has reached (nhm-vcf): the first run died after the
// spawn and before it sent them anything. The caller holds the
// operation's lock, so the first run is over and its records are final.
// An agent is untouched when its target is ready, no kickoff is recorded,
// no receipt of another operation (a mail nudge's aside) and no ledger
// row of any status names its binding, and the binding is still the
// spawn's occupant (active at its first generation, in its pane and the
// spawn's workspace) with herdr's agent ready for a prompt; every other
// agent counts as touched.
// It fills res.WorkspaceID, which the stored outcome does not keep, from
// the first binding. A read that fails is an error: nothing is sent then.
func replayUntouched(ctx context.Context, ie *instanceEnv, res *spawn.Result) ([]int, error) {
	bs := bindings.NewStore(ie.db)
	for _, t := range res.Targets {
		if t.BindingID == "" {
			continue
		}
		b, err := bs.Get(ctx, t.BindingID)
		if err != nil {
			return nil, fmt.Errorf("the binding of %s: %w", t.MailName, err)
		}
		res.WorkspaceID = b.WorkspaceID
		break
	}
	reached, err := spawn.ReachedBindings(ctx, ie.db, res.OperationID)
	if err != nil {
		return nil, err
	}
	ledger, err := assignment.LoadStoreStrictReadOnly(ie.inst.ID)
	if err != nil {
		return nil, fmt.Errorf("the assignment ledger: %w", err)
	}
	for _, row := range ledger.List() {
		reached[strings.TrimSpace(row.OccupancyKey)] = true
		if id, _, err := spawnassign.ParseTarget(row.DispatchTarget); err == nil {
			reached[id] = true
		}
	}
	topo := spawnassign.Topology{Bindings: bs, Herdr: ie.herdr, WorkspaceID: res.WorkspaceID}
	untouched := []int{}
	for i, t := range res.Targets {
		if !t.Ready || t.BindingID == "" || t.KickoffOp != "" || t.KickoffStatus != "" || reached[t.BindingID] {
			continue
		}
		safe, err := topo.ObserveOccupant(ctx, spawnassign.Occupant{BindingID: t.BindingID, Generation: spawn.FirstGeneration,
			PaneID: t.PaneID})
		if err != nil {
			return nil, fmt.Errorf("observe %s: %w", t.MailName, err)
		}
		if safe {
			untouched = append(untouched, i)
		}
	}
	return untouched, nil
}

// identityPreamble is the human form's Preamble: each agent's work
// prompt starts as its kickoff would, with nhm's identity preamble
// (spawn.IdentityPreamble, nhm-lm4), so the kickoff's "register with
// Agent Mail" uses the identity spawn made. A binding that cannot be
// read gets none.
func identityPreamble(ctx context.Context, ie *instanceEnv) func(spawnassign.Occupant) string {
	store := bindings.NewStore(ie.db)
	return func(o spawnassign.Occupant) string {
		b, err := store.Get(ctx, o.BindingID)
		if err != nil {
			return ""
		}
		alias := b.HerdrAlias
		if alias == "" {
			alias = strings.ToLower(b.MailName)
		}
		return spawn.IdentityPreamble(b.MailName, b.MailProjectKey, alias, b.WorkspaceID)
	}
}

// robotAssignRefusal writes a preflight refusal as a --robot-spawn answer.
func robotAssignRefusal(r *robot.RobotResponse, session, project, format string, now time.Time) robot.SpawnOutput {
	out := spawn.SpawnOutput(spawn.Result{Session: session, ProjectRoot: project}, errors.New(r.Error), format, now)
	out.ErrorCode, out.Hint = r.ErrorCode, r.Hint
	return out
}
