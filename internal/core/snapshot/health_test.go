package snapshot

import (
	"errors"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/quota"
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
	}
	// The rate-limited claude waits for its full five-hour window, as the
	// provider's record says; the weekly window is not full.
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.UTC)
	asked := 0
	live := func() []quota.Observation {
		asked++
		return []quota.Observation{{Pool: "claude", Kind: "claude",
			FiveHour: &quota.Window{UsedPct: pct(100), ResetsAt: at(now.Add(2 * time.Hour))},
			Weekly:   &quota.Window{UsedPct: pct(40), ResetsAt: at(now.Add(72 * time.Hour))}}}
	}
	reads := map[string]herdr.PaneRead{
		"w1:p2": {Text: limitScreen}, "w1:p3": {Text: limitScreen}, "w1:p4": {Text: limitScreen},
		"w1:p1": {Text: "❯ \n"}, "w1:p6": {Text: "❯ \n"},
	}
	errs := map[string]error{"w1:p7": errors.New("agent_not_found")}
	out := HealthFor(m, Workspace{ID: "w1", Label: "swarm"}, reads, errs, live, "json", now)
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
	// The quota sources are read once, and not at all without a limit.
	HealthFor(m, Workspace{ID: "w2"}, nil, nil, live, "json", now)
	if asked != 1 {
		t.Errorf("the live quota was read %d times", asked)
	}
}

func pct(v float64) *float64    { return &v }
func at(t time.Time) *time.Time { return &t }
func ago(d time.Duration) *time.Time {
	return at(time.Date(2026, 10, 13, 19, 0, 0, 0, time.UTC).Add(-d))
}

// A rate limit's wait comes from the provider's record: the latest reset
// of the full windows; with a fresh limit-reached record and no full
// window, the latest reset of any window; else 0 (review of af6ef1a).
func TestBackoff(t *testing.T) {
	now := time.Date(2026, 10, 13, 19, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Time { return at(now.Add(d)) }
	fresh := &quota.Limit{Type: "five_hour", ObservedAt: now}
	for _, c := range []struct {
		name string
		obs  quota.Observation
		want int
	}{
		{"the full window", quota.Observation{Pool: "claude",
			FiveHour: &quota.Window{UsedPct: pct(100), ResetsAt: in(time.Hour)}, Weekly: &quota.Window{UsedPct: pct(10), ResetsAt: in(48 * time.Hour)}}, 3600},
		{"both full: the later", quota.Observation{Pool: "claude",
			FiveHour: &quota.Window{UsedPct: pct(100), ResetsAt: in(time.Hour)}, Weekly: &quota.Window{UsedPct: pct(100), ResetsAt: in(2 * time.Hour)}}, 7200},
		{"a reset window does not count", quota.Observation{Pool: "claude",
			FiveHour: &quota.Window{UsedPct: pct(100), ResetsAt: ago(time.Minute), Reset: true}}, 0},
		{"limit reached, no full window", quota.Observation{Pool: "claude", LimitReached: fresh,
			FiveHour: &quota.Window{UsedPct: pct(80), ResetsAt: in(time.Hour)}, Weekly: &quota.Window{ResetsAt: in(3 * time.Hour)}}, 10800},
		{"a stale limit record", quota.Observation{Pool: "claude", LimitReached: &quota.Limit{Stale: true},
			FiveHour: &quota.Window{UsedPct: pct(80), ResetsAt: in(time.Hour)}}, 0},
		{"another pool", quota.Observation{Pool: "codex",
			FiveHour: &quota.Window{UsedPct: pct(100), ResetsAt: in(time.Hour)}}, 0},
	} {
		if got := backoff([]quota.Observation{c.obs}, "claude", now); got != c.want {
			t.Errorf("%s: backoff = %d, want %d", c.name, got, c.want)
		}
	}
}
