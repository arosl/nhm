package spawn

import (
	"context"
	"strings"
	"testing"
)

// The kickoff's preamble tells the agent to register, when the prompt
// says so, with exactly the name and project key nhm gave it: for a
// [prompts] default (ACFS's palette text says only "register") and for a
// prompt of the caller's (nhm-lm4).
func TestKickoffSaysToRegisterWithThisNameAndKey(t *testing.T) {
	const rule = "When the prompt below says to register, register with exactly this name and project key."
	for _, prompt := range []string{"", "Fix the login bug."} {
		w := newWorld(t)
		r := req(Want{Kind: "claude", Count: 1})
		r.Prompt = prompt
		if prompt == "" {
			r.DefaultPrompts = map[string]string{"claude": "Register with Agent Mail, then read AGENTS.md."}
		}
		if _, err := Spawn(context.Background(), w.env, r); err != nil {
			t.Fatal(err)
		}
		if len(w.sent) != 1 {
			t.Fatalf("prompt %q: %d kickoffs", prompt, len(w.sent))
		}
		msg := w.sent[0].Message
		head, task, _ := strings.Cut(msg, "\n\n")
		if !strings.HasPrefix(head, "Your Agent Mail identity is already registered: name IcyKnoll, project key /p") ||
			!strings.Contains(head, rule) || strings.Contains(task, rule) {
			t.Errorf("prompt %q: kickoff = %q", prompt, msg)
		}
		if prompt != "" && task != prompt {
			t.Errorf("prompt %q: the task is %q", prompt, task)
		}
	}
}
