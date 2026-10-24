// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/agent_spec_test.go at commit 9d5ed977: the cases of
// TestParseAgentSpec_ModelValidation, TestParseAgentSpec_ReasoningEffort,
// TestParseAgentSpec_ValidFormats, TestParseAgentSpec_InvalidFormats,
// TestAgentSpecsValue_AgyRejectsModelOverride,
// TestParseAgentSpecEffortAtSuffix and
// TestEffortAtSuffixOnlyForEffortCapableTypes; see LICENSE in this
// directory and PROVENANCE.md. Local changes (nhm-2p6.4.1): this header;
// package and imports; the cases run through ParseCount with an nhm kind
// (claude for ntm's untyped ParseAgentSpec, which takes the @effort
// suffix; agy, opencode and codex for ntm's typed values), and one table
// per test.
package spawn

import (
	"strings"
	"testing"
)

func TestParseCountModelValidation(t *testing.T) {
	for _, tt := range []struct {
		value       string
		expectError bool
	}{
		{"1:claude-3-opus", false},
		{"2:gpt-4.1", false},
		{"1:vendor/model@2025", false},
		{"1:bad model", true},
		{"1:$(touch /tmp/pwn)", true},
		{"1:;rm -rf /", true},
	} {
		_, err := ParseCount("claude", tt.value)
		if tt.expectError && err == nil {
			t.Fatalf("expected error for %q", tt.value)
		}
		if !tt.expectError && err != nil {
			t.Fatalf("unexpected error for %q: %v", tt.value, err)
		}
	}
}

// ntm#140: the N:model:effort form threads a reasoning-effort hint
// through to the agent's launch.
func TestParseCountEffortForms(t *testing.T) {
	for _, tt := range []struct {
		value       string
		wantCount   int
		wantModel   string
		wantEffort  string
		expectError bool
	}{
		{"1", 1, "", "", false},
		{"2:gpt-5", 2, "gpt-5", "", false},
		{"1:gpt-5:medium", 1, "gpt-5", "medium", false},
		{"3:gpt-5-codex:xhigh", 3, "gpt-5-codex", "xhigh", false},
		{"1:gpt-5:", 1, "", "", true}, // empty effort segment rejected
		{"1:gpt-5:bad effort", 1, "", "", true},
		{"1:gpt-5:$(pwn)", 1, "", "", true},
		{"3", 3, "", "", false},
		{"2:opus-4.5", 2, "opus-4.5", "", false},
	} {
		w, err := ParseCount("codex", tt.value)
		if tt.expectError {
			if err == nil {
				t.Fatalf("expected error for %q; got %+v", tt.value, w)
			}
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", tt.value, err)
		}
		if w.Kind != "codex" || w.Count != tt.wantCount || w.Model != tt.wantModel || w.Effort != tt.wantEffort {
			t.Errorf("%q = %+v, want codex %d %q %q", tt.value, w, tt.wantCount, tt.wantModel, tt.wantEffort)
		}
	}
}

func TestParseCountInvalidFormats(t *testing.T) {
	for _, tt := range []struct{ value, want string }{
		{"", "invalid agent spec"},
		{"abc", "invalid count in agent spec"},
		{"0", "count must be at least 1, got 0"},
		{"-1", "count must be at least 1, got -1"},
		{"1:", "empty model in agent spec"},
		{" 2", "invalid count in agent spec"},
	} {
		if _, err := ParseCount("claude", tt.value); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ParseCount(%q) = %v, want %q", tt.value, err, tt.want)
		}
	}
	if _, err := ParseCount("nosuch", "1"); err == nil || !strings.Contains(err.Error(), `unknown kind "nosuch"`) {
		t.Errorf("an unknown kind = %v", err)
	}
}

// Antigravity is pinned by its launcher: a model would name a model that
// does not run (ntm GH#269).
func TestParseCountAgyRefusesModel(t *testing.T) {
	for _, value := range []string{"1:gemini-3.1-pro-high", "1:Gemini 3.8 Flash (High)"} {
		_, err := ParseCount("agy", value)
		want := `antigravity model overrides are not supported; model is pinned to "Gemini 3.8 Flash (High)" (use --agy=N)`
		if err == nil || err.Error() != want {
			t.Errorf("agy %q = %v, want %q", value, err, want)
		}
	}
	if _, err := ParseCount("agy", "1:$(x)"); err == nil || !strings.Contains(err.Error(), "invalid characters in model") {
		t.Errorf("agy with a shell model = %v", err)
	}
	w, err := ParseCount("agy", "2")
	if err != nil || w.Count != 2 || w.Model != "" {
		t.Fatalf("agy count only = %+v %v", w, err)
	}
}

func TestParseCountEffortAtSuffix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      string
		wantCount  int
		wantModel  string
		wantEffort string
		wantErr    string
	}{
		{name: "model with effort suffix", input: "2:gpt-5.6-terra@high", wantCount: 2, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "colon effort still works", input: "2:gpt-5.6-terra:high", wantCount: 2, wantModel: "gpt-5.6-terra", wantEffort: "high"},
		{name: "bare model unchanged", input: "3:opus", wantCount: 3, wantModel: "opus"},
		{name: "empty effort after at", input: "1:gpt-5.6-terra@", wantErr: "empty reasoning effort"},
		{name: "empty model before at", input: "1:@high", wantErr: "empty model"},
		{name: "both at and colon effort", input: "1:model@high:ultra", wantErr: "twice"},
		{name: "effort with slash rejected", input: "1:model@hi/gh", wantErr: "invalid characters in reasoning effort"},
		{name: "colon effort with slash rejected", input: "1:model:hi/gh", wantErr: "invalid characters in reasoning effort"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := ParseCount("claude", tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseCount(%q) = %+v %v, want an error with %q", tc.input, w, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCount(%q): %v", tc.input, err)
			}
			if w.Count != tc.wantCount || w.Model != tc.wantModel || w.Effort != tc.wantEffort {
				t.Errorf("ParseCount(%q) = %+v, want {%d %q %q}", tc.input, w, tc.wantCount, tc.wantModel, tc.wantEffort)
			}
		})
	}
}

// The @effort shorthand stays inert for a kind without an effort, so '@'
// remains a literal model character (e.g. opencode provider model tags).
func TestParseCountEffortAtSuffixOnlyForKindsWithAnEffort(t *testing.T) {
	w, err := ParseCount("opencode", "1:openrouter/some-model@beta")
	if err != nil || w.Model != "openrouter/some-model@beta" || w.Effort != "" {
		t.Fatalf("oc spec with a literal @ = %+v %v", w, err)
	}
	w, err = ParseCount("codex", "1:gpt-5.6-terra@high")
	if err != nil || w.Model != "gpt-5.6-terra" || w.Effort != "high" {
		t.Fatalf("cod spec with @effort = %+v %v", w, err)
	}
}
