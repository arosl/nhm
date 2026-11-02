// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/pressure/pressure_test.go and spawn_admission_limits_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes (nhm-a421): this header; the Classify and
// EvaluateSpawnAdmission tests only (no Governor or RobotSnapshot, which
// nhm does not port), folded into tables; nhm's test of Sample.
package pressure

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func TestClassifyLevels(t *testing.T) {
	th := Thresholds{Elevated: 0.60, High: 0.80, Critical: 0.92}
	for _, c := range []struct {
		v    float64
		want Level
	}{
		{-1, LevelLow}, {0, LevelLow}, {0.10, LevelLow}, {0.30, LevelNormal}, {0.59, LevelNormal},
		{0.60, LevelElevated}, {0.79, LevelElevated}, {0.80, LevelHigh}, {0.91, LevelHigh},
		{0.92, LevelCritical}, {1.00, LevelCritical},
	} {
		if got := Classify(c.v, th); got != c.want {
			t.Errorf("Classify(%.2f) = %s, want %s", c.v, got, c.want)
		}
	}
	th2 := DefaultThresholds()
	for _, src := range []Source{SourceCPU, SourceMemory, SourceLoad, SourceProcCount,
		SourcePaneActivity, SourcePipelineFanout, SourceRchQueue, SourceLocalBuild} {
		if _, ok := th2[src]; !ok {
			t.Errorf("DefaultThresholds missing source %q", src)
		}
	}
	// ntm's memory thresholds (pressure.go:85).
	if m := th2[SourceMemory]; m != (Thresholds{Elevated: 0.65, High: 0.82, Critical: 0.92}) {
		t.Errorf("memory thresholds = %+v", m)
	}
}

func snap(readings ...Reading) Snapshot { return buildSnapshot(testNow, readings, DefaultThresholds()) }

