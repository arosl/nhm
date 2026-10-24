// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/profiler/recommendation_test.go; see LICENSE in this directory
// and PROVENANCE.md. Local changes: each test makes its own Profiler.
package profiler

import (
	"testing"
	"time"
)

func TestDefaultThresholds(t *testing.T) {
	th := DefaultThresholds()

	if th.StartupWarningMs <= 0 {
		t.Error("expected positive startup warning threshold")
	}
	if th.StartupCriticalMs <= th.StartupWarningMs {
		t.Error("expected critical > warning threshold")
	}
	if th.SlowSpanMs <= 0 {
		t.Error("expected positive slow span threshold")
	}
}

func TestRecommendationsNoSpans(t *testing.T) {
	p := enabled()

	found := false
	for _, r := range p.GetRecommendations() {
		if r.Category == "instrumentation" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected instrumentation recommendation when no spans recorded")
	}
}

func TestRecommendationsGoodPerformance(t *testing.T) {
	p := enabled()

	// Thresholds so permissive that fast spans warn of nothing.
	th := Thresholds{
		StartupWarningMs:  10000,
		StartupCriticalMs: 20000,
		SlowSpanMs:        1000,
		VerySlowMs:        5000,
		PhasePercentWarn:  101, // never warn about phase balance
	}

	for i := 0; i < 3; i++ {
		s := p.StartWithPhase("fast-startup-op", "startup")
		time.Sleep(1 * time.Millisecond)
		s.End()
	}
	for i := 0; i < 2; i++ {
		s := p.StartWithPhase("fast-command-op", "command")
		time.Sleep(1 * time.Millisecond)
		s.End()
	}

	recs := p.GetRecommendationsWithThresholds(th)

	for _, r := range recs {
		if r.Severity == SeverityWarning || r.Severity == SeverityCritical {
			t.Errorf("unexpected %s recommendation: %s", r.Severity, r.Message)
		}
	}

	foundInfo := false
	for _, r := range recs {
		if r.Severity == SeverityInfo {
			foundInfo = true
			break
		}
	}
	if !foundInfo {
		t.Error("expected at least one info-level recommendation")
	}
}

func TestRecommendationsSlowStartup(t *testing.T) {
	p := enabled()

	th := Thresholds{
		StartupWarningMs:  10,
		StartupCriticalMs: 50,
		SlowSpanMs:        5,
		VerySlowMs:        20,
		PhasePercentWarn:  50,
	}

	s := p.StartWithPhase("slow-init", "startup")
	time.Sleep(60 * time.Millisecond)
	s.End()

	foundCritical := false
	for _, r := range p.GetRecommendationsWithThresholds(th) {
		if r.Category == "startup" && r.Severity == SeverityCritical {
			foundCritical = true
			break
		}
	}
	if !foundCritical {
		t.Error("expected critical startup recommendation for very slow startup")
	}
}

func TestRecommendationsSlowSpan(t *testing.T) {
	p := enabled()

	th := Thresholds{
		StartupWarningMs:  1000,
		StartupCriticalMs: 2000,
		SlowSpanMs:        5,
		VerySlowMs:        20,
		PhasePercentWarn:  80,
	}

	s := p.Start("slow-operation")
	time.Sleep(25 * time.Millisecond)
	s.End()

	foundSlowSpan := false
	for _, r := range p.GetRecommendationsWithThresholds(th) {
		if r.Category == "slow_span" && r.SpanName == "slow-operation" {
			foundSlowSpan = true
			if r.Severity != SeverityCritical {
				t.Errorf("expected critical severity for very slow span, got %s", r.Severity)
			}
			break
		}
	}
	if !foundSlowSpan {
		t.Error("expected slow_span recommendation")
	}
}

func TestSeverityConstants(t *testing.T) {
	if SeverityInfo != "info" {
		t.Errorf("expected SeverityInfo='info', got %q", SeverityInfo)
	}
	if SeverityWarning != "warning" {
		t.Errorf("expected SeverityWarning='warning', got %q", SeverityWarning)
	}
	if SeverityCritical != "critical" {
		t.Errorf("expected SeverityCritical='critical', got %q", SeverityCritical)
	}
}

func TestPhaseBalanceRecommendation(t *testing.T) {
	p := enabled()

	th := Thresholds{
		StartupWarningMs:  1000,
		StartupCriticalMs: 2000,
		SlowSpanMs:        100,
		VerySlowMs:        500,
		PhasePercentWarn:  30,
	}

	// Most of the time in one phase.
	for i := 0; i < 10; i++ {
		s := p.StartWithPhase("heavy-op", "startup")
		time.Sleep(5 * time.Millisecond)
		s.End()
	}

	s := p.StartWithPhase("light-op", "command")
	time.Sleep(1 * time.Millisecond)
	s.End()

	foundBalance := false
	for _, r := range p.GetRecommendationsWithThresholds(th) {
		if r.Category == "phase_balance" {
			foundBalance = true
			break
		}
	}
	if !foundBalance {
		t.Error("expected phase_balance recommendation for unbalanced phases")
	}
}
