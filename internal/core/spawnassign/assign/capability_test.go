// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/assign/capability_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header; the agent
// import path, with ntm's tmux.AgentType and its tmux.Agent* constants
// written as agent.AgentType and its agent.AgentType* constants, as in
// capability.go (nhm-cvw); TestOmpCapabilityProfile's quality-assignment
// half, which drives ntm's Matcher, came with matcher.go (nhm-y55).

package assign

import (
	"testing"

	"github.com/arosl/nhm/internal/ui/tui/agent"
)

func TestDefaultCapabilities(t *testing.T) {
	// Verify all agent types have capabilities
	agents := []agent.AgentType{
		agent.AgentTypeClaudeCode, agent.AgentTypeCodex, agent.AgentTypeGemini,
		agent.AgentTypeCursor, agent.AgentTypeWindsurf, agent.AgentTypeAider, agent.AgentTypeOllama,
	}
	for _, agent := range agents {
		if _, ok := DefaultCapabilities[agent]; !ok {
			t.Errorf("DefaultCapabilities missing agent %s", agent)
		}
	}
}

func TestCapabilityMatrix_GetScore(t *testing.T) {
	m := NewCapabilityMatrix()

	tests := []struct {
		name    string
		agent   agent.AgentType
		task    TaskType
		wantMin float64
		wantMax float64
	}{
		{"claude refactor", agent.AgentTypeClaudeCode, TaskRefactor, 0.9, 1.0},
		{"claude analysis", agent.AgentTypeClaudeCode, TaskAnalysis, 0.85, 0.95},
		{"codex bug", agent.AgentTypeCodex, TaskBug, 0.85, 0.95},
		{"codex feature", agent.AgentTypeCodex, TaskFeature, 0.85, 0.95},
		{"gemini docs", agent.AgentTypeGemini, TaskDocs, 0.85, 0.95},
		{"unknown task defaults to 0.5", agent.AgentTypeClaudeCode, TaskType("unknown"), 0.45, 0.55},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score := m.GetScore(tc.agent, tc.task)
			if score < tc.wantMin || score > tc.wantMax {
				t.Errorf("GetScore(%s, %s) = %f, want in range [%f, %f]",
					tc.agent, tc.task, score, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// TestOmpCapabilityProfile pins omp's documented basis (the per-task midpoint
// of the Claude and Codex profiles) and that assignment ranks omp panes like
// the other profiled frontier agents instead of the 0.5 unknown default.
func TestOmpCapabilityProfile(t *testing.T) {
	m := NewCapabilityMatrix()
	tasks := []TaskType{TaskRefactor, TaskAnalysis, TaskDocs, TaskDocumentation, TaskBug, TaskFeature, TaskTesting, TaskTask, TaskChore, TaskEpic}
	for _, task := range tasks {
		claude, codex, omp := m.GetScore(agent.AgentTypeClaudeCode, task), m.GetScore(agent.AgentTypeCodex, task), m.GetScore(agent.AgentTypeOmp, task)
		mid := (claude + codex) / 2
		if omp < mid-0.0051 || omp > mid+0.0051 {
			t.Errorf("omp %s = %.2f, want the claude/codex midpoint %.3f", task, omp, mid)
		}
		if omp == 0.5 {
			t.Errorf("omp %s fell back to the neutral 0.5 default", task)
		}
	}
	if got := GetAgentScoreByString("oh-my-pi", "feature"); got != 0.88 {
		t.Errorf("GetAgentScoreByString(oh-my-pi, feature) = %.2f, want 0.88", got)
	}

	// Quality assignment: omp is the best candidate over a non-frontier
	// profile, and loses to the vendor that is strongest at the task.
	matcher := NewMatcher()
	pick := func(task TaskType, agents ...Agent) agent.AgentType {
		t.Helper()
		result := matcher.AssignTasks([]Bead{{ID: "b1", Title: string(task), TaskType: task, Priority: 2}}, agents, StrategyQuality)
		if len(result) != 1 {
			t.Fatalf("expected one assignment for %s, got %d", task, len(result))
		}
		return result[0].Agent.AgentType
	}
	omp := Agent{ID: "omp", AgentType: agent.AgentTypeOmp, Idle: true}
	ollama := Agent{ID: "oll", AgentType: agent.AgentTypeOllama, Idle: true}
	if got := pick(TaskFeature, ollama, omp); got != agent.AgentTypeOmp {
		t.Errorf("feature: picked %s over omp, want omp", got)
	}
	if got := pick(TaskRefactor, omp, Agent{ID: "cc", AgentType: agent.AgentTypeClaudeCode, Idle: true}); got != agent.AgentTypeClaudeCode {
		t.Errorf("refactor: picked %s, want claude (0.95 > omp 0.85)", got)
	}
	if got := pick(TaskRefactor, omp, Agent{ID: "cod", AgentType: agent.AgentTypeCodex, Idle: true}); got != agent.AgentTypeOmp {
		t.Errorf("refactor: picked %s, want omp (0.85 > codex 0.75)", got)
	}
}

func TestCapabilityMatrix_Clamp(t *testing.T) {
	if got := clampScore(1.5); got != 1.0 {
		t.Errorf("clampScore(1.5) = %f, want 1.0", got)
	}
	if got := clampScore(-0.5); got != 0.0 {
		t.Errorf("clampScore(-0.5) = %f, want 0.0", got)
	}
}

func TestGetAgentScoreByString(t *testing.T) {
	tests := []struct {
		agent   string
		task    string
		wantMin float64
		wantMax float64
	}{
		{"claude", "refactor", 0.9, 1.0},
		{"cc", "analysis", 0.85, 0.95},
		{"codex", "bug", 0.85, 0.95},
		{"cod", "feature", 0.85, 0.95},
		{"gemini", "docs", 0.85, 0.95},
		{"gmi", "documentation", 0.85, 0.95},
	}

	for _, tc := range tests {
		t.Run(tc.agent+"/"+tc.task, func(t *testing.T) {
			score := GetAgentScoreByString(tc.agent, tc.task)
			if score < tc.wantMin || score > tc.wantMax {
				t.Errorf("GetAgentScoreByString(%s, %s) = %f, want in range [%f, %f]",
					tc.agent, tc.task, score, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestParseAgentType(t *testing.T) {
	tests := []struct {
		input string
		want  agent.AgentType
	}{
		{"cc", agent.AgentTypeClaudeCode},
		{"claude", agent.AgentTypeClaudeCode},
		{"claude-code", agent.AgentTypeClaudeCode},
		{"claude_code", agent.AgentTypeClaudeCode},
		{"Claude", agent.AgentTypeClaudeCode},
		{"CC", agent.AgentTypeClaudeCode}, // uppercase short code
		{"cod", agent.AgentTypeCodex},
		{"codex", agent.AgentTypeCodex},
		{"codex-cli", agent.AgentTypeCodex},
		{"openai-codex", agent.AgentTypeCodex},
		{"Codex", agent.AgentTypeCodex},
		{"COD", agent.AgentTypeCodex}, // uppercase short code
		{"gmi", agent.AgentTypeGemini},
		{"gemini", agent.AgentTypeGemini},
		{"gemini_cli", agent.AgentTypeGemini},
		{"google-gemini", agent.AgentTypeGemini},
		{"Gemini", agent.AgentTypeGemini},
		{"GMI", agent.AgentTypeGemini}, // uppercase short code
		{"ws", agent.AgentTypeWindsurf},
		{"  codex-cli  ", agent.AgentTypeCodex},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseAgentType(tc.input)
			if got != tc.want {
				t.Errorf("ParseAgentType(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseAgentTypeUnknown(t *testing.T) {
	// Unknown agent types should return the lowercased input as-is
	tests := []struct {
		input string
		want  agent.AgentType
	}{
		{"unknown_agent", agent.AgentType("unknown_agent")},
		{"custom", agent.AgentType("custom")},
		{"CUSTOM", agent.AgentType("custom")}, // lowercased
		{"my-agent", agent.AgentType("my-agent")},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseAgentType(tc.input)
			if got != tc.want {
				t.Errorf("ParseAgentType(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseTaskType(t *testing.T) {
	tests := []struct {
		input string
		want  TaskType
	}{
		// Bug type - all aliases
		{"bug", TaskBug},
		{"fix", TaskBug},
		{"broken", TaskBug},
		{"error", TaskBug},
		{"crash", TaskBug},
		// Feature type - all aliases
		{"feature", TaskFeature},
		{"implement", TaskFeature},
		{"add", TaskFeature},
		{"new", TaskFeature},
		// Testing type - all aliases
		{"test", TaskTesting},
		{"testing", TaskTesting},
		{"spec", TaskTesting},
		{"coverage", TaskTesting},
		// Docs type - all aliases
		{"docs", TaskDocs},
		{"doc", TaskDocs},
		{"documentation", TaskDocs},
		{"readme", TaskDocs},
		{"comment", TaskDocs},
		// Refactor type - all aliases
		{"refactor", TaskRefactor},
		{"refactoring", TaskRefactor},
		// Analysis type - all aliases
		{"analysis", TaskAnalysis},
		{"analyze", TaskAnalysis},
		{"investigate", TaskAnalysis},
		{"research", TaskAnalysis},
		{"design", TaskAnalysis},
		// Chore type
		{"chore", TaskChore},
		// Epic type
		{"epic", TaskEpic},
		// Unknown defaults to task
		{"unknown", TaskTask},
		{"random", TaskTask},
		// Case insensitivity
		{"BUG", TaskBug},
		{"FEATURE", TaskFeature},
		{"Docs", TaskDocs},
		{"REFACTOR", TaskRefactor},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseTaskType(tc.input)
			if got != tc.want {
				t.Errorf("ParseTaskType(%s) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestGlobalMatrix(t *testing.T) {
	// Verify global matrix is accessible and functional
	gm := GlobalMatrix()
	if gm == nil {
		t.Fatal("GlobalMatrix() returned nil")
	}

	// Should match the string-based convenience accessor
	score1 := GetAgentScoreByString("claude", "refactor")
	score2 := gm.GetScore(agent.AgentTypeClaudeCode, TaskRefactor)
	if score1 != score2 {
		t.Errorf("GetAgentScoreByString != GlobalMatrix().GetScore: %f vs %f", score1, score2)
	}
}