func TestEvaluateSpawnAdmission(t *testing.T) {
	cases := []struct {
		name     string
		in       SpawnAdmissionInput
		decision SpawnAdmissionDecision
		reason   string
		check    func(t *testing.T, a SpawnAdmission)
	}{
		{name: "headroom", in: SpawnAdmissionInput{Session: "proj", RequestedAgents: 3, RequestedPanes: 4, SessionPanes: 1,
			CurrentPanes: 8, RunningAgents: 5, RunningSessions: 2, MaxAgents: 10, LargeSpawnThreshold: 4,
			Pressure: snap(Reading{Source: SourceCPU, Value: 0.20, Unit: "ratio"}, Reading{Source: SourceMemory, Value: 0.30, Unit: "ratio"})},
			decision: SpawnAdmissionAdmit, reason: "headroom_available",
			check: func(t *testing.T, a SpawnAdmission) {
				if a.AdditionalPanes != 3 || a.ProjectedPanes != 11 || a.AgentHeadroom != 2 {
					t.Errorf("panes %d/%d, headroom %d; want 3/11, 2", a.AdditionalPanes, a.ProjectedPanes, a.AgentHeadroom)
				}
			}},
		{name: "large spawn, high pressure", in: SpawnAdmissionInput{Session: "proj", RequestedAgents: 4, RequestedPanes: 5, MaxAgents: 8,
			LargeSpawnThreshold: 4, Pressure: snap(Reading{Source: SourceCPU, Value: 0.85, Unit: "ratio"})},
			decision: SpawnAdmissionDefer, reason: "pressure_high",
			check: func(t *testing.T, a SpawnAdmission) {
				if a.Hint != "reduce spawn size or wait for headroom: cpu" || !a.LargeSpawn || a.PressureLevel != "high" {
					t.Errorf("hint %q, large %v, level %s", a.Hint, a.LargeSpawn, a.PressureLevel)
				}
			}},
		{name: "small spawn, high pressure", in: SpawnAdmissionInput{RequestedAgents: 3, RequestedPanes: 3,
			LargeSpawnThreshold: 4, Pressure: snap(Reading{Source: SourceCPU, Value: 0.99, Unit: "ratio"})},
			decision: SpawnAdmissionAdmit, reason: "headroom_available"},
		{name: "critical pressure", in: SpawnAdmissionInput{Session: "proj", RequestedAgents: 6, RequestedPanes: 7, MaxAgents: 8,
			LargeSpawnThreshold: 4, Pressure: snap(Reading{Source: SourceMemory, Value: 0.95, Unit: "ratio"})},
			decision: SpawnAdmissionRefuse, reason: "pressure_critical"},
		{name: "agent limit", in: SpawnAdmissionInput{RequestedAgents: 9, RequestedPanes: 10, MaxAgents: 7, LargeSpawnThreshold: 4},
			decision: SpawnAdmissionRefuse, reason: "agent_limit_exceeded"},
		{name: "running plus requested over the cap", in: SpawnAdmissionInput{RequestedAgents: 5, RequestedPanes: 6, RunningAgents: 10, MaxAgents: 12},
			decision: SpawnAdmissionRefuse, reason: "agent_limit_exceeded",
			check: func(t *testing.T, a SpawnAdmission) {
				for _, want := range []string{"running 10", "requested 5", "cap 12"} {
					if !strings.Contains(a.Hint, want) || a.AgentHeadroom != 0 {
						t.Errorf("hint %q (headroom %d) lacks %q", a.Hint, a.AgentHeadroom, want)
					}
				}
			}},
		{name: "at the exact cap", in: SpawnAdmissionInput{RequestedAgents: 7, RequestedPanes: 8, RunningAgents: 5, MaxAgents: 12},
			decision: SpawnAdmissionAdmit, reason: "headroom_available"},
		{name: "no agents", in: SpawnAdmissionInput{RequestedPanes: 1}, decision: SpawnAdmissionRefuse, reason: "invalid_request"},
		{name: "inventory unknown, capped", in: SpawnAdmissionInput{RequestedAgents: 1, RequestedPanes: 2, MaxAgents: 10,
			RequestedByType: map[string]int{"claude": 1}, RunningByType: map[string]int{}, InventoryError: "herdr timed out"},
			decision: SpawnAdmissionDefer, reason: "agent_inventory_unavailable"},
		{name: "inventory unknown, uncapped", in: SpawnAdmissionInput{RequestedAgents: 1, RequestedPanes: 2, InventoryError: "offline"},
			decision: SpawnAdmissionAdmit, reason: "headroom_available",
			check: func(t *testing.T, a SpawnAdmission) {
				if a.AgentInventoryAvailable || a.AgentInventoryError != "offline" {
					t.Errorf("inventory %v %q", a.AgentInventoryAvailable, a.AgentInventoryError)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := EvaluateSpawnAdmission(c.in)
			if a.Decision != c.decision || a.Reason != c.reason {
				t.Fatalf("= %s %s, want %s %s (%+v)", a.Decision, a.Reason, c.decision, c.reason, a)
			}
			if c.check != nil {
				c.check(t, a)
			}
		})
	}
}

func TestSpawnAdmissionAgentTypeLimits(t *testing.T) {
	for _, tc := range []struct {
		name               string
		running, requested map[string]int
		limits             map[string]int
		want               SpawnAdmissionDecision
		reason             string
	}{
		{"cannot borrow other type capacity", map[string]int{"claude": 2}, map[string]int{"claude": 1}, map[string]int{"claude": 2, "codex": 10}, SpawnAdmissionRefuse, "agent_type_limit_exceeded"},
		{"exact boundary", map[string]int{"claude": 1}, map[string]int{"claude": 1}, map[string]int{"claude": 2}, SpawnAdmissionAdmit, "headroom_available"},
		{"unrequested type already above cap", map[string]int{"claude": 4}, map[string]int{"codex": 1}, map[string]int{"claude": 2, "codex": 3}, SpawnAdmissionAdmit, "headroom_available"},
		{"zero disables only that type limit", map[string]int{"claude": 4}, map[string]int{"claude": 3}, map[string]int{"claude": 0}, SpawnAdmissionAdmit, "headroom_available"},
		{"unconfigured type", map[string]int{"omp": 2}, map[string]int{"omp": 3}, map[string]int{"claude": 1}, SpawnAdmissionAdmit, "headroom_available"},
		{"mixed batch is refused as a whole", map[string]int{"codex": 1}, map[string]int{"claude": 1, "codex": 2}, map[string]int{"claude": 3, "codex": 2}, SpawnAdmissionRefuse, "agent_type_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			running, _ := sumSpawnCounts(tc.running)
			requested, _ := sumSpawnCounts(tc.requested)
			in := SpawnAdmissionInput{RequestedAgents: requested, RequestedPanes: requested + 1, RunningAgents: running,
				MaxAgents: 100, RunningByType: tc.running, RequestedByType: tc.requested, MaxAgentsByType: tc.limits}
			before, _ := json.Marshal(in)
			got := EvaluateSpawnAdmission(in)
			if got.Decision != tc.want || got.Reason != tc.reason || !got.AgentInventoryAvailable {
				t.Fatalf("admission = %+v", got)
			}
			if after, _ := json.Marshal(in); string(before) != string(after) {
				t.Fatal("admission mutated input maps")
			}
		})
	}
	in := SpawnAdmissionInput{RequestedAgents: 5, RequestedPanes: 6, RunningAgents: 3,
		MaxAgents: 100, RequestedByType: map[string]int{"codex": 2, "claude": 3},
		RunningByType: map[string]int{"codex": 2, "claude": 1}, MaxAgentsByType: map[string]int{"codex": 3, "claude": 3}}
	want := []SpawnAdmissionAgentLimit{
		{AgentType: "claude", Running: 1, Requested: 3, Projected: 4, Limit: 3, Headroom: 0, BlocksRequest: true},
		{AgentType: "codex", Running: 2, Requested: 2, Projected: 4, Limit: 3, Headroom: 0, BlocksRequest: true},
	}
	if out := EvaluateSpawnAdmission(in); !reflect.DeepEqual(out.AgentTypeLimits, want) ||
		!strings.Contains(out.Hint, "claude: running 1 + requested 3") {
		t.Fatalf("incomplete or unordered refusal: %+v", out)
	}
}

