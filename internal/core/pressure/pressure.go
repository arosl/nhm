// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/pressure/pressure.go at commit 9d5ed977: Source, Level,
// Reading, Thresholds, DefaultThresholds, Classify, Snapshot, the spawn
// admission types, EvaluateSpawnAdmission and its helpers, buildSnapshot;
// and in part internal/pressure/governor.go (recommendation, joinShort,
// limitingStrings, the Refresh loop as Sample) and budget.go (Action,
// DefaultBudget's pipeline fan-out) at the same commit; see LICENSE in
// this directory and PROVENANCE.md. Local changes (nhm-a421): this
// header; the package comment; no Governor, Budget, modes or logger:
// Sample polls the providers once and drops a provider's error, as
// Refresh does after it logs it; the hint of agent_inventory_unavailable
// names herdr, where ntm's names tmux.
//
// Package pressure is ntm's spawn admission: the host's pressure sources,
// their levels, and the admit, defer or refuse decision before a spawn
// or an add (EvaluateSpawnAdmission).
package pressure

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Source identifies a pressure source.
type Source string

const (
	SourceCPU            Source = "cpu"
	SourceMemory         Source = "memory"
	SourceLoad           Source = "load"
	SourceProcCount      Source = "proc_count"
	SourcePaneActivity   Source = "pane_activity"
	SourcePipelineFanout Source = "pipeline_fanout"
	SourceRchQueue       Source = "rch_queue"
	SourceLocalBuild     Source = "local_build"
)

// Level classifies how loaded a pressure source is. Levels are ordered
// from least to most loaded; numerically larger means more loaded so
// callers can compare with `<` / `>`.
type Level int

const (
	LevelLow Level = iota
	LevelNormal
	LevelElevated
	LevelHigh
	LevelCritical
)

