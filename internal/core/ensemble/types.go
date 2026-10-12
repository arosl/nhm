// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/ensemble/types.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: only the types that the
// mode catalog (modes_ntm.go) and the presets (presets_ntm.go) use, with
// JSON tags only; the UI fields (Icon, Color, PreambleKey), ntm's session,
// assignment, cache and output types, and its TOML and YAML loading are
// left out. Catalog, Mode, ModeByCode, Preset and Resolve are nhm's.

// Package ensemble is nhm's reasoning-mode catalog and ensemble presets
// (plan-v8 2.11, F.13: ntm's catalog and presets reused with provenance),
// and the ensemble runs built on them.
package ensemble

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ModeCategory is a taxonomy category (A-L) of reasoning modes.
type ModeCategory string

// The twelve categories, A to L.
const (
	CategoryFormal      ModeCategory = "Formal"
	CategoryAmpliative  ModeCategory = "Ampliative"
	CategoryUncertainty ModeCategory = "Uncertainty"
	CategoryVagueness   ModeCategory = "Vagueness"
	CategoryChange      ModeCategory = "Change"
	CategoryCausal      ModeCategory = "Causal"
	CategoryPractical   ModeCategory = "Practical"
	CategoryStrategic   ModeCategory = "Strategic"
	CategoryDialectical ModeCategory = "Dialectical"
	CategoryModal       ModeCategory = "Modal"
	CategoryDomain      ModeCategory = "Domain"
	CategoryMeta        ModeCategory = "Meta"
)

// categoryLetters maps each category to its code letter.
var categoryLetters = map[ModeCategory]string{
	CategoryFormal: "A", CategoryAmpliative: "B", CategoryUncertainty: "C", CategoryVagueness: "D",
	CategoryChange: "E", CategoryCausal: "F", CategoryPractical: "G", CategoryStrategic: "H",
	CategoryDialectical: "I", CategoryModal: "J", CategoryDomain: "K", CategoryMeta: "L",
}

// IsValid reports whether c is one of the twelve categories.
func (c ModeCategory) IsValid() bool { _, ok := categoryLetters[c]; return ok }

// ModeTier is a mode's visibility: core modes are the default set.
type ModeTier string

// Tiers.
const (
	TierCore         ModeTier = "core"
	TierAdvanced     ModeTier = "advanced"
	TierExperimental ModeTier = "experimental"
)

// IsValid reports whether t is a known tier.
func (t ModeTier) IsValid() bool {
	return t == TierCore || t == TierAdvanced || t == TierExperimental
}

// ReasoningMode is one mode of the catalog.
type ReasoningMode struct {
	ID             string       `json:"id"`
	Code           string       `json:"code"`
	Name           string       `json:"name"`
	Category       ModeCategory `json:"category"`
	Tier           ModeTier     `json:"tier"`
	ShortDesc      string       `json:"short_desc"`
	Description    string       `json:"description"`
	Outputs        string       `json:"outputs"`
	BestFor        []string     `json:"best_for"`
	FailureModes   []string     `json:"failure_modes"`
	Differentiator string       `json:"differentiator"`
}

var (
	modeIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	modeCodePattern = regexp.MustCompile(`^[A-L][0-9]+$`)
)

// Validate checks a mode's fields.
func (m ReasoningMode) Validate() error {
	switch {
	case !modeIDPattern.MatchString(m.ID):
		return fmt.Errorf("mode id %q is not lowercase letters, digits and hyphens", m.ID)
	case m.Name == "":
		return fmt.Errorf("mode %s has no name", m.ID)
	case !m.Category.IsValid():
		return fmt.Errorf("mode %s: invalid category %q", m.ID, m.Category)
	case !m.Tier.IsValid():
		return fmt.Errorf("mode %s: invalid tier %q", m.ID, m.Tier)
	case m.ShortDesc == "" || len(m.ShortDesc) > 80:
		return fmt.Errorf("mode %s: short_desc must be 1 to 80 characters", m.ID)
	case !modeCodePattern.MatchString(m.Code) || !strings.HasPrefix(m.Code, categoryLetters[m.Category]):
		return fmt.Errorf("mode %s: code %q does not match category %s", m.ID, m.Code, m.Category)
	}
	return nil
}

// SynthesisStrategy is how a preset's outputs are combined.
type SynthesisStrategy string

// The strategies ntm's presets name.
const (
	StrategyManual        SynthesisStrategy = "manual"
	StrategyAdversarial   SynthesisStrategy = "adversarial"
	StrategyConsensus     SynthesisStrategy = "consensus"
	StrategyCreative      SynthesisStrategy = "creative"
	StrategyAnalytical    SynthesisStrategy = "analytical"
	StrategyDeliberative  SynthesisStrategy = "deliberative"
	StrategyPrioritized   SynthesisStrategy = "prioritized"
	StrategyDialectical   SynthesisStrategy = "dialectical"
	StrategyMetaReasoning SynthesisStrategy = "meta-reasoning"
	StrategyVoting        SynthesisStrategy = "voting"
	StrategyArgumentation SynthesisStrategy = "argumentation-graph"
)

// Confidence is a value from 0 to 1.
type Confidence float64

// SynthesisConfig configures how a preset's outputs are combined.
type SynthesisConfig struct {
	Strategy      SynthesisStrategy `json:"strategy"`
	MinConfidence Confidence        `json:"min_confidence,omitempty"`
	MaxFindings   int               `json:"max_findings,omitempty"`
}

