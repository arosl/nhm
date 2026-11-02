package main

import (
	"testing"

	"github.com/arosl/nhm/internal/core/hooks"
	"github.com/arosl/nhm/internal/core/send"
)

// A hook's nudge does not wait for the turn it asks for (nhm-ew51): a
// send's wait does not fit the hook's budget. nhm maintain's nudges
// wait.
func TestHookNudgesDoNotWait(t *testing.T) {
	w, err := wakeEnv(hooks.Env{}, func(string) string { return "" }, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.PromptWait != 0 {
		t.Fatalf("PromptWait = %s, want none", w.PromptWait)
	}
	if send.PromptWait <= budget {
		t.Fatalf("send's wait %s fits the hook's budget %s: the hook's nudges could wait", send.PromptWait, budget)
	}
}
