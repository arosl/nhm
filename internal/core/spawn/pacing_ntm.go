// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/spawn_pacing.go at commit 9d5ed977: SpawnStagger,
// ResolveSpawnStagger, spawnStaggerProvider, Active, Delay,
// planSpawnStagger and spawnStaggerPacer; internal/config/pacing.go at
// the same commit: the stagger bounds, ValidateSpawnStaggerMode and
// ValidateSpawnStaggerDelay; and internal/ratelimit/tracker.go at the same
// commit: the default delays, getDefaultDelay, NormalizeProvider and the
// read side of LoadFromDir and GetOptimalDelay; see LICENSE in this
// directory and PROVENANCE.md. Local changes (nhm-2p6.4.10): this header;
// package and imports; the provider of an agent is looked up by its ntm
// type (agentProvider) in place of ntm's agent package; the learned
// delays are read from the project's .nhm/rate_limits.json (nhm's path,
// the operator's path ruling, 2026-10-24), and nothing in nhm writes
// them yet; the plan is internal/robot's SpawnStaggerPlan; the pacer is
// exported for the assignment phase (Pacer), with nhm's clock and sleep;
// the legacy --stagger[=D] interval is a parameter of its own
// (nhm-2p6.4.14), not a field of a request type.
package spawn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/robot"
)

const (
	// MaxStaggerDelay bounds every per-agent stagger interval.
	MaxStaggerDelay = 5 * time.Minute
	// DefaultStaggerDelay is the fixed-mode interval when none is set.
	DefaultStaggerDelay = 30 * time.Second
)

// Default delays per provider (initial values before learning).
const (
	DefaultDelayAnthropic = 15 * time.Second
	DefaultDelayOpenAI    = 10 * time.Second
	DefaultDelayGoogle    = 8 * time.Second
)

// ValidateStaggerMode checks a stagger mode against the shared vocabulary.
func ValidateStaggerMode(mode string) error {
	switch mode {
	case "", StaggerNone, StaggerFixed, StaggerSmart:
		return nil
	default:
		return fmt.Errorf("must be one of none, fixed, or smart; got %q", mode)
	}
}

// ValidateStaggerDelay bounds a stagger interval to [0, MaxStaggerDelay].
func ValidateStaggerDelay(delay time.Duration) error {
	if delay < 0 || delay > MaxStaggerDelay {
		return fmt.Errorf("must be between 0 and %s", MaxStaggerDelay)
	}
	return nil
}

// SpawnStagger is a resolved pacing policy: the agent at delivery order i
// (0-based) receives its prompt i*Interval after the first agent.
type SpawnStagger struct {
	Mode     string        // none, fixed, or smart
	Interval time.Duration // gap between consecutive agents' deliveries
	Provider string        // smart only: rate-limit bucket Interval was learned from
}

// ResolveSpawnStagger resolves a mode, a fixed delay and the legacy
// --stagger interval for a batch of agents of ntm types. Smart mode takes
// the learned delay of the strictest provider in the batch from learned
// (nil: no learned history), else that provider's built-in default delay.
// The legacy interval applies only when the mode is none.
func ResolveSpawnStagger(mode string, delay, legacy time.Duration, agentTypes []string, learned map[string]time.Duration) SpawnStagger {
	switch mode {
	case StaggerFixed:
		return SpawnStagger{Mode: StaggerFixed, Interval: delay}
	case StaggerSmart:
		provider := spawnStaggerProvider(agentTypes)
		interval, ok := learned[provider]
		if !ok {
			interval = defaultDelay(provider)
		}
		return SpawnStagger{Mode: StaggerSmart, Interval: interval, Provider: provider}
	}
	if legacy > 0 {
		return SpawnStagger{Mode: StaggerLegacy, Interval: legacy}
	}
	return SpawnStagger{Mode: StaggerNone}
}

// spawnStaggerProvider picks the strictest rate-limit bucket present in the
// batch: anthropic, then openai, then google. omp routes panes through its own
// configured providers, so an omp-only batch uses the tracker's own omp bucket
// instead of inheriting Anthropic's learned delay. A batch with none of these
// types uses anthropic, the strictest default.
func spawnStaggerProvider(agentTypes []string) string {
	present := make(map[string]bool, len(agentTypes))
	for _, agentType := range agentTypes {
		present[agentProvider(agentType)] = true
	}
	for _, provider := range []string{"anthropic", "openai", "google", agentProvider("omp")} {
		if present[provider] {
			return provider
		}
	}
	return "anthropic"
}

// agentProvider is ntm's NormalizeProvider for an agent's ntm type
// (agentTypeString's names, and the short ones).
func agentProvider(agentType string) string {
	normalized := strings.ToLower(strings.TrimSpace(agentType))
	switch normalized {
	case "claude", "cc", "claude-code", "claude_code", "anthropic":
		return "anthropic"
	case "codex", "cod", "openai", "gpt", "chatgpt":
		return "openai"
	case "gemini", "gmi", "antigravity", "agy", "google":
		return "google"
	case "ollama":
		return "ollama"
	}
	return normalized
}

