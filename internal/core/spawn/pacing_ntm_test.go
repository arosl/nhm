// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/spawn_pacing_test.go at commit 9d5ed977:
// TestResolveSpawnStaggerModes and
// TestResolveSpawnStaggerSmartProviderPriority; see LICENSE in this
// directory and PROVENANCE.md. Local changes (nhm-2p6.4.10,
// nhm-2p6.4.14): this header; package and imports; the learned delays are
// a map, in place of ntm's tracker; the legacy interval is a parameter;
// no subtests; nhm's tests of LoadLearnedDelays and the Pacer follow.
package spawn

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveSpawnStaggerModes(t *testing.T) {
	learned := map[string]time.Duration{"anthropic": 22500 * time.Millisecond}
	claude := []string{"claude", "claude"}
	for _, tc := range []struct {
		name         string
		mode         string
		delay        time.Duration
		legacy       time.Duration
		learned      map[string]time.Duration
		wantMode     string
		wantInterval time.Duration
		wantProvider string
	}{
		{name: "empty mode is none", delay: time.Minute, wantMode: "none"},
		{name: "explicit none ignores the fixed delay", mode: "none", delay: time.Minute, wantMode: "none"},
		{name: "legacy interval applies under none", mode: "none", legacy: 90 * time.Second, wantMode: "legacy", wantInterval: 90 * time.Second},
		{name: "explicit fixed wins over legacy", mode: "fixed", delay: 5 * time.Second, legacy: 90 * time.Second, wantMode: "fixed",
			wantInterval: 5 * time.Second},
		{name: "fixed uses the delay", mode: "fixed", delay: 20 * time.Second, wantMode: "fixed", wantInterval: 20 * time.Second},
		{name: "fixed zero delay is unpaced", mode: "fixed", wantMode: "fixed"},
		{name: "smart reads the learned delay", mode: "smart", legacy: 90 * time.Second, learned: learned, wantMode: "smart",
			wantInterval: 22500 * time.Millisecond, wantProvider: "anthropic"},
		{name: "smart without history uses the built-in delay", mode: "smart", wantMode: "smart", wantInterval: DefaultDelayAnthropic,
			wantProvider: "anthropic"},
	} {
		got := ResolveSpawnStagger(tc.mode, tc.delay, tc.legacy, claude, tc.learned)
		if got.Mode != tc.wantMode || got.Interval != tc.wantInterval || got.Provider != tc.wantProvider {
			t.Fatalf("%s: ResolveSpawnStagger = %+v, want mode %q interval %v provider %q", tc.name, got, tc.wantMode, tc.wantInterval,
				tc.wantProvider)
		}
		if got.Active() != (tc.wantInterval > 0) {
			t.Fatalf("%s: Active() = %t for %+v", tc.name, got.Active(), got)
		}
		for order, want := range []time.Duration{0, tc.wantInterval, 2 * tc.wantInterval, 3 * tc.wantInterval} {
			if delay := got.Delay(order); delay != want {
				t.Fatalf("%s: Delay(%d) = %v, want %v", tc.name, order, delay, want)
			}
		}
	}
}

// Smart mode's provider choice: the strictest provider present wins, and
// an omp-only batch uses omp's own bucket rather than Anthropic's.
func TestResolveSpawnStaggerSmartProviderPriority(t *testing.T) {
	for _, tc := range []struct {
		types    []string
		want     string
		interval time.Duration
	}{
		{types: []string{"omp", "omp"}, want: "omp", interval: DefaultDelayOpenAI},
		{types: []string{"cc", "omp"}, want: "anthropic", interval: DefaultDelayAnthropic},
		{types: []string{"codex", "gemini"}, want: "openai", interval: DefaultDelayOpenAI},
		{types: []string{"gemini", "antigravity", "omp"}, want: "google", interval: DefaultDelayGoogle},
		{types: []string{"agy"}, want: "google", interval: DefaultDelayGoogle},
		{types: []string{"opencode"}, want: "anthropic", interval: DefaultDelayAnthropic},
		{types: []string{"oc", "pi"}, want: "anthropic", interval: DefaultDelayAnthropic},
	} {
		got := ResolveSpawnStagger(StaggerSmart, 0, 0, tc.types, nil)
		if got.Provider != tc.want || got.Interval != tc.interval {
			t.Fatalf("types %v: smart stagger = %+v, want provider %q at %v", tc.types, got, tc.want, tc.interval)
		}
	}
}

// The learned delays come from the project's .nhm/rate_limits.json (ntm's
// persisted state); a delay out of (0, 1h] is the provider's default, no
// file is no history, and a bad file is an error that smart mode turns
// into the plan's warning.
func TestLoadLearnedDelays(t *testing.T) {
	dir := t.TempDir()
	if got, err := LoadLearnedDelays(dir); err != nil || got != nil {
		t.Fatalf("no file = %v %v", got, err)
	}
	path := filepath.Join(dir, ".nhm", "rate_limits.json")
	if err := writeFileAll(path, `{"state":{"anthropic":{"current_delay":30000000000},"openai":{"current_delay":-1}}}`); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLearnedDelays(dir)
	if err != nil || got["anthropic"] != 30*time.Second || got["openai"] != DefaultDelayOpenAI {
		t.Fatalf("learned = %v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	stagger, plan := PlanStagger(StaggerSmart, 0, 0, dir, []Target{{Kind: "claude", Slot: "1.1"}})
	if stagger.Interval != DefaultDelayAnthropic || plan.Warning == "" || plan.Provider != "anthropic" {
		t.Fatalf("a bad file = %+v %+v", stagger, plan)
	}
}

// The pacer holds slot i until i*interval after the first slot, which
// anchors the schedule and stamps every slot's scheduled_at; a slow
// delivery takes the next gap instead of shifting the rest.
func TestPacerHoldsEachSlotToItsOffset(t *testing.T) {
	now := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)
	var waits []time.Duration
	stagger, plan := PlanStagger(StaggerFixed, 10*time.Second, 0, "", []Target{{Kind: "claude", Slot: "1.1"}, {Kind: "codex", Slot: "1.2"},
		{Kind: "claude", Slot: "1.3"}})
	p := NewPacer(stagger, plan, func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		now = now.Add(d)
		return nil
	})
	for order := range 3 {
		if err := p.Await(context.Background(), order); err != nil {
			t.Fatal(err)
		}
		if order == 1 {
			now = now.Add(4 * time.Second) // a slow delivery
		}
	}
	if len(waits) != 2 || waits[0] != 10*time.Second || waits[1] != 6*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	if plan.Schedule[2].ScheduledAt != "2026-10-24T06:00:20Z" || plan.Schedule[1].DelayMs != 10000 || plan.Schedule[1].AgentType != "codex" {
		t.Fatalf("plan = %+v", plan.Schedule)
	}
	var nilPacer *Pacer
	if err := nilPacer.Await(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
}
