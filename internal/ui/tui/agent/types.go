// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/agent/types.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the package comment; only AgentType, its
// constants, String, Canonical, DisplayName, ProfileName and IsValid are
// kept, for the UI's colours, icons and names. ntm's Grok capability
// hints, the automated relaunch and delivery checks, NeedsDoubleEnter,
// AgentState, Recommendation and the parser types (its detection engine)
// are left out.

// Package agent names ntm's agent types for the UI. nhm's kinds (claude,
// codex, agy, grok, opencode, omp) are aliases that Canonical knows.
package agent

import (
	"strings"
)

// AgentType identifies which CLI agent is running in a pane.
type AgentType string

const (
	AgentTypeClaudeCode  AgentType = "cc"       // Claude Code CLI
	AgentTypeCodex       AgentType = "cod"      // Codex CLI (OpenAI)
	AgentTypeGemini      AgentType = "gmi"      // Gemini CLI (Google)
	AgentTypeAntigravity AgentType = "agy"      // Antigravity CLI (Google) — successor to the Gemini CLI
	AgentTypeGrok        AgentType = "grok"     // Grok Build CLI (xAI)
	AgentTypeOllama      AgentType = "ollama"   // Local Ollama CLI
	AgentTypeCursor      AgentType = "cursor"   // Cursor AI
	AgentTypeWindsurf    AgentType = "windsurf" // Windsurf IDE
	AgentTypeAider       AgentType = "aider"    // Aider CLI
	AgentTypeOpencode    AgentType = "oc"       // Opencode (https://opencode.ai) — see ntm#116
	AgentTypeOmp         AgentType = "omp"      // Oh My Pi (omp), the pi-agent fork (https://omp.sh)
	AgentTypeUser        AgentType = "user"     // User/Shell pane
	AgentTypeUnknown     AgentType = "unknown"  // Unable to determine agent type
)

// String returns the agent type as a string.
func (t AgentType) String() string {
	return string(t)
}

// Canonical normalizes aliases and formatting drift into the canonical short form
// used throughout tmux metadata and parser dispatch.
func (t AgentType) Canonical() AgentType {
	switch strings.ToLower(strings.TrimSpace(string(t))) {
	case "cc", "claude", "claude-code", "claude_code", "claudecode":
		return AgentTypeClaudeCode
	case "cod", "codex", "codex-cli", "codex_cli", "codexcli", "openai", "openai-codex", "openai_codex", "openaicodex":
		return AgentTypeCodex
	case "gmi", "gemini", "gemini-cli", "gemini_cli", "geminicli", "google", "google-ai", "google_ai", "google-gemini", "google_gemini", "googlegemini":
		return AgentTypeGemini
	case "agy", "antigravity", "antigravity-cli", "antigravity_cli", "antigravitycli", "google-antigravity":
		return AgentTypeAntigravity
	case "grok", "grok-build", "grok_build", "grokbuild", "xai-grok-build", "xai_grok_build", "xaigrokbuild":
		return AgentTypeGrok
	case "cursor":
		return AgentTypeCursor
	case "windsurf", "ws":
		return AgentTypeWindsurf
	case "aider":
		return AgentTypeAider
	case "oc", "opencode":
		return AgentTypeOpencode
	case "omp", "oh-my-pi", "oh_my_pi", "ohmypi":
		return AgentTypeOmp
	case "ollama":
		return AgentTypeOllama
	case "user":
		return AgentTypeUser
	case "unknown":
		return AgentTypeUnknown
	default:
		return AgentType(strings.TrimSpace(string(t)))
	}
}

// DisplayName returns a human-readable name for the agent type.
func (t AgentType) DisplayName() string {
	switch t.Canonical() {
	case AgentTypeClaudeCode:
		return "Claude Code"
	case AgentTypeCodex:
		return "Codex CLI"
	case AgentTypeGemini:
		return "Gemini CLI"
	case AgentTypeAntigravity:
		return "Antigravity CLI"
	case AgentTypeGrok:
		return "Grok Build"
	case AgentTypeOllama:
		return "Ollama"
	case AgentTypeCursor:
		return "Cursor"
	case AgentTypeWindsurf:
		return "Windsurf"
	case AgentTypeAider:
		return "Aider"
	case AgentTypeOpencode:
		return "Opencode"
	case AgentTypeOmp:
		return "Oh My Pi"
	case AgentTypeUser:
		return "User"
	default:
		return "Unknown"
	}
}

// ProfileName returns the friendly display name for the agent type (short form).
func (t AgentType) ProfileName() string {
	switch t.Canonical() {
	case AgentTypeClaudeCode:
		return "Claude"
	case AgentTypeCodex:
		return "Codex"
	case AgentTypeGemini:
		return "Gemini"
	case AgentTypeAntigravity:
		return "Antigravity"
	case AgentTypeGrok:
		return "Grok"
	case AgentTypeOllama:
		return "Ollama"
	case AgentTypeCursor:
		return "Cursor"
	case AgentTypeWindsurf:
		return "Windsurf"
	case AgentTypeAider:
		return "Aider"
	case AgentTypeOpencode:
		return "Opencode"
	case AgentTypeOmp:
		return "OMP"
	case AgentTypeUser:
		return "User"
	default:
		// Capitalize first letter for unknown types
		s := string(t)
		if len(s) > 0 {
			r := []rune(s)
			return strings.ToUpper(string(r[0])) + string(r[1:])
		}
		return s
	}
}

// IsValid returns true if this is a known agent type.
func (t AgentType) IsValid() bool {
	switch t.Canonical() {
	case AgentTypeClaudeCode, AgentTypeCodex, AgentTypeGemini, AgentTypeAntigravity, AgentTypeGrok, AgentTypeOllama, AgentTypeCursor, AgentTypeWindsurf, AgentTypeAider, AgentTypeOpencode, AgentTypeOmp, AgentTypeUser:
		return true
	default:
		return false
	}
}
