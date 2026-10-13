package lifecycle

import (
	"context"
	"strings"
	"testing"
)

// The successor's kickoff opens with spawn's identity preamble: the name
// and key nhm registered, and the rule to register with exactly those
// when the prompt says to register (nhm-lm4).
func TestRecycleKickoffSaysToRegisterWithThisNameAndKey(t *testing.T) {
	w := newWorld(t)
	b := bindIn(t, w, "IcyKnoll", "w1:p1")
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Carry on."})
	if err != nil || res.Stage != StageDone || len(w.sent) != 1 {
		t.Fatalf("recycle = %+v %v, sent %d", res, err, len(w.sent))
	}
	msg := w.sent[0].Message
	if !strings.HasPrefix(msg, "Your Agent Mail identity is already registered: name IcyKnoll, project key "+b.MailProjectKey) ||
		!strings.Contains(msg, "When the prompt below says to register, register with exactly this name and project key.") ||
		!strings.HasSuffix(msg, "Carry on.") {
		t.Fatalf("kickoff = %q", msg)
	}
}
