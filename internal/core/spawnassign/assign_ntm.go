// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/assign.go at commit 9d5ed977: UnassignableBead,
// filterAssignableActionableRecommendationsForProject,
// filterAssignableActionableRecommendationsWithGate,
// classifyAssignableActionableRecommendationsWithGate and inferTaskType;
// see LICENSE in this directory and PROVENANCE.md. Local changes: this
// header; package and imports.

package spawnassign

import (
	"strings"

	"github.com/arosl/nhm/internal/integrations/bv"
)

type UnassignableBead struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

func filterAssignableActionableRecommendationsForProject(projectDir string, recommendations []bv.TriageRecommendation, limit int) []bv.TriageRecommendation {
	return filterAssignableActionableRecommendationsWithGate(recommendations, limit, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func filterAssignableActionableRecommendationsWithGate(recommendations []bv.TriageRecommendation, limit int, operatorGated func(string) bool) []bv.TriageRecommendation {
	filtered, _ := classifyAssignableActionableRecommendationsWithGate(recommendations, limit, operatorGated)
	return filtered
}

func classifyAssignableActionableRecommendationsWithGate(recommendations []bv.TriageRecommendation, limit int, operatorGated func(string) bool) ([]bv.TriageRecommendation, []UnassignableBead) {
	filtered := make([]bv.TriageRecommendation, 0, len(recommendations))
	unassignable := make([]UnassignableBead, 0)
	seen := make(map[string]struct{}, len(recommendations))
	for _, recommendation := range recommendations {
		beadID := strings.TrimSpace(recommendation.ID)
		if beadID == "" {
			unassignable = append(unassignable, UnassignableBead{Title: strings.TrimSpace(recommendation.Title), Reason: "missing bead ID"})
			continue
		}
		if len(recommendation.BlockedBy) > 0 {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "blocked by " + strings.Join(recommendation.BlockedBy, ", ")})
			continue
		}

		gatedLabel := ""
		for _, label := range recommendation.Labels {
			if operatorGated(label) {
				gatedLabel = strings.TrimSpace(label)
				break
			}
		}
		if gatedLabel != "" {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "operator-gated label: " + gatedLabel})
			continue
		}

		status := strings.ToLower(strings.TrimSpace(recommendation.Status))
		if status != "open" && status != "ready" {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "status is " + status})
			continue
		}
		if _, duplicate := seen[beadID]; duplicate {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "duplicate recommendation"})
			continue
		}
		if limit > 0 && len(filtered) >= limit {
			unassignable = append(unassignable, UnassignableBead{ID: beadID, Title: strings.TrimSpace(recommendation.Title), Reason: "recommendation limit reached"})
			continue
		}
		seen[beadID] = struct{}{}

		recommendation.ID = beadID
		recommendation.Title = strings.TrimSpace(recommendation.Title)
		recommendation.Type = strings.TrimSpace(recommendation.Type)
		filtered = append(filtered, recommendation)
	}
	return filtered, unassignable
}

// inferTaskType attempts to determine task type from bead metadata
func inferTaskType(bead bv.BeadPreview) string {
	title := strings.ToLower(bead.Title)

	// Check for common keywords in priority order
	// Order matters! Check specific types before generic ones.
	type rule struct {
		typ string
		kws []string
	}

	rules := []rule{
		{"bug", []string{"bug", "fix", "broken", "error", "crash"}},
		{"testing", []string{"test", "spec", "coverage"}},
		{"documentation", []string{"doc", "readme", "comment", "documentation"}},
		{"refactor", []string{"refactor", "cleanup", "improve", "consolidate"}},
		{"analysis", []string{"analyze", "investigate", "research", "design"}},
		{"feature", []string{"feature", "implement", "add", "new"}},
	}

	for _, r := range rules {
		for _, kw := range r.kws {
			if strings.Contains(title, kw) {
				return r.typ
			}
		}
	}

	return "task" // Default
}