// String renders a Level as a stable robot-JSON token.
func (l Level) String() string {
	switch l {
	case LevelLow:
		return "low"
	case LevelNormal:
		return "normal"
	case LevelElevated:
		return "elevated"
	case LevelHigh:
		return "high"
	case LevelCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// Reading is a single observation from a Provider at a moment in time.
type Reading struct {
	Source Source  `json:"source"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
}

// Thresholds defines the value boundaries between Levels for one source.
// Boundaries are inclusive at the lower end: v >= Elevated => LevelElevated.
type Thresholds struct {
	Elevated float64 `json:"elevated"`
	High     float64 `json:"high"`
	Critical float64 `json:"critical"`
}

// DefaultThresholds returns the per-source thresholds ntm ships.
func DefaultThresholds() map[Source]Thresholds {
	return map[Source]Thresholds{
		SourceCPU:            {Elevated: 0.60, High: 0.80, Critical: 0.92},
		SourceMemory:         {Elevated: 0.65, High: 0.82, Critical: 0.92},
		SourceLoad:           {Elevated: 0.75, High: 1.00, Critical: 1.50},
		SourceProcCount:      {Elevated: 0.70, High: 0.85, Critical: 0.95},
		SourcePaneActivity:   {Elevated: 50, High: 100, Critical: 200},
		SourcePipelineFanout: {Elevated: 16, High: 32, Critical: 64},
		SourceRchQueue:       {Elevated: 0.60, High: 0.80, Critical: 0.95},
		SourceLocalBuild:     {Elevated: 4, High: 8, Critical: 16},
	}
}

// DefaultLargeSpawnThreshold is ntm's DefaultBudget().MaxPipelineFanout:
// the size of a large spawn when spawn pacing sets no
// max_concurrent_spawns.
const DefaultLargeSpawnThreshold = 16

// Classify reduces a raw reading value to a Level given thresholds.
// A value below Elevated is split between Low and Normal at Elevated/2.
func Classify(v float64, t Thresholds) Level {
	switch {
	case v >= t.Critical:
		return LevelCritical
	case v >= t.High:
		return LevelHigh
	case v >= t.Elevated:
		return LevelElevated
	case v >= t.Elevated/2:
		return LevelNormal
	default:
		return LevelLow
	}
}

// Snapshot is the aggregated pressure view at a point in time.
type Snapshot struct {
	TakenAt  time.Time        `json:"taken_at"`
	Readings []Reading        `json:"readings"`
	Levels   map[Source]Level `json:"-"`
	Overall  Level            `json:"-"`
	Limiting []Source         `json:"-"`
}

// Provider returns one or more current Readings.
type Provider interface {
	Name() string
	Read(ctx context.Context) ([]Reading, error)
}

// Sample polls every provider once and folds the readings with ntm's
// default thresholds, as ntm's Governor.Refresh does. A provider that
// fails adds no reading.
func Sample(ctx context.Context, now time.Time, providers ...Provider) Snapshot {
	var readings []Reading
	for _, p := range providers {
		rs, err := p.Read(ctx)
		if err != nil {
			continue
		}
		readings = append(readings, rs...)
	}
	return buildSnapshot(now, readings, DefaultThresholds())
}

// SpawnAdmissionDecision is the robot-stable decision token for a
// pre-spawn admission check.
type SpawnAdmissionDecision string

const (
	SpawnAdmissionAdmit  SpawnAdmissionDecision = "admit"
	SpawnAdmissionDefer  SpawnAdmissionDecision = "defer"
	SpawnAdmissionRefuse SpawnAdmissionDecision = "refuse"
)

// SpawnAdmissionInput captures the signals used to decide whether a
// requested swarm spawn should proceed. Count limits are optional: zero
// means "not configured".
type SpawnAdmissionInput struct {
	Session             string
	RequestedAgents     int
	RequestedPanes      int
	SessionPanes        int
	CurrentPanes        int
	RunningAgents       int
	RunningSessions     int
	MaxAgents           int
	LargeSpawnThreshold int
	Pressure            Snapshot
	// Count maps use canonical agent types, including every requested/live
	// agent, not only types with configured caps. They must describe the same
	// observation as the scalar totals. InventoryError means counts are unknown.
	RequestedByType map[string]int
	RunningByType   map[string]int
	MaxAgentsByType map[string]int
	InventoryError  string
}

// SpawnAdmission is the robot-stable explanation for a pre-spawn
// admission check.
type SpawnAdmission struct {
	// Serialized is true only when the caller holds the local cross-process
	// fence across observation and launch. A preview does not reserve capacity.
	Serialized              bool                       `json:"serialized"`
	Decision                SpawnAdmissionDecision     `json:"decision"`
	Reason                  string                     `json:"reason"`
	Hint                    string                     `json:"hint,omitempty"`
	Session                 string                     `json:"session,omitempty"`
	RequestedAgents         int                        `json:"requested_agents"`
	RequestedPanes          int                        `json:"requested_panes"`
	SessionPanes            int                        `json:"session_panes"`
	AdditionalPanes         int                        `json:"additional_panes"`
	CurrentPanes            int                        `json:"current_panes"`
	ProjectedPanes          int                        `json:"projected_panes"`
	RunningAgents           int                        `json:"running_agents"`
	RunningSessions         int                        `json:"running_sessions"`
	MaxAgents               int                        `json:"max_agents,omitempty"`
	AgentHeadroom           int                        `json:"agent_headroom,omitempty"`
	LargeSpawn              bool                       `json:"large_spawn"`
	LargeSpawnThreshold     int                        `json:"large_spawn_threshold,omitempty"`
	PressureLevel           string                     `json:"pressure_level"`
	Limiting                []string                   `json:"limiting,omitempty"`
	Sources                 []SpawnAdmissionSource     `json:"sources,omitempty"`
	AgentInventoryAvailable bool                       `json:"agent_inventory_available"`
	AgentInventoryError     string                     `json:"agent_inventory_error,omitempty"`
	AgentTypeLimits         []SpawnAdmissionAgentLimit `json:"agent_type_limits,omitempty"`
}

// SpawnAdmissionAgentLimit explains a configured per-type bound. These are
// observed counts, not capacity reservations or estimates of provider quota.
type SpawnAdmissionAgentLimit struct {
	AgentType     string `json:"agent_type"`
	Running       int    `json:"running"`
	Requested     int    `json:"requested"`
	Projected     int    `json:"projected"`
	Limit         int    `json:"limit"`
	Headroom      int    `json:"headroom"`
	BlocksRequest bool   `json:"blocks_request"`
}

// SpawnAdmissionSource records the source-level pressure inputs used by
// a spawn admission check.
type SpawnAdmissionSource struct {
	Source string  `json:"source"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
	Level  string  `json:"level"`
}

// EvaluateSpawnAdmission converts pressure, pane, and agent-count
// signals into an admit/defer/refuse decision. It is pure, so robot
// spawn and add share the same threshold behavior.
func EvaluateSpawnAdmission(in SpawnAdmissionInput) SpawnAdmission {
	requestedAgents := maxInt(in.RequestedAgents, 0)
	requestedPanes := maxInt(in.RequestedPanes, 0)
	sessionPanes := maxInt(in.SessionPanes, 0)
	currentPanes := maxInt(in.CurrentPanes, 0)
	runningAgents := maxInt(in.RunningAgents, 0)
	runningSessions := maxInt(in.RunningSessions, 0)
	maxAgents := maxInt(in.MaxAgents, 0)
	largeThreshold := maxInt(in.LargeSpawnThreshold, 0)
	additionalPanes := requestedPanes - sessionPanes
	if additionalPanes < 0 {
		additionalPanes = 0
	}
	projectedPanes := addSpawnCounts(currentPanes, additionalPanes)
	largeSpawn := largeThreshold > 0 && requestedAgents >= largeThreshold
	pressureLevel := in.Pressure.Overall
	limiting := limitingStrings(in.Pressure.Limiting)

	out := SpawnAdmission{
		Decision:                SpawnAdmissionAdmit,
		Reason:                  "headroom_available",
		Session:                 in.Session,
		RequestedAgents:         requestedAgents,
		RequestedPanes:          requestedPanes,
		SessionPanes:            sessionPanes,
		AdditionalPanes:         additionalPanes,
		CurrentPanes:            currentPanes,
		ProjectedPanes:          projectedPanes,
		RunningAgents:           runningAgents,
		RunningSessions:         runningSessions,
		MaxAgents:               maxAgents,
		LargeSpawn:              largeSpawn,
		LargeSpawnThreshold:     largeThreshold,
		PressureLevel:           pressureLevel.String(),
		Limiting:                limiting,
		Sources:                 spawnAdmissionSources(in.Pressure),
		AgentInventoryAvailable: in.InventoryError == "",
		AgentInventoryError:     in.InventoryError,
	}
	// postSpawnAgents is what the host would be running after this
	// admission: running + requested must stay under MaxAgents (ntm's
	// bd-1oenb).
	postSpawnAgents := addSpawnCounts(runningAgents, requestedAgents)
	if maxAgents > 0 {
		out.AgentHeadroom = maxAgents - postSpawnAgents
		if out.AgentHeadroom < 0 {
			out.AgentHeadroom = 0
		}
	}

	// Per-type caps supplement the shared host budget. A type that is
	// already over its cap does not prevent launching a different type.
	// Never infer a checked-empty inventory from a missing or partial map.
	var firstBlocked *SpawnAdmissionAgentLimit
	var invalidLimits, invalidRequestCounts bool
	types := make([]string, 0, len(in.MaxAgentsByType))
	for kind, limit := range in.MaxAgentsByType {
		if limit < 0 || kind == "" {
			invalidLimits = true
		}
		if limit > 0 {
			types = append(types, kind)
		}
	}
	sort.Strings(types)
	if in.RequestedByType != nil || len(types) > 0 {
		count, valid := sumSpawnCounts(in.RequestedByType)
		invalidRequestCounts = !valid || count != requestedAgents
	}
	if len(types) > 0 {
		count, valid := sumSpawnCounts(in.RunningByType)
		if (!valid || count != runningAgents) && out.AgentInventoryAvailable {
			out.AgentInventoryAvailable = false
			out.AgentInventoryError = "agent-type inventory is missing or disagrees with the fleet total"
		}
		if !invalidRequestCounts && out.AgentInventoryAvailable {
			for _, kind := range types {
				running, requested, limit := in.RunningByType[kind], in.RequestedByType[kind], in.MaxAgentsByType[kind]
				projected := addSpawnCounts(running, requested)
				row := SpawnAdmissionAgentLimit{
					AgentType: kind, Running: running, Requested: requested,
					Projected: projected, Limit: limit, Headroom: maxInt(limit-projected, 0),
					BlocksRequest: requested > 0 && (running > limit || requested > limit-running),
				}
				out.AgentTypeLimits = append(out.AgentTypeLimits, row)
				if row.BlocksRequest && firstBlocked == nil {
					firstBlocked = &out.AgentTypeLimits[len(out.AgentTypeLimits)-1]
				}
			}
		}
	}

	switch {
	case requestedAgents == 0 || requestedPanes == 0:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "invalid_request"
		out.Hint = "specify at least one agent"
	case invalidLimits:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "invalid_agent_type_limits"
		out.Hint = "agent-type limits require non-empty type names and non-negative counts"
	case invalidRequestCounts:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "invalid_request"
		out.Hint = "requested agent-type counts must be complete and match requested_agents"
	case (maxAgents > 0 || len(types) > 0) && !out.AgentInventoryAvailable:
		out.Decision = SpawnAdmissionDefer
		out.Reason = "agent_inventory_unavailable"
		out.Hint = "restore herdr fleet enumeration and retry; unknown running counts cannot authorize a capped spawn"
	case maxAgents > 0 && (runningAgents > maxAgents || requestedAgents > maxAgents-runningAgents):
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "agent_limit_exceeded"
		out.Hint = "running " + strconv.Itoa(runningAgents) +
			" + requested " + strconv.Itoa(requestedAgents) +
			" exceeds cap " + strconv.Itoa(maxAgents) +
			"; reduce requested agents or raise spawn_pacing agent caps"
	case firstBlocked != nil:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "agent_type_limit_exceeded"
		out.Hint = fmt.Sprintf("%s: running %d + requested %d exceeds per-type cap %d; reduce that agent type or adjust spawn_pacing.agent_type_limits.%s",
			firstBlocked.AgentType, firstBlocked.Running, firstBlocked.Requested, firstBlocked.Limit, firstBlocked.AgentType)
	case largeSpawn && pressureLevel >= LevelCritical:
		out.Decision = SpawnAdmissionRefuse
		out.Reason = "pressure_critical"
		out.Hint = recommendation(ActionSwarmSpawn, in.Pressure.Limiting)
	case largeSpawn && pressureLevel >= LevelHigh:
		out.Decision = SpawnAdmissionDefer
		out.Reason = "pressure_high"
		out.Hint = recommendation(ActionSwarmSpawn, in.Pressure.Limiting)
	}
	return out
}

// addSpawnCounts saturates diagnostic totals. Gate comparisons use subtraction
// so an unrepresentable sum cannot wrap below a cap, even a MaxInt cap.
func addSpawnCounts(a, b int) int {
	maxValue := int(^uint(0) >> 1)
	if a > maxValue-b {
		return maxValue
	}
	return a + b
}

func sumSpawnCounts(counts map[string]int) (int, bool) {
	if counts == nil {
		return 0, false
	}
	total := 0
	maxValue := int(^uint(0) >> 1)
	for kind, count := range counts {
		if kind == "" || count < 0 || total > maxValue-count {
			return 0, false
		}
		total += count
	}
	return total, true
}

func spawnAdmissionSources(snap Snapshot) []SpawnAdmissionSource {
	if len(snap.Readings) == 0 {
		return nil
	}
	ordered := append([]Reading(nil), snap.Readings...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Source < ordered[j].Source })
	out := make([]SpawnAdmissionSource, 0, len(ordered))
	for _, r := range ordered {
		lvl := LevelLow
		if snap.Levels != nil {
			lvl = snap.Levels[r.Source]
		} else if thresholds, ok := DefaultThresholds()[r.Source]; ok {
			lvl = Classify(r.Value, thresholds)
		}
		out = append(out, SpawnAdmissionSource{
			Source: string(r.Source),
			Value:  r.Value,
			Unit:   r.Unit,
			Level:  lvl.String(),
		})
	}
	return out
}

// limitingSources returns the sources whose Level matches Overall, sorted
// alphabetically so the slice is deterministic for robot output.
func limitingSources(levels map[Source]Level, overall Level) []Source {
	if len(levels) == 0 {
		return nil
	}
	out := make([]Source, 0, len(levels))
	for src, lvl := range levels {
		if lvl == overall {
			out = append(out, src)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// buildSnapshot folds readings + thresholds into a Snapshot.
func buildSnapshot(now time.Time, readings []Reading, thresh map[Source]Thresholds) Snapshot {
	levels := make(map[Source]Level, len(readings))
	overall := LevelLow
	for _, r := range readings {
		t, ok := thresh[r.Source]
		if !ok {
			levels[r.Source] = LevelLow
			continue
		}
		lvl := Classify(r.Value, t)
		levels[r.Source] = lvl
		if lvl > overall {
			overall = lvl
		}
	}
	return Snapshot{
		TakenAt:  now,
		Readings: append([]Reading(nil), readings...),
		Levels:   levels,
		Overall:  overall,
		Limiting: limitingSources(levels, overall),
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Action enumerates the gated swarm operations ntm's governor knows.
type Action string

const (
	ActionAgentSend      Action = "agent_send"
	ActionAgentInterrupt Action = "agent_interrupt"
	ActionSwarmSpawn     Action = "swarm_spawn"
	ActionPipelineFanout Action = "pipeline_fanout"
	ActionBuildOrTest    Action = "build_or_test"
	ActionScannerScan    Action = "scanner_scan"
)

// recommendation picks a hint string based on the action and the
// limiting pressure sources. Hints are stable strings so robot output
// can match on them.
func recommendation(action Action, limiting []Source) string {
	if len(limiting) == 0 {
		return ""
	}
	srcs := make([]string, len(limiting))
	for i, s := range limiting {
		srcs[i] = string(s)
	}
	sort.Strings(srcs)
	switch action {
	case ActionBuildOrTest:
		return "offload to rch: " + joinShort(srcs)
	case ActionPipelineFanout:
		return "reduce parallelism: " + joinShort(srcs)
	case ActionSwarmSpawn:
		return "reduce spawn size or wait for headroom: " + joinShort(srcs)
	case ActionAgentSend, ActionAgentInterrupt:
		return "wait for headroom: " + joinShort(srcs)
	case ActionScannerScan:
		return "lengthen scanner interval: " + joinShort(srcs)
	default:
		return joinShort(srcs)
	}
}

func joinShort(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	default:
		out := s[0]
		for _, v := range s[1:] {
			out += "," + v
		}
		return out
	}
}

func limitingStrings(in []Source) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}
