// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/ensemble/mode_card.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: GetModeCard is
// (*Catalog).ModeCard, on nhm's catalog (ntm's GetMode then
// GetModeByCode is nhm's Mode); findComplements walks the catalog in its
// fixed order, where ntm's map-built catalog has none, so nhm's pick is
// stable; Icon and Color stay empty, as ntm's embedded modes leave them.

package ensemble

import (
	"fmt"
	"strings"
)

// ModeCard provides detailed explanation and context for a reasoning mode.
// It extends ReasoningMode with additional context useful for understanding
// when and how to use the mode.
type ModeCard struct {
	// Core mode info (embedded from ReasoningMode)
	ModeID   string       `json:"mode_id"`
	Code     string       `json:"code"`
	Name     string       `json:"name"`
	Category ModeCategory `json:"category"`
	Tier     ModeTier     `json:"tier"`
	Icon     string       `json:"icon,omitempty"`
	Color    string       `json:"color,omitempty"`

	// Description fields
	ShortDesc      string `json:"short_desc"`
	Description    string `json:"description"`
	Differentiator string `json:"differentiator,omitempty"`

	// Usage guidance
	BestFor      []string `json:"best_for,omitempty"`
	FailureModes []string `json:"failure_modes,omitempty"`
	Examples     []string `json:"examples,omitempty"`
	Outputs      string   `json:"outputs,omitempty"`

	// Cost and compatibility
	TypicalCost int      `json:"typical_cost,omitempty"` // Estimated tokens
	Complements []string `json:"complements,omitempty"`  // Mode IDs that work well with this one
}

// NewModeCard creates a ModeCard from a ReasoningMode.
func NewModeCard(mode *ReasoningMode) *ModeCard {
	if mode == nil {
		return nil
	}

	return &ModeCard{
		ModeID:         mode.ID,
		Code:           mode.Code,
		Name:           mode.Name,
		Category:       mode.Category,
		Tier:           mode.Tier,
		ShortDesc:      mode.ShortDesc,
		Description:    mode.Description,
		Differentiator: mode.Differentiator,
		BestFor:        mode.BestFor,
		FailureModes:   mode.FailureModes,
		Outputs:        mode.Outputs,
		// Examples, TypicalCost, and Complements populated separately
	}
}

// ModeCard returns a detailed explanation card for the given mode.
// The modeRef can be either a mode ID (e.g., "deductive") or a code (e.g., "A1").
func (c *Catalog) ModeCard(modeRef string) (*ModeCard, error) {
	if c == nil {
		return nil, fmt.Errorf("mode catalog is nil")
	}
	mode, err := c.Mode(modeRef)
	if err != nil {
		return nil, err
	}

	card := NewModeCard(&mode)

	// Add examples based on mode type
	card.Examples = generateExamples(&mode)

	// Estimate typical cost based on mode complexity
	card.TypicalCost = estimateTypicalCost(&mode)

	// Find complementary modes
	card.Complements = findComplements(c, &mode)

	return card, nil
}

// generateExamples creates usage examples based on mode characteristics.
func generateExamples(mode *ReasoningMode) []string {
	if mode == nil {
		return nil
	}

	examples := make([]string, 0, 3)

	// Generate example based on category
	switch mode.Category {
	case CategoryFormal:
		examples = append(examples, "Prove that the algorithm terminates for all inputs")
		examples = append(examples, "Verify the invariants hold across all states")
	case CategoryAmpliative:
		examples = append(examples, "What patterns emerge from these user behaviors?")
		examples = append(examples, "Generalize the solution to handle similar cases")
	case CategoryUncertainty:
		examples = append(examples, "What's the probability this change causes a regression?")
		examples = append(examples, "Estimate confidence in the proposed solution")
	case CategoryCausal:
		examples = append(examples, "What caused this production outage?")
		examples = append(examples, "How would this change affect system behavior?")
	case CategoryPractical:
		examples = append(examples, "What's the best approach to migrate this service?")
		examples = append(examples, "Prioritize these technical debt items")
	case CategoryStrategic:
		examples = append(examples, "How might users try to exploit this feature?")
		examples = append(examples, "What are competitors likely doing?")
	case CategoryDialectical:
		examples = append(examples, "What are the strongest objections to this design?")
		examples = append(examples, "Steelman the alternative approach")
	default:
		examples = append(examples, "Apply "+mode.Name+" to analyze the problem")
	}

	return examples
}

