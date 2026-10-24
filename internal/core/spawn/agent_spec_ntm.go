// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/agent_spec.go at commit 9d5ed977: modelPattern,
// agyModelPattern, effortPattern, agentTypeSupportsEffortSuffix,
// parseAgentSpec, parseTypedAgentSpec and validateAgentModelOverride; see
// LICENSE in this directory and PROVENANCE.md. Local changes (nhm-2p6.4.1):
// this header; package and imports; the result is a Want of an nhm kind,
// in place of ntm's AgentSpec of an AgentType; the kind's adapter says
// whether it takes the @effort suffix (an effort flag) and whether its
// model is pinned (agy's), in place of ntm's type switches; the pinned
// model's refusal names the kind's ntm type and flag.
package spawn

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

var (
	// specModelPattern restricts model/alias values to a safe charset to
	// prevent shell injection (ntm's modelPattern).
	specModelPattern = regexp.MustCompile(`^[A-Za-z0-9._/@:+-]+$`)

	// pinnedModelPattern is the relaxed charset of a kind whose model is
	// pinned (ntm's agyModelPattern): Antigravity's display names hold
	// spaces and parentheses, such as "Gemini 3.8 Flash (High)". Such a kind
	// refuses every model, so the charset only decides which error comes.
	pinnedModelPattern = regexp.MustCompile(`^[A-Za-z0-9._/@:+ ()-]+$`)

	// specEffortPattern restricts the effort to plain words (ntm's
	// effortPattern): '@', '/' and ':' are left out to keep the grammar
	// unambiguous.
	specEffortPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
)

// ParseCount reads one value of a kind's count flag (--cc, --cod, …) by
// ntm's grammar: "N", "N:model", "N:model:effort", or "N:model@effort"
// for a kind that takes an effort. N is at least 1. The specs of repeated
// flags add up: the caller keeps each Want.
func ParseCount(kind, value string) (Want, error) {
	a, ok := kinds.Get(kind)
	if !ok {
		return Want{}, fmt.Errorf("unknown kind %q: nhm knows %s", kind, strings.Join(kinds.Names(), ", "))
	}
	w, err := parseAgentSpec(value, a.PinnedModel != "", len(a.EffortArgs) > 0)
	if err != nil {
		return Want{}, err
	}
	w.Kind = a.Name
	if a.PinnedModel != "" && strings.TrimSpace(w.Model) != "" {
		flag := a.Name
		if len(a.Flags) > 0 {
			flag = a.Flags[0]
		}
		return Want{}, fmt.Errorf("%s model overrides are not supported; model is pinned to %q (use --%s=N)", a.NTMType, a.PinnedModel, flag)
	}
	return w, nil
}

// parseAgentSpec parses a spec string, optionally relaxing the model
// charset for a kind whose model is pinned. effortAtSuffix enables the
// `model@effort` shorthand; for a kind without an effort, '@' stays a
// literal model character (e.g. opencode `provider/model@tag` specs).
func parseAgentSpec(value string, relaxedModel bool, effortAtSuffix bool) (Want, error) {
	var spec Want

	modelRe := specModelPattern
	allowedDesc := "letters, numbers, . _ / @ : + -"
	if relaxedModel {
		modelRe = pinnedModelPattern
		allowedDesc = "letters, numbers, spaces, parentheses, . _ / @ : + -"
	}

	parts := strings.SplitN(value, ":", 3)
	if len(parts) == 0 || parts[0] == "" {
		return spec, fmt.Errorf("invalid agent spec: %q", value)
	}

	count, err := strconv.Atoi(parts[0])
	if err != nil {
		return spec, fmt.Errorf("invalid count in agent spec: %q", parts[0])
	}
	if count < 1 {
		return spec, fmt.Errorf("count must be at least 1, got %d", count)
	}
	spec.Count = count

	if len(parts) > 1 {
		model := strings.TrimSpace(parts[1])
		if model == "" {
			return spec, fmt.Errorf("empty model in agent spec: %q", value)
		}
		// `model@effort` is shorthand for `model:effort`: the suffix after
		// the last '@' is the reasoning-effort hint. Combining both forms
		// (`N:model@effort:effort`) is ambiguous and rejected.
		if at := strings.LastIndex(model, "@"); effortAtSuffix && at >= 0 {
			effort := strings.TrimSpace(model[at+1:])
			model = strings.TrimSpace(model[:at])
			if model == "" {
				return spec, fmt.Errorf("empty model before '@' in agent spec: %q", value)
			}
			if effort == "" {
				return spec, fmt.Errorf("empty reasoning effort after '@' in agent spec: %q (use N:model or N:model@effort)", value)
			}
			if len(parts) > 2 {
				return spec, fmt.Errorf("agent spec %q sets reasoning effort twice (both '@%s' and ':%s'); use one form", value, effort, strings.TrimSpace(parts[2]))
			}
			if !specEffortPattern.MatchString(effort) {
				return spec, fmt.Errorf("invalid characters in reasoning effort %q; allowed: letters, numbers, . _ + -", effort)
			}
			spec.Effort = effort
		}
		if !modelRe.MatchString(model) {
			return spec, fmt.Errorf("invalid characters in model %q; allowed: %s", model, allowedDesc)
		}
		spec.Model = model
	}

	if len(parts) > 2 {
		effort := strings.TrimSpace(parts[2])
		if effort == "" {
			return spec, fmt.Errorf("empty reasoning effort in agent spec: %q", value)
		}
		if !specEffortPattern.MatchString(effort) {
			return spec, fmt.Errorf("invalid characters in reasoning effort %q; allowed: letters, numbers, . _ + -", effort)
		}
		spec.Effort = effort
	}

	return spec, nil
}
