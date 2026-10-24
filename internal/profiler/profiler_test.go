// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/profiler/profiler_test.go; see LICENSE in this directory and
// PROVENANCE.md. Local changes: each test makes its own Profiler (New)
// in place of resetting ntm's global; the helpers act on it.
package profiler

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// enabled is a fresh profiler, turned on.
func enabled() *Profiler {
	p := New()
	p.Enable()
	return p
}

// disable turns a profiler off (test-only helper).
func (p *Profiler) disable() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled = false
}

// recordedSpans returns snapshots of all recorded spans (test-only helper).
func (p *Profiler) recordedSpans() []*Span {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*Span, len(p.spans))
	for i, span := range p.spans {
		result[i] = cloneSpanSnapshot(span)
	}
	return result
}

// GetSpansByPhase returns clones, not live span pointers.
func TestGetSpansByPhaseReturnsSnapshots(t *testing.T) {
	p := enabled()

	span := p.StartWithPhase("snapshot-op", "startup")
	span.Tag("session", "live")
	span.End()

	phaseSpans := p.GetSpansByPhase("startup")
	if len(phaseSpans) != 1 {
		t.Fatalf("expected 1 startup span, got %d", len(phaseSpans))
	}
	phaseSpans[0].Tags["session"] = "phase-mutated"
	if again := p.GetSpansByPhase("startup"); again[0].Tags["session"] != "live" {
		t.Error("GetSpansByPhase returned live tag map")
	}
}

func TestEnableDisable(t *testing.T) {
	p := New()
	if p.IsEnabled() {
		t.Error("expected profiler to be disabled initially")
	}

	p.Enable()
	if !p.IsEnabled() {
		t.Error("expected profiler to be enabled after Enable()")
	}

	p.disable()
	if p.IsEnabled() {
		t.Error("expected profiler to be disabled after disable")
	}
}

func TestSpanCreation(t *testing.T) {
	p := enabled()

	span := p.Start("test-operation")
	if span == nil {
		t.Fatal("expected non-nil span")
	}
	if span.Name != "test-operation" {
		t.Errorf("expected name 'test-operation', got %q", span.Name)
	}

	time.Sleep(10 * time.Millisecond)
	span.End()

	if span.Duration < 10*time.Millisecond {
		t.Errorf("expected duration >= 10ms, got %v", span.Duration)
	}
}

func TestSpanWithPhase(t *testing.T) {
	p := enabled()

	span := p.StartWithPhase("init-config", "startup")
	span.End()

	if span.Phase != "startup" {
		t.Errorf("expected phase 'startup', got %q", span.Phase)
	}

	startupSpans := p.GetSpansByPhase("startup")
	if len(startupSpans) != 1 {
		t.Errorf("expected 1 startup span, got %d", len(startupSpans))
	}
}

func TestSpanTags(t *testing.T) {
	p := enabled()

	span := p.Start("tagged-op")
	span.Tag("session", "myproject")
	span.Tag("count", 42)
	span.End()

	if span.Tags["session"] != "myproject" {
		t.Errorf("expected tag session='myproject', got %v", span.Tags["session"])
	}
	if span.Tags["count"] != 42 {
		t.Errorf("expected tag count=42, got %v", span.Tags["count"])
	}
}

func TestDisabledProfilingNoOps(t *testing.T) {
	p := New()

	// No panic, and no-op spans.
	span := p.Start("should-not-record")
	span.Tag("key", "value")
	span.End()

	if spans := p.recordedSpans(); len(spans) != 0 {
		t.Errorf("expected no spans when disabled, got %d", len(spans))
	}
}

