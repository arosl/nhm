// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/robot/robot.go (ResolveAgentType) and internal/cli/wait.go
// (runWait's check of --type); see LICENSE in this directory and
// PROVENANCE.md. Local changes: the type resolves to nhm's kinds by their
// ntm type in the kind registry.
package cli

import (
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui/tui/agent"
)

// ntmTypeKinds are nhm's kinds of an ntm --type (ResolveAgentType's
// names: cc and claude, cod and codex, agy and antigravity …), or the
// name itself when no nhm kind has it. user and unknown are ntm's error.
func ntmTypeKinds(raw string) ([]string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	switch c := agent.AgentType(t).Canonical(); c {
	case agent.AgentTypeClaudeCode:
		t = "claude"
	case agent.AgentTypeCodex:
		t = "codex"
	case agent.AgentTypeGemini:
		t = "gemini"
	case agent.AgentTypeAntigravity:
		t = "antigravity"
	case agent.AgentTypeUser:
		t = "user"
	case agent.AgentTypeUnknown:
		t = "unknown"
	default:
		if c != "" {
			t = string(c)
		}
	}
	if t == "" || t == "unknown" || t == "user" {
		return nil, fmt.Errorf("invalid agent type '%s'", strings.TrimSpace(raw))
	}
	var out []string
	for _, a := range kinds.All() {
		if strings.EqualFold(a.NTMType, t) || a.Name == t {
			out = append(out, a.Name)
		}
	}
	if len(out) == 0 {
		out = []string{t}
	}
	return out, nil
}