// BudgetConfig is a preset's resource limits.
type BudgetConfig struct {
	MaxTokensPerMode int           `json:"max_tokens_per_mode,omitempty"`
	MaxTotalTokens   int           `json:"max_total_tokens,omitempty"`
	TimeoutPerMode   time.Duration `json:"timeout_per_mode,omitempty"`
	TotalTimeout     time.Duration `json:"total_timeout,omitempty"`
}

// ModeRef names a mode by id (or by taxonomy code).
type ModeRef struct {
	ID   string `json:"id,omitempty"`
	Code string `json:"code,omitempty"`
}

// ModeRefFromID makes a ModeRef from a mode id.
func ModeRefFromID(id string) ModeRef { return ModeRef{ID: id} }

// EnsemblePreset is a named set of angles: modes to run side by side.
type EnsemblePreset struct {
	Name          string          `json:"name"`
	DisplayName   string          `json:"display_name,omitempty"`
	Description   string          `json:"description"`
	Modes         []ModeRef       `json:"modes"`
	Synthesis     SynthesisConfig `json:"synthesis,omitempty"`
	Budget        BudgetConfig    `json:"budget,omitempty"`
	AllowAdvanced bool            `json:"allow_advanced,omitempty"`
	Tags          []string        `json:"tags,omitempty"`
}

// Catalog is the modes and presets nhm knows.
type Catalog struct {
	modes   []ReasoningMode
	byID    map[string]int
	byCode  map[string]int
	presets []EnsemblePreset
}

// Errors.
var (
	ErrUnknownMode   = errors.New("ensemble: unknown mode")
	ErrUnknownPreset = errors.New("ensemble: unknown preset")
)

// Builtin returns the catalog of ntm's embedded modes and presets. It
// fails only if that data is inconsistent, which a test rules out.
func Builtin() (*Catalog, error) { return NewCatalog(EmbeddedModes, EmbeddedEnsembles) }

// NewCatalog checks modes and presets and indexes them: ids and codes are
// unique, and every preset's modes resolve, core-tier unless the preset
// allows advanced modes.
func NewCatalog(modes []ReasoningMode, presets []EnsemblePreset) (*Catalog, error) {
	c := &Catalog{modes: modes, byID: map[string]int{}, byCode: map[string]int{}, presets: presets}
	for i, m := range modes {
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if _, dup := c.byID[m.ID]; dup {
			return nil, fmt.Errorf("ensemble: mode id %s twice", m.ID)
		}
		if _, dup := c.byCode[m.Code]; dup {
			return nil, fmt.Errorf("ensemble: mode code %s twice", m.Code)
		}
		c.byID[m.ID], c.byCode[m.Code] = i, i
	}
	seen := map[string]bool{}
	for _, p := range presets {
		if seen[p.Name] {
			return nil, fmt.Errorf("ensemble: preset %s twice", p.Name)
		}
		seen[p.Name] = true
		if _, err := c.Resolve(p); err != nil {
			return nil, fmt.Errorf("ensemble: preset %s: %w", p.Name, err)
		}
	}
	return c, nil
}

// Modes returns the modes of a tier ("" for every tier), in catalog order.
func (c *Catalog) Modes(tier ModeTier) []ReasoningMode {
	var out []ReasoningMode
	for _, m := range c.modes {
		if tier == "" || m.Tier == tier {
			out = append(out, m)
		}
	}
	return out
}

// Mode finds a mode by id or by taxonomy code (case-insensitive).
func (c *Catalog) Mode(ref string) (ReasoningMode, error) {
	if i, ok := c.byID[strings.ToLower(ref)]; ok {
		return c.modes[i], nil
	}
	if i, ok := c.byCode[strings.ToUpper(ref)]; ok {
		return c.modes[i], nil
	}
	return ReasoningMode{}, fmt.Errorf("%w: %s", ErrUnknownMode, ref)
}

// Presets returns the presets, sorted by name.
func (c *Catalog) Presets() []EnsemblePreset {
	out := append([]EnsemblePreset(nil), c.presets...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Preset finds a preset by name.
func (c *Catalog) Preset(name string) (EnsemblePreset, error) {
	for _, p := range c.presets {
		if p.Name == name {
			return p, nil
		}
	}
	return EnsemblePreset{}, fmt.Errorf("%w: %s", ErrUnknownPreset, name)
}

// Resolve returns a preset's modes in order. A mode outside the core tier
// needs AllowAdvanced; a mode named twice is refused.
func (c *Catalog) Resolve(p EnsemblePreset) ([]ReasoningMode, error) {
	if len(p.Modes) == 0 {
		return nil, errors.New("no modes")
	}
	seen := map[string]bool{}
	out := make([]ReasoningMode, 0, len(p.Modes))
	for i, ref := range p.Modes {
		key := ref.ID
		if key == "" {
			key = ref.Code
		}
		m, err := c.Mode(key)
		if err != nil {
			return nil, fmt.Errorf("modes[%d]: %w", i, err)
		}
		if m.Tier != TierCore && !p.AllowAdvanced {
			return nil, fmt.Errorf("modes[%d]: %s is %s-tier; the preset does not allow advanced modes", i, m.ID, m.Tier)
		}
		if seen[m.ID] {
			return nil, fmt.Errorf("modes[%d]: %s twice", i, m.ID)
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out, nil
}