func TestGetProfile(t *testing.T) {
	p := enabled()

	s1 := p.StartWithPhase("op1", "startup")
	time.Sleep(5 * time.Millisecond)
	s1.End()

	s2 := p.StartWithPhase("op2", "startup")
	time.Sleep(5 * time.Millisecond)
	s2.End()

	s3 := p.StartWithPhase("op3", "command")
	time.Sleep(5 * time.Millisecond)
	s3.End()

	profile := p.GetProfile()

	if profile.SpanCount != 3 {
		t.Errorf("expected 3 spans, got %d", profile.SpanCount)
	}

	if len(profile.Phases) < 2 {
		t.Errorf("expected at least 2 phases, got %d", len(profile.Phases))
	}

	var startupPhase *PhaseReport
	for i := range profile.Phases {
		if profile.Phases[i].Phase == "startup" {
			startupPhase = &profile.Phases[i]
			break
		}
	}
	if startupPhase == nil {
		t.Fatal("expected to find startup phase")
	}
	if startupPhase.SpanCount != 2 {
		t.Errorf("expected 2 startup spans, got %d", startupPhase.SpanCount)
	}
}

func TestGetProfileReturnsSnapshotTags(t *testing.T) {
	p := enabled()

	span := p.Start("profile-snapshot")
	span.Tag("session", "live")
	span.End()

	profile := p.GetProfile()
	if len(profile.Spans) != 1 {
		t.Fatalf("expected 1 profile span, got %d", len(profile.Spans))
	}
	profile.Spans[0].Tags["session"] = "mutated"

	again := p.GetProfile()
	if again.Spans[0].Tags["session"] != "live" {
		t.Errorf("GetProfile returned live tag map; session changed to %v", again.Spans[0].Tags["session"])
	}
}

func TestWriteJSON(t *testing.T) {
	p := enabled()

	span := p.Start("json-test")
	span.Tag("test", true)
	span.End()

	var buf bytes.Buffer
	if err := p.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	var profile Profile
	if err := json.Unmarshal(buf.Bytes(), &profile); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	if profile.SpanCount != 1 {
		t.Errorf("expected span_count=1, got %d", profile.SpanCount)
	}
}

func TestWriteText(t *testing.T) {
	p := enabled()

	s := p.StartWithPhase("text-test", "startup")
	time.Sleep(5 * time.Millisecond)
	s.End()

	var buf bytes.Buffer
	if err := p.WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Performance Profile") {
		t.Error("expected output to contain 'Performance Profile'")
	}
	if !strings.Contains(output, "text-test") {
		t.Error("expected output to contain span name 'text-test'")
	}
}

func TestDoubleEnd(t *testing.T) {
	p := enabled()

	span := p.Start("double-end")
	time.Sleep(5 * time.Millisecond)
	span.End()

	firstDuration := span.Duration

	time.Sleep(10 * time.Millisecond)
	span.End() // a no-op

	if span.Duration != firstDuration {
		t.Error("expected duration to not change after second End()")
	}
}

func TestWriteJSONIncludesRecommendations(t *testing.T) {
	p := enabled()

	span := p.Start("json-rec-test")
	span.End()

	var buf bytes.Buffer
	if err := p.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	var profile Profile
	if err := json.Unmarshal(buf.Bytes(), &profile); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	// At least the "performance looks good" recommendation.
	if len(profile.Recommendations) == 0 {
		t.Fatal("expected recommendations in JSON output")
	}

	rec := profile.Recommendations[0]
	if rec.Category == "" {
		t.Error("expected recommendation to have category")
	}
	if rec.Message == "" {
		t.Error("expected recommendation to have message")
	}
	if rec.Suggestion == "" {
		t.Error("expected recommendation to have suggestion")
	}
}

func TestWriteTextIncludesRecommendations(t *testing.T) {
	p := enabled()

	s := p.StartWithPhase("text-rec-test", "startup")
	s.End()

	var buf bytes.Buffer
	if err := p.WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Recommendations:") {
		t.Error("expected output to contain 'Recommendations:'")
	}
	if !strings.Contains(output, "→") {
		t.Error("expected output to contain recommendation suggestions")
	}
}

func TestWriteTextRecommendationSeverityIcons(t *testing.T) {
	p := enabled()

	// Slow enough to trigger a warning (>100ms).
	span := p.StartWithPhase("very-slow-op", "startup")
	time.Sleep(150 * time.Millisecond)
	span.End()

	var buf bytes.Buffer
	if err := p.WriteText(&buf); err != nil {
		t.Fatalf("WriteText failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "⚠") && !strings.Contains(output, "❌") {
		t.Error("expected output to contain warning or critical icons for slow operation")
	}
}
