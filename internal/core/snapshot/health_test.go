package snapshot

import (
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
)

// ntm's --robot-health=SESSION on herdr (nhm-hir): one row per agent of
// the workspace, ntm's health in ntm's order, and a bound agent gone from
// its pane as unhealthy; agents of other workspaces stay out.
func TestHealthFor(t *testing.T) {
	agent := func(ws, pane, slot string, st herdr.AgentStatus, limit string) Agent {
		return Agent{WorkspaceID: ws, PaneID: pane, Slot: slot, Kind: "claude", State: st, Limit: limit}
	}
	m := &Model{
		Agents: []Agent{
			agent("w1", "w1:p1", "0.1", herdr.StatusIdle, ""),
			agent("w1", "w1:p2", "0.2", herdr.StatusWorking, ""),
			agent("w1", "w1:p3", "0.3", herdr.StatusBlocked, "limit reached"),
			agent("w1", "w1:p4", "0.4", herdr.StatusDone, "You've hit your limit"),
			agent("w1", "w1:p5", "", herdr.StatusUnknown, ""),
			agent("w2", "w2:p1", "0.1", herdr.StatusBlocked, ""),
		},
		Absent: []bindings.Binding{{ID: "bnd-9", WorkspaceID: "w1", PaneID: "w1:p9", Slot: "1.1", Kind: "codex", MailName: "BlueLake"},
			{ID: "bnd-8", WorkspaceID: "w2", PaneID: "w2:p8", Kind: "codex"}},
		// The rate-limited claude waits for the later reset of its
		// pool's limited windows; an open window, a past reset and
		// another pool do not count.
		Quota: []Quota{
			{PoolID: "claude", Window: "five_hour", Limited: true, ResetsAt: "2026-10-13T20:00:00Z"},
			{PoolID: "claude", Window: "seven_day", Limited: true, ResetsAt: "2026-10-13T21:00:00Z"},
			{PoolID: "claude", Window: "seven_day_opus", Limited: false, ResetsAt: "2026-10-14T19:00:00Z"},
			{PoolID: "claude", Window: "old", Limited: true, ResetsAt: "2026-10-13T18:00:00Z"},
			{PoolID: "codex", Window: "five_hour", Limited: true, ResetsAt: "2026-10-14T19:00:00Z"},
		},
	}
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.UTC)
	out := HealthFor(m, Workspace{ID: "w1", Label: "swarm"}, "json", now)
	want := []struct {
		target, health string
	}{
		{"0.1", robot.HealthHealthy}, {"0.2", robot.HealthHealthy}, {"0.3", robot.HealthBlocked},
		{"0.4", robot.HealthRateLimited}, {"w1:p5", robot.HealthDegraded}, {"1.1", robot.HealthUnhealthy},
	}
	if len(out.Agents) != len(want) || out.Session != "swarm" || !out.CheckedAt.Equal(now) {
		t.Fatalf("health = %+v", out)
	}
	for i, w := range want {
		if a := out.Agents[i]; a.PaneTarget != w.target || a.Health != w.health {
			t.Errorf("agent %d = %s %s, want %s %s", i, a.PaneTarget, a.Health, w.target, w.health)
		}
	}
	// As ntm, a rate limit is a count, not an error; the line is under
	// "_nhm" (review of ea6f0fc).
	if a := out.Agents[3]; a.RateLimitCount != 1 || a.LastError != "" || a.NHM.(map[string]any)["limit"] != "You've hit your limit" || a.Pane != 4 ||
		a.BackoffRemaining != 7200 {
		t.Errorf("rate limited = %+v", a)
	}
	if a := out.Agents[5]; a.Restarts != 1 || a.AgentType != "codex" || a.Confidence != 0 {
		t.Errorf("gone = %+v", a)
	}
	s := out.Summary
	if s.Total != 6 || s.Healthy != 2 || s.Blocked != 1 || s.RateLimited != 1 || s.Degraded != 1 || s.Unhealthy != 1 {
		t.Errorf("summary = %+v", s)
	}
}
