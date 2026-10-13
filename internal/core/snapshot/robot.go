// Partly derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/robot.go at commit 9d5ed977 (statusOverall,
// statusHealthScore and the summary counting); see LICENSE in this
// directory and PROVENANCE.md. Local changes: they read nhm's model and
// its attention severities instead of ntm's alert and state stores.

package snapshot

import (
	"math"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// BuildInfo is what the robot outputs say about nhm itself.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// staleAfterSec is the age at which nhm calls an observation stale.
const staleAfterSec = 30

// NTMType maps a herdr kind to ntm's agent type name, from the kind
// registry.
func NTMType(kind string) string { return kinds.Lookup(kind).NTMType }

// Activity maps herdr's state to ntm's activity enum, whether a prompt
// may go to the agent now, and the confidence of the observation. herdr
// observes the states itself, so a known state has confidence 1. A
// blocked agent waits for a person (herdr refuses prompts to it), so it
// is STALLED.
func Activity(s herdr.AgentStatus) (state string, safe bool, confidence float64) {
	switch s {
	case herdr.StatusWorking:
		return robot.ActivityGenerating, false, 1
	case herdr.StatusIdle, herdr.StatusDone:
		return robot.ActivityWaiting, true, 1
	case herdr.StatusBlocked:
		return robot.ActivityStalled, false, 1
	default:
		return robot.ActivityUnknown, false, 0
	}
}

func summary(m *Model) robot.StatusSummary {
	s := robot.StatusSummary{AgentsByState: map[string]int{}, AgentsByType: map[string]int{}}
	s.TotalSessions = len(m.Workspaces)
	for _, w := range m.Workspaces {
		if w.Focused {
			s.AttachedCount++
		}
	}
	for _, a := range m.Agents {
		s.TotalAgents++
		typeKey := NTMType(a.Kind)
		stateKey := strings.TrimSpace(string(a.State))
		if stateKey == "" {
			stateKey = "unknown"
		}
		s.AgentsByType[typeKey]++
		s.AgentsByState[stateKey]++
		switch typeKey {
		case "claude":
			s.ClaudeCount++
		case "codex":
			s.CodexCount++
		case "gemini":
			s.GeminiCount++
		case "antigravity":
			s.AntigravityCount++
		case "grok":
			s.GrokCount++
		case "cursor":
			s.CursorCount++
		case "opencode":
			s.OpencodeCount++
		case "omp":
			s.OmpCount++
		}
	}
	return s
}

func statusOverall(degraded []string, counts map[string]int) string {
	if counts[SevCritical] > 0 {
		return "critical"
	}
	if len(degraded) > 0 || counts[SevWarning] > 0 {
		return "degraded"
	}
	return "healthy"
}

func statusHealthScore(degraded []string, counts map[string]int) float64 {
	score := 1.0
	score -= float64(len(degraded)) * 0.1
	score -= float64(counts[SevWarning]) * 0.05
	score -= float64(counts[SevCritical]) * 0.2
	score = math.Max(0, math.Min(1, score))
	return math.Round(score*100) / 100
}

func sessionName(w Workspace) string {
	if w.Label != "" {
		return w.Label
	}
	return w.ID
}

func sessionHealth(m *Model, ws string) robot.StatusSessionHealth {
	if !m.HerdrOK() {
		return robot.StatusSessionHealth{Status: "unknown", Reason: "herdr did not answer"}
	}
	blocked := 0
	for _, a := range m.Agents {
		if a.WorkspaceID == ws && a.State == herdr.StatusBlocked {
			blocked++
		}
	}
	if blocked > 0 {
		return robot.StatusSessionHealth{Status: "degraded", Reason: plural(blocked, "agent waits", "agents wait") + " for an answer"}
	}
	return robot.StatusSessionHealth{Status: "healthy"}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func mailSummary(m *Model) *robot.AgentMailSummary {
	h, ok := m.Sources[SourceMail]
	if !ok || h.Status == Skipped {
		return nil
	}
	s := &robot.AgentMailSummary{Available: h.Status != Unavailable, Error: h.Error}
	for _, a := range m.Agents {
		if a.Mail != nil && a.Mail.Status == Fresh {
			s.SessionsRegistered++
			s.TotalUnread += a.Mail.Unread
			s.UrgentMessages += a.Mail.Urgent
		}
	}
	return s
}

// agentExtras is an agent's "_nhm" block.
func agentExtras(a Agent) map[string]any {
	x := map[string]any{"pane_id": a.PaneID, "workspace_id": a.WorkspaceID, "herdr_state": a.State}
	if a.HerdrName != "" {
		x["herdr_name"] = a.HerdrName
	}
	if a.StateChangeSeq != nil {
		x["state_change_seq"] = *a.StateChangeSeq
	}
	if a.CompletionSeq != nil {
		x["completion_seq"] = *a.CompletionSeq
	}
	if b := a.Binding; b != nil {
		x["binding_id"], x["generation"], x["mail_name"], x["mail_project_key"] = b.ID, b.Generation, b.MailName, b.ProjectKey
	}
	if a.Mail != nil {
		x["mail"] = a.Mail
	}
	return x
}

// Status projects the model onto ntm's --robot-status.
func Status(m *Model, items []Item, b BuildInfo, format string, now time.Time) robot.StatusOutput {
	degraded := m.Degraded()
	counts := CountBySeverity(items)
	out := robot.StatusOutput{
		RobotResponse: robot.NewResponse(true, format, now),
		SchemaID:      robot.StatusSchemaID,
		SchemaVersion: robot.StatusSchemaVersion,
		GeneratedAt:   m.GeneratedAt,
		System: robot.SystemInfo{Version: b.Version, Commit: b.Commit, BuildDate: b.BuildDate,
			GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH},
		Sessions:        []robot.StatusSessionHeader{},
		Summary:         summary(m),
		DegradedSources: degraded,
		AgentMail:       mailSummary(m),
	}
	for _, w := range m.Workspaces {
		out.Sessions = append(out.Sessions, robot.StatusSessionHeader{Name: sessionName(w), Attached: w.Focused,
			AgentCount: w.AgentCount, Health: sessionHealth(m, w.ID)})
	}
	out.OverallStatus = statusOverall(degraded, counts)
	out.Summary.HealthStatus = out.OverallStatus
	out.Summary.HealthScore = statusHealthScore(degraded, counts)
	out.NHM = map[string]any{"herdr_version": m.HerdrVersion, "herdr_protocol": m.Protocol, "sources": m.Sources,
		"attention": counts, "absent_bindings": len(m.Absent), "displaced_bindings": len(m.Displaced)}
	return out
}

// Snapshot projects the model onto ntm's --robot-snapshot. A slot is the
// agent's "pane"; an agent without a binding has no slot, and its herdr
// pane id is in "_nhm".
func Snapshot(m *Model, items []Item, format string, now time.Time) robot.SnapshotOutput {
	out := robot.SnapshotOutput{
		RobotResponse:            robot.NewResponse(true, format, now),
		SchemaID:                 robot.SnapshotSchemaID,
		SchemaVersion:            robot.SnapshotSchemaVersion,
		Timestamp:                m.GeneratedAt.Format(time.RFC3339),
		AttentionContractVersion: robot.AttentionContractVersion,
		ReplayWindow:             robot.SnapshotReplayWindowInfo{Reason: "nhm keeps no event log; take a new snapshot"},
		Sessions:                 []robot.SnapshotSession{},
		ActiveIncidents:          []robot.SnapshotIncident{},
		DegradedSources:          m.Degraded(),
		Summary:                  summary(m),
		Alerts:                   []string{},
	}
	counts := CountBySeverity(items)
	out.Summary.HealthStatus = statusOverall(out.DegradedSources, counts)
	out.Summary.HealthScore = statusHealthScore(out.DegradedSources, counts)
	for _, w := range m.Workspaces {
		s := robot.SnapshotSession{Name: sessionName(w), Attached: w.Focused, Agents: []robot.SnapshotAgent{}}
		for _, a := range m.Agents {
			if a.WorkspaceID != w.ID {
				continue
			}
			sa := robot.SnapshotAgent{Pane: a.Slot, Type: NTMType(a.Kind), TypeConfidence: 1, TypeMethod: "herdr",
				State: string(a.State), NHM: agentExtras(a)}
			if a.Binding != nil {
				sa.Variant = a.Binding.Model
			}
			if a.Mail != nil {
				sa.PendingMail = a.Mail.Unread
				out.MailUnread += a.Mail.Unread
			}
			s.Agents = append(s.Agents, sa)
		}
		out.Sessions = append(out.Sessions, s)
	}
	for _, it := range items {
		if it.Severity != SevInfo {
			out.Alerts = append(out.Alerts, it.Kind+": "+it.Detail)
		}
	}
	out.NHM = map[string]any{"generated_at": m.GeneratedAt, "herdr_version": m.HerdrVersion, "sources": m.Sources,
		"workspaces": m.Workspaces, "attention": items, "absent_bindings": m.Absent, "displaced_bindings": m.Displaced,
		"open_operations": m.Operations, "uncertain_receipts": m.Uncertain, "quota": m.Quota, "pending_wakes": m.Wakes}
	return out
}

func sourceEntry(h SourceHealth, now time.Time) robot.SourceHealthEntry {
	status := h.Status
	if status == Skipped {
		status = Unknown
	}
	e := robot.SourceHealthEntry{Source: h.Source, Status: status, CollectedAt: h.CollectedAt.Format(time.RFC3339),
		FreshnessSec: int(now.Sub(h.CollectedAt).Seconds()), StaleAfterSec: staleAfterSec, Provenance: "live"}
	if h.Error != "" {
		e.LastError, e.LastErrorAt = h.Error, e.CollectedAt
	}
	return e
}

func paneIdx(slot string) int {
	s, err := bindings.ParseSlot(slot)
	if err != nil {
		return 0
	}
	return s.Pane
}

// ActivityFor projects one workspace onto ntm's --robot-activity.
func ActivityFor(m *Model, ws Workspace, format string, now time.Time) robot.ActivityOutput {
	out := robot.ActivityOutput{RobotResponse: robot.NewResponse(true, format, now), Session: sessionName(ws),
		CapturedAt: m.GeneratedAt, Agents: []robot.AgentActivityInfo{},
		Summary:      robot.ActivitySummary{ByState: map[string]int{}},
		SourceHealth: map[string]robot.SourceHealthEntry{"herdr": sourceEntry(m.Sources[SourceHerdr], now)}}
	for _, a := range m.Agents {
		if a.WorkspaceID != ws.ID {
			continue
		}
		state, safe, conf := Activity(a.State)
		info := robot.AgentActivityInfo{Pane: a.Slot, PaneIdx: paneIdx(a.Slot), AgentType: NTMType(a.Kind), State: state,
			Confidence: conf, CaptureCollectedAt: m.GeneratedAt.Format(time.RFC3339), CaptureProvenance: "live",
			ObservationState: "live", ObservationFreshness: Fresh, ObservationConfidence: conf, SafeToDispatch: safe,
			NHM: agentExtras(a)}
		out.Agents = append(out.Agents, info)
		out.Summary.TotalAgents++
		out.Summary.ByState[state]++
	}
	return out
}

// HealthFor projects one workspace onto ntm's --robot-health=SESSION
// (nhm-hir), with ntm's order: unhealthy, blocked, rate_limited,
// degraded, healthy. A bound agent gone from its pane is unhealthy (ntm's
// crash, counted as a restart); herdr's blocked is ntm's interactive
// gate; a limit line nhm limits saw is a rate limit; herdr's unknown is
// degraded, as nhm cannot observe the agent. herdr gives no time of last
// output, so idle_since_seconds stays 0.
func HealthFor(m *Model, ws Workspace, format string, now time.Time) robot.SessionHealthOutput {
	out := robot.SessionHealthOutput{RobotResponse: robot.NewResponse(true, format, now), Session: sessionName(ws),
		CheckedAt: now.UTC(), Agents: []robot.SessionAgentHealth{}}
	add := func(h robot.SessionAgentHealth) {
		out.Agents = append(out.Agents, h)
		out.Summary.Total++
		switch h.Health {
		case robot.HealthHealthy:
			out.Summary.Healthy++
		case robot.HealthDegraded:
			out.Summary.Degraded++
		case robot.HealthUnhealthy:
			out.Summary.Unhealthy++
		case robot.HealthBlocked:
			out.Summary.Blocked++
		case robot.HealthRateLimited:
			out.Summary.RateLimited++
		}
	}
	for _, a := range m.Agents {
		if a.WorkspaceID != ws.ID {
			continue
		}
		_, _, conf := Activity(a.State)
		h := robot.SessionAgentHealth{Pane: paneIdx(a.Slot), PaneTarget: paneTarget(a.Slot, a.PaneID), AgentType: NTMType(a.Kind),
			Health: robot.HealthHealthy, Confidence: conf, NHM: agentExtras(a)}
		switch {
		case a.State == herdr.StatusBlocked:
			h.Health, h.LastError = robot.HealthBlocked, "the agent waits for a person (herdr: blocked)"
		case a.Limit != "":
			// As ntm: a count and the wait, no last_error.
			h.Health, h.RateLimitCount = robot.HealthRateLimited, 1
			h.BackoffRemaining = backoff(m, a.Kind, now)
			if x, ok := h.NHM.(map[string]any); ok {
				x["limit"] = a.Limit
			}
		case a.State != herdr.StatusIdle && a.State != herdr.StatusDone && a.State != herdr.StatusWorking:
			h.Health = robot.HealthDegraded
		}
		add(h)
	}
	for _, b := range m.Absent {
		if b.WorkspaceID != ws.ID {
			continue
		}
		add(robot.SessionAgentHealth{Pane: paneIdx(b.Slot), PaneTarget: paneTarget(b.Slot, b.PaneID), AgentType: NTMType(b.Kind),
			Health: robot.HealthUnhealthy, Restarts: 1, LastError: "the agent is gone from its pane",
			NHM: map[string]any{"binding_id": b.ID, "mail_name": b.MailName, "pane_id": b.PaneID}})
	}
	return out
}

// backoff is the seconds until the kind's quota pool resets: the latest
// reset of its limited windows, as the agent waits for all of them; 0
// when no limited window names a reset time in the future.
func backoff(m *Model, kind string, now time.Time) int {
	pool := kinds.Lookup(kind).QuotaPool
	wait := 0
	for _, q := range m.Quota {
		if q.PoolID != pool || !q.Limited || q.ResetsAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, q.ResetsAt)
		if err != nil || !t.After(now) {
			continue
		}
		wait = max(wait, int(t.Sub(now).Seconds()))
	}
	return wait
}

// paneTarget is a pane's address in robot output: its slot, or its herdr
// pane id without one.
func paneTarget(slot, paneID string) string {
	if slot != "" {
		return slot
	}
	return paneID
}

// TailFor projects one workspace's pane reads onto ntm's --robot-tail.
// reads and errs are keyed by herdr pane id.
func TailFor(m *Model, ws Workspace, reads map[string]herdr.PaneRead, errs map[string]error, format string, now time.Time) robot.TailOutput {
	out := robot.TailOutput{RobotResponse: robot.NewResponse(true, format, now), Session: sessionName(ws),
		CapturedAt: m.GeneratedAt, Panes: map[string]robot.PaneOutput{},
		SourceHealth: map[string]robot.SourceHealthEntry{"herdr": sourceEntry(m.Sources[SourceHerdr], now)}}
	for _, a := range m.Agents {
		if a.WorkspaceID != ws.ID {
			continue
		}
		key := a.Slot
		if key == "" {
			key = a.PaneID
		}
		_, safe, conf := Activity(a.State)
		po := robot.PaneOutput{Type: NTMType(a.Kind), State: PaneState(a.State), Lines: []string{},
			ObservationState: "live", ObservationFreshness: Fresh, ObservationConfidence: conf, SafeToDispatch: safe,
			CaptureCollectedAt: m.GeneratedAt.Format(time.RFC3339), CaptureProvenance: "live"}
		if err := errs[a.PaneID]; err != nil {
			po.CaptureProvenance, po.CaptureError = Unavailable, err.Error()
		} else if r, ok := reads[a.PaneID]; ok {
			po.Lines = splitLines(r.Text)
			po.Truncated = r.Truncated
		}
		out.Panes[key] = po
	}
	return out
}

// PaneState maps herdr's state to ntm's pane state of --robot-tail and
// --robot-interrupt: active, idle or unknown.
func PaneState(s herdr.AgentStatus) string {
	switch s {
	case herdr.StatusWorking:
		return "active"
	case herdr.StatusIdle, herdr.StatusDone:
		return "idle"
	default:
		return "unknown"
	}
}

func splitLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return []string{}
	}
	return strings.Split(text, "\n")
}
