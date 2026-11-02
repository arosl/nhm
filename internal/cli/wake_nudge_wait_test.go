package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// nhm wake's nudge waits as nhm send does, until herdr sees the turn
// start (nhm-ew51). A nudge that starts none is possibly submitted: the
// pass says so, lists it under "possibly submitted, not repeated", and
// the next pass does not nudge again.
func TestWakeNudgeWaitsAndReportsNoTurn(t *testing.T) {
	s, inst, _ := newSendInstance(t)
	mail := newFakeAgentMail(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	ownNhm(t, inst)
	mail.set("IcyKnoll", msg(7, "SageOak", "acfs-1", "to"))
	if code, out, errOut := runVerb(runWake, "--recipients", "IcyKnoll"); code != exitOK ||
		!strings.Contains(out, "IcyKnoll (w1:p1): nudged (submitted) for #7") {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	var prompts []map[string]any
	for _, r := range s.Requests() {
		if r.Method == "agent.prompt" {
			var p map[string]any
			_ = json.Unmarshal(r.Params, &p)
			prompts = append(prompts, p)
		}
	}
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v", prompts)
	}
	wait, _ := json.Marshal(prompts[0]["wait"])
	want := `{"timeout_ms":` + jsonNumber(send.PromptWait.Milliseconds()) + `,"until":["working","blocked"]}`
	if string(wait) != want {
		t.Fatalf("wait = %s, want %s", wait, want)
	}

	// herdr took the text and its Enter, and saw no turn start.
	s.Handle("agent.prompt", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "agent_prompt_stalled", Message: "agent did not reach working or blocked within 5000ms"}
	})
	mail.set("BlueLake", msg(8, "SageOak", "acfs-1", "to"))
	code, out, errOut := runVerb(runWake, "--recipients", "BlueLake")
	if code != exitOK ||
		!strings.Contains(out, "BlueLake (w1:p2): nudged (possibly submitted) for #8: agent_prompt_stalled") ||
		!strings.Contains(out, "herdr saw the agent start no turn: the text may be left in the input box of pane w1:p2") ||
		!strings.Contains(out, "possibly submitted, not repeated: BlueLake at ") ||
		!strings.Contains(errOut, "nhm wake: warning: BlueLake (w1:p2): the nudge is possibly submitted and is not repeated: agent_prompt_stalled") {
		t.Fatalf("stalled: exit %d: %s %s", code, out, errOut)
	}
	code, out, _ = runVerb(runWake, "--recipients", "BlueLake")
	if code != exitOK || !strings.Contains(out, "no pending mail") || !strings.Contains(out, "possibly submitted, not repeated: BlueLake") {
		t.Fatalf("second pass: exit %d: %s", code, out)
	}
	n := 0
	for _, r := range s.Requests() {
		if r.Method == "agent.prompt" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("agent.prompt calls = %d, want 2", n)
	}
}