func TestSpawnAdmissionRequiresCompleteTypeCountEvidence(t *testing.T) {
	for _, tc := range []struct {
		name               string
		requested, running map[string]int
		reason             string
	}{
		{"missing request", nil, map[string]int{}, "invalid_request"},
		{"partial request", map[string]int{"codex": 1}, map[string]int{}, "invalid_request"},
		{"negative request", map[string]int{"claude": 3, "codex": -1}, map[string]int{}, "invalid_request"},
		{"missing inventory", map[string]int{"claude": 2}, nil, "agent_inventory_unavailable"},
		{"inconsistent inventory", map[string]int{"claude": 2}, map[string]int{"claude": 1}, "agent_inventory_unavailable"},
		{"checked empty", map[string]int{"claude": 2}, map[string]int{}, "headroom_available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := EvaluateSpawnAdmission(SpawnAdmissionInput{RequestedAgents: 2, RequestedPanes: 3,
				RequestedByType: tc.requested, RunningByType: tc.running, MaxAgentsByType: map[string]int{"claude": 3}})
			if out.Reason != tc.reason || (tc.reason != "headroom_available" && out.Decision == SpawnAdmissionAdmit) {
				t.Fatalf("admission = %+v", out)
			}
		})
	}
	maxValue := int(^uint(0) >> 1)
	out := EvaluateSpawnAdmission(SpawnAdmissionInput{RequestedAgents: 1, RequestedPanes: 1, RunningAgents: maxValue, MaxAgents: maxValue,
		CurrentPanes: maxValue, RequestedByType: map[string]int{"claude": 1}, RunningByType: map[string]int{"claude": maxValue}})
	if out.Decision != SpawnAdmissionRefuse || out.Reason != "agent_limit_exceeded" || out.ProjectedPanes != maxValue {
		t.Fatalf("overflow bypassed the cap: %+v", out)
	}
}

type fakeProvider struct {
	readings []Reading
	err      error
}

func (f fakeProvider) Name() string { return "fake" }
func (f fakeProvider) Read(context.Context) ([]Reading, error) {
	return f.readings, f.err
}

// Sample folds every provider's readings; a provider that fails adds
// none, and the snapshot names the limiting sources.
func TestSample(t *testing.T) {
	s := Sample(context.Background(), testNow,
		fakeProvider{readings: []Reading{{Source: SourceMemory, Value: 0.83, Unit: "ratio"}, {Source: SourceLoad, Value: 0.2}}},
		fakeProvider{err: errors.New("no /proc")},
		fakeProvider{readings: []Reading{{Source: SourceProcCount, Value: 0.86}}})
	if s.Overall != LevelHigh || !reflect.DeepEqual(s.Limiting, []Source{SourceMemory, SourceProcCount}) ||
		len(s.Readings) != 3 || !s.TakenAt.Equal(testNow) {
		t.Fatalf("snapshot = %+v", s)
	}
	a := EvaluateSpawnAdmission(SpawnAdmissionInput{RequestedAgents: 4, RequestedPanes: 4, LargeSpawnThreshold: 4, Pressure: s})
	if a.Decision != SpawnAdmissionDefer || a.Hint != "reduce spawn size or wait for headroom: memory,proc_count" ||
		len(a.Sources) != 3 || a.Sources[0].Source != "load" || a.Sources[1].Level != "high" {
		t.Fatalf("admission = %+v", a)
	}
}
