// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/patterns_test.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the package; the tests of
// what patterns_ntm.go keeps (Match, MatchFirst, MatchByCategory and the
// Has* checks, the priority order, the agent filter and the variants),
// as tables without subtests; TestPatternPriority reads the library's
// Patterns, since GetPatterns is left out.

package panestate

import "testing"

func TestPatternLibrary_Match(t *testing.T) {
	lib := NewPatternLibrary()
	for _, c := range []struct {
		content, agentType string
		wantState          AgentState // "" for no match
	}{
		{"claude>", "claude", StateWaiting},
		{"claude> ", "claude", StateWaiting},
		{"codex>", "codex", StateWaiting},
		{"47% context left · ? for shortcuts", "codex", StateWaiting},
		{"› Write tests for @filename", "codex", StateWaiting},
		{"• Working (4m 51s • esc to interrupt)", "codex", StateThinking},
		{"· Working on request", "codex", StateThinking},
		{"(15s · esc to interrupt)", "codex", StateThinking},
		{"• Waiting for background terminal (4m 45s • esc to i…)", "codex", StateThinking},
		{"(3s · esc to int...)", "codex", StateThinking},
		{"· Waiting for background cargo run", "codex", StateThinking},
		{"• Thinking (3s)", "codex", StateThinking},
		{"gemini>", "gemini", StateWaiting},
		{"Rate limit exceeded", "*", StateError},
		{"HTTP 429 Too Many Requests", "*", StateError},
		{"panic: runtime error", "*", StateError},
		{"Thinking...", "*", StateThinking},
		{"Done", "*", StateWaiting},
		{"just some random text", "*", ""},
	} {
		matches := lib.Match(c.content, c.agentType)
		switch {
		case c.wantState == "" && len(matches) > 0:
			t.Errorf("%q: expected no match, got %v", c.content, matches)
		case c.wantState != "" && len(matches) == 0:
			t.Errorf("%q: expected a match, got none", c.content)
		case c.wantState != "" && matches[0].State != c.wantState:
			t.Errorf("%q: state %s, want %s", c.content, matches[0].State, c.wantState)
		}
	}
}

// TestCodexEscInterruptDoesNotMatchProse guards the truncation-tolerant
// codex_esc_interrupt regex against unrelated text that starts with
// "esc to i".
func TestCodexEscInterruptDoesNotMatchProse(t *testing.T) {
	lib := NewPatternLibrary()
	for _, content := range []string{
		"esc to i made a mistake",                    // trailing prose
		"The agent will esc to init something later", // mid-sentence
		"escape to interrupt",                        // wrong prefix
	} {
		for _, m := range lib.Match(content, "codex") {
			if m.Pattern == "codex_esc_interrupt" {
				t.Errorf("codex_esc_interrupt matched false positive %q", content)
			}
		}
	}
}

// The codex thinking patterns are agent-scoped: they do not fire on claude
// or gemini panes whose scrollback says "Working" or "Waiting".
func TestCodexWorkingPatternsDoNotLeakToOtherAgents(t *testing.T) {
	lib := NewPatternLibrary()
	content := "• Working (4m 51s • esc to interrupt)"
	for _, a := range []string{"claude", "gemini"} {
		for _, m := range lib.Match(content, a) {
			if m.Pattern == "codex_working" || m.Pattern == "codex_esc_interrupt" || m.Pattern == "codex_waiting_background" {
				t.Errorf("%s leaked to %s agent on content %q", m.Pattern, a, content)
			}
		}
	}
}

func TestPatternLibrary_MatchFirst(t *testing.T) {
	lib := NewPatternLibrary()
	if m := lib.MatchFirst("panic: something went wrong", "*"); m == nil || m.State != StateError {
		t.Errorf("panic = %v, want ERROR", m)
	}
	if m := lib.MatchFirst("completely ordinary text", "*"); m != nil {
		t.Errorf("ordinary text = %v, want nil", m)
	}
	if m := MatchFirstPattern("panic: x", "cc"); m == nil || m.Pattern != "panic" {
		t.Errorf("MatchFirstPattern = %v", m)
	}
}

func TestPatternLibrary_MatchByCategory(t *testing.T) {
	lib := NewPatternLibrary()
	errs := lib.MatchByCategory("Rate limit error occurred", "*", CategoryError)
	if len(errs) == 0 {
		t.Error("expected error matches")
	}
	for _, m := range errs {
		if m.Category != CategoryError {
			t.Errorf("expected error category, got %s", m.Category)
		}
	}
	if len(lib.MatchByCategory("claude>", "claude", CategoryIdle)) == 0 {
		t.Error("expected idle matches")
	}
}

