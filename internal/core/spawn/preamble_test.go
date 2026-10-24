package spawn

import (
	"context"
	"testing"
)

// The kickoff is the prompt alone, as ntm's: no identity preamble, for a
// [prompts] default and for a prompt of the caller's. The agent's name is
// in its identity file only (the operator, 2026-10-24; nhm-2p6.4.6).
func TestKickoffIsThePromptAlone(t *testing.T) {
	for _, prompt := range []string{"", "Fix the login bug."} {
		w := newWorld(t)
		r := req(Want{Kind: "claude", Count: 1})
		r.Prompt = prompt
		want := prompt
		if prompt == "" {
			want = "Register with Agent Mail, then read AGENTS.md."
			r.DefaultPrompts = map[string]string{"claude": want}
		}
		if _, err := Spawn(context.Background(), w.env, r); err != nil {
			t.Fatal(err)
		}
		if len(w.sent) != 1 || w.sent[0].Message != want {
			t.Fatalf("prompt %q: kickoffs = %+v", prompt, w.sent)
		}
		if len(w.idFiles) != 1 || w.idFiles[0] != "w1:p1=IcyKnoll" {
			t.Fatalf("prompt %q: identity files = %v", prompt, w.idFiles)
		}
	}
}
