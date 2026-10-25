// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/agent_names.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: only the generator and the
// output types (NATOAlphabet, agentTypePrefix, AgentNameMap with
// NewAgentNameMap, NewAgentNameMapWithCustomNames, generateNameLocked,
// AssignNew and AllNames, AgentNameEntry, AgentNamesOutput,
// ParseCustomNames, sortAgentNameEntries); the session reads
// (GetAgentNames, PrintAgentNames, BuildNameMapFromSession and the tmux
// wrappers) are nhm's, on herdr's panes (internal/cli).

package robot

import (
	"fmt"
	"strings"
	"sync"
)

// NATOAlphabet is the NATO phonetic alphabet used for generating agent names.
var NATOAlphabet = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot",
	"golf", "hotel", "india", "juliet", "kilo", "lima",
	"mike", "november", "oscar", "papa", "quebec", "romeo",
	"sierra", "tango", "uniform", "victor", "whiskey", "xray",
	"yankee", "zulu",
}

// agentTypePrefix maps agent type strings to their name prefixes.
var agentTypePrefix = map[string]string{
	"claude": "claude",
	"codex":  "codex",
	"gemini": "gemini",
	"user":   "user",
}

// AgentNameMap stores the mapping from agent names to pane references and back.
// It is safe for concurrent use.
type AgentNameMap struct {
	mu           sync.RWMutex
	nameToPane   map[string]string // name -> pane ref (e.g., "0.1")
	paneToName   map[string]string // pane ref -> name
	nameToType   map[string]string // name -> agent type
	sessionName  string
	natoIndex    int // next NATO alphabet index to use
	customNames  []string
	customOffset int // how many custom names have been consumed
}

// NewAgentNameMap creates a new empty AgentNameMap for the given session.
func NewAgentNameMap(sessionName string) *AgentNameMap {
	return &AgentNameMap{
		nameToPane:  make(map[string]string),
		paneToName:  make(map[string]string),
		nameToType:  make(map[string]string),
		sessionName: sessionName,
	}
}

// NewAgentNameMapWithCustomNames creates an AgentNameMap with user-supplied custom names.
// Custom names are used in order as agents are assigned names.
// If more agents exist than custom names, the NATO alphabet is used for the remainder.
func NewAgentNameMapWithCustomNames(sessionName string, customNames []string) *AgentNameMap {
	m := NewAgentNameMap(sessionName)
	m.customNames = customNames
	return m
}

// generateNameLocked is the lock-free version of GenerateName for internal use.
// Caller must hold m.mu.
func (m *AgentNameMap) generateNameLocked(agentType string) string {
	// Custom names identify launched agents, not the user pane.
	if agentType != "user" && m.customOffset < len(m.customNames) {
		name := m.customNames[m.customOffset]
		m.customOffset++
		return name
	}

	// Use NATO phonetic alphabet with agent type prefix
	prefix, ok := agentTypePrefix[agentType]
	if !ok {
		prefix = agentType
	}

	var natoWord string
	if m.natoIndex < len(NATOAlphabet) {
		natoWord = NATOAlphabet[m.natoIndex]
	} else {
		// Wrap around with numeric suffix for >26 agents
		idx := m.natoIndex % len(NATOAlphabet)
		cycle := m.natoIndex / len(NATOAlphabet)
		natoWord = fmt.Sprintf("%s-%d", NATOAlphabet[idx], cycle+1)
	}
	m.natoIndex++

	return fmt.Sprintf("%s-%s", prefix, natoWord)
}

// AssignNew generates a name for the agent type and assigns it to the pane reference.
// Returns the generated name.
func (m *AgentNameMap) AssignNew(agentType, paneRef string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := m.generateNameLocked(agentType)
	m.nameToPane[name] = paneRef
	m.paneToName[paneRef] = name
	m.nameToType[name] = agentType
	return name
}

// AllNames returns all assigned names sorted by pane reference.
func (m *AgentNameMap) AllNames() []AgentNameEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries := make([]AgentNameEntry, 0, len(m.nameToPane))
	for name, pane := range m.nameToPane {
		entries = append(entries, AgentNameEntry{
			Name:      name,
			Pane:      pane,
			AgentType: m.nameToType[name],
		})
	}

	// Sort by pane reference for deterministic output
	sortAgentNameEntries(entries)
	return entries
}

// AgentNameEntry represents a single name-to-pane mapping.
type AgentNameEntry struct {
	Name      string `json:"name"`
	Pane      string `json:"pane"`
	AgentType string `json:"agent_type"`
}

// AgentNamesOutput is the structured JSON output for --robot-agent-names.
type AgentNamesOutput struct {
	RobotResponse
	Session string           `json:"session"`
	Agents  []AgentNameEntry `json:"agents"`
	Count   int              `json:"count"`
}

// ParseCustomNames parses a comma-separated list of custom agent names.
func ParseCustomNames(namesList string) []string {
	if namesList == "" {
		return nil
	}
	parts := strings.Split(namesList, ",")
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return names
}

// sortAgentNameEntries sorts entries by pane reference for deterministic output.
func sortAgentNameEntries(entries []AgentNameEntry) {
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].Pane > entries[j].Pane {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
}