// defaultDelay returns the default delay for a provider.
func defaultDelay(provider string) time.Duration {
	switch provider {
	case "anthropic", "claude":
		return DefaultDelayAnthropic
	case "openai", "gpt":
		return DefaultDelayOpenAI
	case "google", "gemini":
		return DefaultDelayGoogle
	default:
		return DefaultDelayOpenAI // Default to OpenAI timing
	}
}

// LoadLearnedDelays reads the learned delay of each provider from the
// project's .nhm/rate_limits.json (ntm's .ntm/rate_limits.json, persisted
// data's state). No file is no history. A delay out of (0, 1h] is reset to
// the provider's default, as ntm sanitizes it.
func LoadLearnedDelays(dir string) (map[string]time.Duration, error) {
	if dir == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(dir, ".nhm", "rate_limits.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rate limits file: %w", err)
	}
	var pd struct {
		State map[string]struct {
			CurrentDelay time.Duration `json:"current_delay"`
		} `json:"state"`
	}
	if err := json.Unmarshal(data, &pd); err != nil {
		return nil, fmt.Errorf("parse rate limits file: %w", err)
	}
	out := map[string]time.Duration{}
	for provider, ps := range pd.State {
		d := ps.CurrentDelay
		if d <= 0 || d > time.Hour {
			d = defaultDelay(provider)
		}
		out[provider] = d
	}
	return out, nil
}

// Active reports whether deliveries are paced at all.
func (s SpawnStagger) Active() bool {
	return s.Mode != "" && s.Mode != StaggerNone && s.Interval > 0
}

// Delay is the offset of delivery order (0-based) from the first delivery.
func (s SpawnStagger) Delay(order int) time.Duration {
	if !s.Active() || order <= 0 {
		return 0
	}
	return time.Duration(order) * s.Interval
}

// PlanStagger resolves the stagger of a batch of deliveries, one per
// target in order, and builds its reportable schedule. An unreadable
// rate-limit history degrades smart mode to the built-in provider delay
// and says so.
func PlanStagger(mode string, delay, legacy time.Duration, dir string, targets []Target) (SpawnStagger, *robot.SpawnStaggerPlan) {
	agentTypes := make([]string, 0, len(targets))
	for _, t := range targets {
		agentTypes = append(agentTypes, ntmType(t.Kind))
	}
	var learned map[string]time.Duration
	warning := ""
	if mode == StaggerSmart {
		loaded, err := LoadLearnedDelays(dir)
		if err != nil {
			warning = fmt.Sprintf("rate-limit history unavailable (%v); using the provider's built-in delay", err)
		} else {
			learned = loaded
		}
	}
	stagger := ResolveSpawnStagger(mode, delay, legacy, agentTypes, learned)
	plan := &robot.SpawnStaggerPlan{
		Mode:       stagger.Mode,
		IntervalMs: stagger.Interval.Milliseconds(),
		Provider:   stagger.Provider,
		Warning:    warning,
		Schedule:   make([]robot.SpawnStaggerSlot, 0, len(targets)),
	}
	for i, t := range targets {
		plan.Schedule = append(plan.Schedule, robot.SpawnStaggerSlot{
			Pane: t.Slot, AgentType: agentTypes[i], Order: i + 1,
			DelayMs: stagger.Delay(i).Milliseconds(),
		})
	}
	return stagger, plan
}

// Pacer holds each delivery slot until its planned offset from the first
// slot, which starts the schedule. Slots run in order; a slow delivery
// consumes the following gap instead of shifting later agents.
type Pacer struct {
	stagger SpawnStagger
	plan    *robot.SpawnStaggerPlan
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
	base    time.Time
	started bool
}

// NewPacer paces deliveries by stagger and stamps plan's slots; now and
// wait are the clock and the sleep (nil: the system's).
func NewPacer(stagger SpawnStagger, plan *robot.SpawnStaggerPlan, now func() time.Time, wait func(context.Context, time.Duration) error) *Pacer {
	if now == nil {
		now = time.Now
	}
	if wait == nil {
		wait = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Pacer{stagger: stagger, plan: plan, now: now, wait: wait}
}

// Await blocks until slot order (0-based) is due. The first call anchors the
// schedule and stamps every slot's scheduled_at. A nil pacer never waits.
func (p *Pacer) Await(ctx context.Context, order int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	if !p.started {
		p.started = true
		p.base = p.now()
		if p.plan != nil {
			for i := range p.plan.Schedule {
				due := p.base.Add(p.stagger.Delay(p.plan.Schedule[i].Order - 1))
				p.plan.Schedule[i].ScheduledAt = due.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	if remaining := p.base.Add(p.stagger.Delay(order)).Sub(p.now()); remaining > 0 {
		if err := p.wait(ctx, remaining); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Stamp is the delivery time reported for a slot that just delivered.
func (p *Pacer) Stamp() string {
	if p == nil || p.now == nil {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return p.now().UTC().Format(time.RFC3339Nano)
}