// estimateTypicalCost estimates token usage based on mode complexity.
func estimateTypicalCost(mode *ReasoningMode) int {
	if mode == nil {
		return 0
	}

	// Base cost varies by category
	baseCost := 2000

	switch mode.Category {
	case CategoryFormal:
		baseCost = 3000 // Formal proofs are verbose
	case CategoryMeta:
		baseCost = 2500 // Meta-reasoning requires context
	case CategoryStrategic:
		baseCost = 2500 // Game theory needs exploration
	case CategoryDialectical:
		baseCost = 2800 // Arguments need both sides
	default:
		baseCost = 2000
	}

	// Adjust by tier
	switch mode.Tier {
	case TierCore:
		return baseCost
	case TierAdvanced:
		return int(float64(baseCost) * 1.2)
	case TierExperimental:
		return int(float64(baseCost) * 1.5)
	default:
		return baseCost
	}
}

// findComplements identifies modes that work well with the given mode.
func findComplements(catalog *Catalog, mode *ReasoningMode) []string {
	if catalog == nil || mode == nil {
		return nil
	}

	complements := make([]string, 0, 3)

	// Category-based complementary pairings
	categoryComplements := map[ModeCategory][]ModeCategory{
		CategoryFormal:      {CategoryAmpliative, CategoryMeta},
		CategoryAmpliative:  {CategoryFormal, CategoryUncertainty},
		CategoryUncertainty: {CategoryCausal, CategoryPractical},
		CategoryCausal:      {CategoryUncertainty, CategoryChange},
		CategoryPractical:   {CategoryStrategic, CategoryUncertainty},
		CategoryStrategic:   {CategoryPractical, CategoryDialectical},
		CategoryDialectical: {CategoryStrategic, CategoryMeta},
		CategoryMeta:        {CategoryFormal, CategoryDialectical},
	}

	targetCategories := categoryComplements[mode.Category]
	if len(targetCategories) == 0 {
		return nil
	}

	// Find one mode from each complementary category
	for _, cat := range targetCategories {
		for _, m := range catalog.modes {
			if m.Category == cat && m.Tier == TierCore && len(complements) < 3 {
				complements = append(complements, m.ID)
				break
			}
		}
	}

	return complements
}

// FormatCard formats a ModeCard for human-readable display.
func FormatCard(card *ModeCard) string {
	if card == nil {
		return ""
	}

	var sb strings.Builder

	// Header
	icon := card.Icon
	if icon == "" {
		icon = "📋"
	}
	sb.WriteString(fmt.Sprintf("%s %s (%s)\n", icon, card.Name, card.Code))
	sb.WriteString(fmt.Sprintf("   ID: %s | Category: %s | Tier: %s\n",
		card.ModeID, card.Category, card.Tier))
	sb.WriteString("\n")

	// Description
	sb.WriteString(fmt.Sprintf("Description:\n   %s\n\n", card.ShortDesc))
	if card.Description != "" && card.Description != card.ShortDesc {
		wrapped := wrapText(card.Description, 70, "   ")
		sb.WriteString(wrapped + "\n\n")
	}

	// Differentiator
	if card.Differentiator != "" {
		sb.WriteString(fmt.Sprintf("What makes it unique:\n   %s\n\n", card.Differentiator))
	}

	// Best for
	if len(card.BestFor) > 0 {
		sb.WriteString("Best for:\n")
		for _, use := range card.BestFor {
			sb.WriteString(fmt.Sprintf("   • %s\n", use))
		}
		sb.WriteString("\n")
	}

	// Examples
	if len(card.Examples) > 0 {
		sb.WriteString("Example prompts:\n")
		for _, ex := range card.Examples {
			sb.WriteString(fmt.Sprintf("   • \"%s\"\n", ex))
		}
		sb.WriteString("\n")
	}

	// Outputs
	if card.Outputs != "" {
		sb.WriteString(fmt.Sprintf("Typical outputs:\n   %s\n\n", card.Outputs))
	}

	// Failure modes
	if len(card.FailureModes) > 0 {
		sb.WriteString("Watch out for:\n")
		for _, fm := range card.FailureModes {
			sb.WriteString(fmt.Sprintf("   ⚠ %s\n", fm))
		}
		sb.WriteString("\n")
	}

	// Cost and complements
	if card.TypicalCost > 0 {
		sb.WriteString(fmt.Sprintf("Estimated tokens: ~%d\n", card.TypicalCost))
	}
	if len(card.Complements) > 0 {
		sb.WriteString(fmt.Sprintf("Pairs well with: %s\n", strings.Join(card.Complements, ", ")))
	}

	return sb.String()
}

// wrapText wraps text at the given width with a prefix for each line.
func wrapText(text string, width int, prefix string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	currentLine := prefix

	for _, word := range words {
		if len(currentLine)+len(word)+1 <= width+len(prefix) {
			if currentLine == prefix {
				currentLine += word
			} else {
				currentLine += " " + word
			}
		} else {
			lines = append(lines, currentLine)
			currentLine = prefix + word
		}
	}

	if currentLine != prefix {
		lines = append(lines, currentLine)
	}

	return strings.Join(lines, "\n")
}
