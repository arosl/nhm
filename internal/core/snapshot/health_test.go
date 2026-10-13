package snapshot

import (
	"errors"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
)

// ntm's --robot-health=SESSION on herdr (nhm-hir, nhm-8l6): one row per
// agent of the workspace, in pane order, with ntm's health in ntm's order.
// A rate limit comes from this call's screen read, as ntm's check does,
// never from an old record and never for a working agent. herdr's unknown
// is ntm's unclassified pane. A bound agent gone from an open pane is
// unhealthy; a closed pane is left out.
func TestHealthFor(t *testing.T) {
	agent := func(ws, pane, slot string, st herdr.AgentStatus, recorded string) Agent {
		return Agent{WorkspaceID: ws, PaneID: pane, Slot: slot, Kind: "claude", State: st, Limit: recorded}
	}
	limitScreen := "working...\n  ⎿  You've hit your session limit · resets 3pm (UTC)\n❯ \n"
	m := &Model{
		Agents: []Agent{
			agent("w1", "w1:p5", "", herdr.StatusUnknown, ""),
			agent("w1", "w1:p1", "0.1", herdr.StatusIdle, ""),
			agent("w1", "w1:p2", "0.2", herdr.StatusWorking, "an old limit line"),
			agent("w1", "w1:p3", "0.3", herdr.StatusBlocked, ""),
			agent("w1", "w1:p4", "0.4", herdr.StatusDone, ""),
			agent("w1", "w1:p6", "0.6", herdr.StatusIdle, "an old limit line"),
			agent("w1", "w1:p7", "0.7", herdr.StatusIdle, ""),
			agent("w2", "w2:p1", "0.1", herdr.StatusBlocked, ""),
		},
		Absent: []bindings.Binding{{ID: "bnd-9", WorkspaceID: "w1", PaneID: "w1:p9", Slot: "1.1", Kind: "codex", MailName: "BlueLake"},
			{ID: "bnd-7", WorkspaceID: "w1", PaneID: "w1:p8", Slot: "0.8", Kind: "codex"},
			{ID: "bnd-8", WorkspaceID: "w2", PaneID: "w2:p8", Kind: "codex"}},
		panes: map[string]bool{"w1:p9": true, "w2:p8": true},
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
	reads := map[string]herdr.PaneRead{
		"w1:p2": {Text: limitScreen}, "w1:p3": {Text: limitScreen}, "w1:p4": {Text: limitScreen},
		"w1:p1": {Text: "❯ \n"}, "w1:p6": {Text: "❯ \n"},
	}
	errs := map[string]error{"w1:p7": errors.New("agent_not_found")}
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.UTC)
	out := HealthFor(m, Workspace{ID: "w1", Label: "swarm"}, reads, errs, "json", now)
	want := []struct {
		target, health string
		conf           float64
	}{
		{"0.1", robot.HealthHealthy, 1}, {"0.2", robot.HealthHealthy, 1}, {"0.3", robot.HealthBlocked, 1},
		{"0.4", robot.HealthRateLimited, 1}, {"0.6", robot.HealthHealthy, 1}, {"0.7", robot.HealthHealthy, 0.8},
		{"1.1", robot.HealthUnhealthy, 1}, {"w1:p5", robot.HealthHealthy, 0.5},
	}
	if len(out.Agents) != len(want) || out.Session != "swarm" || !out.CheckedAt.Equal(now) {
		t.Fatalf("health = %+v", out)
	}
	for i, w := range want {
		if a := out.Agents[i]; a.PaneTarget != w.target || a.Health != w.health || a.Confidence != w.conf {
			t.Errorf("agent %d = %s %s %v, want %s %s %v", i, a.PaneTarget, a.Health, a.Confidence, w.target, w.health, w.conf)
		}
	}
	if a := out.Agents[3]; a.RateLimitCount != 1 || a.LastError != "" || a.BackoffRemaining != 7200 || a.Pane != 4 ||
		a.NHM.(map[string]any)["limit"] != "⎿  You've hit your session limit · resets 3pm (UTC)" {
		t.Errorf("rate limited = %+v", a)
	}
	if a := out.Agents[5]; a.NHM.(map[string]any)["read_error"] != "agent_not_found" {
		t.Errorf("unread = %+v", a)
	}
	if a := out.Agents[6]; a.Restarts != 1 || a.AgentType != "codex" {
		t.Errorf("gone = %+v", a)
	}
	s := out.Summary
	if s.Total != 8 || s.Healthy != 5 || s.Blocked != 1 || s.RateLimited != 1 || s.Degraded != 0 || s.Unhealthy != 1 {
		t.Errorf("summary = %+v", s)
	}
}
