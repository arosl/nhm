// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/agent/types_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; only the tests of the kept functions;
// TestNhmKindsAreKnown is nhm's.

package agent

import (
	"testing"
)

func TestAgentType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want string
	}{
		{AgentTypeClaudeCode, "cc"},
		{AgentTypeCodex, "cod"},
		{AgentTypeGemini, "gmi"},
		{AgentTypeGrok, "grok"},
		{AgentTypeOllama, "ollama"},
		{AgentTypeCursor, "cursor"},
		{AgentTypeWindsurf, "windsurf"},
		{AgentTypeAider, "aider"},
		{AgentTypeUser, "user"},
		{AgentTypeUnknown, "unknown"},
	}

	for _, tt := range tests {
		if got := tt.at.String(); got != tt.want {
			t.Errorf("AgentType.String() = %q, want %q", got, tt.want)
		}
	}
}

func TestAgentType_DisplayName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want string
	}{
		{AgentTypeClaudeCode, "Claude Code"},
		{AgentTypeCodex, "Codex CLI"},
		{AgentTypeGemini, "Gemini CLI"},
		{AgentTypeGrok, "Grok Build"},
		{AgentTypeOllama, "Ollama"},
		{AgentTypeCursor, "Cursor"},
		{AgentTypeWindsurf, "Windsurf"},
		{AgentTypeAider, "Aider"},
		{AgentTypeUser, "User"},
		{AgentType("codex"), "Codex CLI"},
		{AgentType(" gemini "), "Gemini CLI"},
		{AgentTypeUnknown, "Unknown"},
		{AgentType("anything-else"), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.at.DisplayName(); got != tt.want {
			t.Errorf("AgentType(%q).DisplayName() = %q, want %q", tt.at, got, tt.want)
		}
	}
}

func TestAgentType_Canonical(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want AgentType
	}{
		{AgentTypeClaudeCode, AgentTypeClaudeCode},
		{AgentType("claudecode"), AgentTypeClaudeCode},
		{AgentType("claude"), AgentTypeClaudeCode},
		{AgentType(" Claude-Code "), AgentTypeClaudeCode},
		{AgentType("claude_code"), AgentTypeClaudeCode},
		{AgentType("codex"), AgentTypeCodex},
		{AgentType("openaicodex"), AgentTypeCodex},
		{AgentType("codex-cli"), AgentTypeCodex},
		{AgentType("openai"), AgentTypeCodex},
		{AgentType("openai-codex"), AgentTypeCodex},
		{AgentType(" GMI "), AgentTypeGemini},
		{AgentType("google"), AgentTypeGemini},
		{AgentType("googlegemini"), AgentTypeGemini},
		{AgentType("gemini_cli"), AgentTypeGemini},
		{AgentType("google-gemini"), AgentTypeGemini},
		{AgentType(" Grok-Build "), AgentTypeGrok},
		{AgentType("xai_grok_build"), AgentTypeGrok},
		{AgentType("ws"), AgentTypeWindsurf},
		{AgentType("unknown"), AgentTypeUnknown},
		{AgentType(" custom "), AgentType("custom")},
		{AgentType(""), AgentType("")},
	}

	for _, tt := range tests {
		if got := tt.at.Canonical(); got != tt.want {
			t.Errorf("AgentType(%q).Canonical() = %q, want %q", tt.at, got, tt.want)
		}
	}
}

func TestAgentType_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want bool
	}{
		{AgentTypeClaudeCode, true},
		{AgentTypeCodex, true},
		{AgentTypeGemini, true},
		{AgentTypeGrok, true},
		{AgentTypeOllama, true},
		{AgentTypeCursor, true},
		{AgentTypeWindsurf, true},
		{AgentTypeAider, true},
		{AgentTypeUser, true},
		{AgentType("codex"), true},
		{AgentType(" gemini "), true},
		{AgentTypeUnknown, false},
		{AgentType("invalid"), false},
		{AgentType(""), false},
	}

	for _, tt := range tests {
		if got := tt.at.IsValid(); got != tt.want {
			t.Errorf("AgentType(%q).IsValid() = %v, want %v", tt.at, got, tt.want)
		}
	}
}

func TestAgentTypeGrokAliasesAreExact(t *testing.T) {
	for _, alias := range []string{"grok", "grok-build", "grok_build", "grokbuild", "xai-grok-build", "xai_grok_build", "xaigrokbuild"} {
		if got := AgentType(alias).Canonical(); got != AgentTypeGrok {
			t.Errorf("AgentType(%q).Canonical() = %q, want %q", alias, got, AgentTypeGrok)
		}
	}
	for _, unrelated := range []string{"xai", "ngrok", "grok-cli", "grok-exporter"} {
		if got := AgentType(unrelated).Canonical(); got == AgentTypeGrok {
			t.Errorf("unrelated AgentType(%q) canonicalized to Grok", unrelated)
		}
	}
	if got := AgentTypeGrok.ProfileName(); got != "Grok" {
		t.Errorf("AgentTypeGrok.ProfileName() = %q, want Grok", got)
	}
}

func TestAgentType_ProfileName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		at   AgentType
		want string
	}{
		{AgentTypeClaudeCode, "Claude"},
		{AgentTypeCodex, "Codex"},
		{AgentTypeGemini, "Gemini"},
		{AgentTypeCursor, "Cursor"},
		{AgentTypeWindsurf, "Windsurf"},
		{AgentTypeAider, "Aider"},
		{AgentTypeUser, "User"},
		{AgentTypeUnknown, "Unknown"},
		{AgentType("custom"), "Custom"}, // default: capitalize first letter
	}

	for _, tt := range tests {
		t.Run(string(tt.at), func(t *testing.T) {
			t.Parallel()
			if got := tt.at.ProfileName(); got != tt.want {
				t.Errorf("AgentType(%q).ProfileName() = %q, want %q", tt.at, got, tt.want)
			}
		})
	}
}

func TestAgentType_ProfileName_Empty(t *testing.T) {
	t.Parallel()
	// Empty AgentType should return empty string from default branch
	at := AgentType("")
	if got := at.ProfileName(); got != "" {
		t.Errorf("AgentType(\"\").ProfileName() = %q, want \"\"", got)
	}
}

// TestNhmKindsAreKnown: the UI passes nhm's kind names (and the ntm_type
// of its kinds) to the colour and icon lookups, so each built-in kind
// that ntm also has must be one of ntm's types. pi is nhm's own.
func TestNhmKindsAreKnown(t *testing.T) {
	t.Parallel()
	for kind, want := range map[string]AgentType{
		"claude": AgentTypeClaudeCode, "codex": AgentTypeCodex, "agy": AgentTypeAntigravity,
		"antigravity": AgentTypeAntigravity, "grok": AgentTypeGrok, "opencode": AgentTypeOpencode, "omp": AgentTypeOmp,
	} {
		if got := AgentType(kind).Canonical(); got != want {
			t.Errorf("kind %q: Canonical() = %q, want %q", kind, got, want)
		}
	}
	if AgentType("pi").IsValid() {
		t.Error("pi is nhm's kind, not one of ntm's types")
	}
}