func TestPatternLibrary_AgentFiltering(t *testing.T) {
	lib := NewPatternLibrary()
	has := func(ms []PatternMatch, name string) bool {
		for _, m := range ms {
			if m.Pattern == name {
				return true
			}
		}
		return false
	}
	if !has(lib.Match("claude>", "claude"), "claude_prompt") {
		t.Error("expected claude_prompt match for claude agent")
	}
	if has(lib.Match("claude>", "codex"), "claude_prompt") {
		t.Error("claude_prompt should not match for codex agent")
	}
	// ntm's short names resolve: cc is claude.
	if !has(lib.Match("claude>", "cc"), "claude_prompt") {
		t.Error("expected claude_prompt match for cc")
	}
	if len(lib.Match("panic: error", "any")) == 0 {
		t.Error("wildcard patterns should match any agent")
	}
}

func TestPatternPriority(t *testing.T) {
	lib := NewPatternLibrary()
	for i := 1; i < len(lib.Patterns); i++ {
		if lib.Patterns[i-1].Priority < lib.Patterns[i].Priority {
			t.Errorf("patterns not sorted by priority at index %d", i)
		}
	}
}

func TestPatternLibraryErrorPatternVariants(t *testing.T) {
	lib := NewPatternLibrary()
	for _, text := range []string{
		"Rate limit exceeded", "rate limit", "HTTP/1.1 429 Too Many Requests", "HTTP 429", "too many requests",
		"quota exceeded", "api error: something went wrong", "Error: failed", "exception: null pointer",
		"panic: runtime error", "SIGSEGV", "Killed", "SIGKILL", "process exited", "agent terminated", "unauthorized",
		"invalid api key", "authentication failed", "connection refused", "request timed out", "connection timeout",
		"network error",
	} {
		if !lib.HasError(text, "*") {
			t.Errorf("expected error detection for %q", text)
		}
	}
	// Prose that merely mentions failure, and a bare status number, are not
	// operational errors (ntm#297, ntm#299).
	for _, text := range []string{
		"429",
		"6f764f0c7344e437767e61f189f493065dca56cadc7048610948be7a5d42963f",
		"it failed naming exactly the seven against the HEAD ci.yml before the edit,",
		"the planted negative failed as expected, and 19/19 switches discriminate after",
		"the review finding says cleanup catches BaseException",
		"the service status line reports a configured wait timeout 20000ms",
		"we should handle the diagnostics timeout in a follow-up",
		"scanned 429 files in the worktree",
	} {
		if lib.HasError(text, "*") {
			t.Errorf("prose must not be an operational error: %q matched %v", text, lib.MatchByCategory(text, "*", CategoryError))
		}
	}
}

func TestPatternLibraryIdlePromptVariants(t *testing.T) {
	lib := NewPatternLibrary()
	for prompt, a := range map[string]string{
		"claude>": "claude", "claude> ": "claude", "Claude>": "claude", "claude code>": "claude", "╰─>": "claude",
		"codex>": "codex", "Codex> ": "codex", "gemini>": "gemini", "Gemini> ": "gemini", ">>>": "gemini",
		">>> ": "gemini", "$": "*", "$ ": "user", ">": "*", "#": "*", "%": "*",
	} {
		if !lib.HasIdlePrompt(prompt, a) {
			t.Errorf("expected idle prompt detection for %q (agent: %s)", prompt, a)
		}
	}
	for prompt, a := range map[string]string{"$": "codex", "$ ": "claude", "#": "gemini"} {
		if lib.HasIdlePrompt(prompt, a) {
			t.Errorf("did not expect generic shell prompt %q to look idle for %s", prompt, a)
		}
	}
}

func TestPatternLibraryThinkingVariants(t *testing.T) {
	lib := NewPatternLibrary()
	for _, text := range []string{
		"Thinking...", "thinking", "Processing...", "processing", "Analyzing...", "analyzing", "Loading...",
		"loading", "please wait", "waiting...", "thinking deeply", "extended thinking", "...",
	} {
		if !lib.HasThinkingIndicator(text, "*") {
			t.Errorf("expected thinking detection for %q", text)
		}
	}
}

func TestPatternLibraryCompletionVariants(t *testing.T) {
	lib := NewPatternLibrary()
	for _, text := range []string{"Done", "done", "Done!", "Completed", "completed", "Finished", "Finished!", "Summary:", "Changes made:"} {
		if !lib.HasCompletionSignal(text, "*") {
			t.Errorf("expected completion detection for %q", text)
		}
	}
}
