package spawnassign

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
)

// A Preamble names each occupant ahead of its own work prompt, before the
// initial prompt, and the ledger records that exact prompt: the human
// spawn --assign's identity preamble (nhm-cvw). Without a Preamble the
// work prompt is as before.
func TestAssignPreambleNamesEachOccupant(t *testing.T) {
	isolate(t)
	w := newWorld(t)
	w.bind("IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind("BlueLake", "w1:t2", "w1:p2", "codex", herdr.StatusIdle)
	deps := w.deps()
	deps.Preamble = func(o Occupant) string { return "You are " + o.MailName + "." }
	out := &SpawnOutput{RobotResponse: robot.NewResponse(true, "json", time.Now()), Agents: w.agents}
	Assign(context.Background(), out, planOf("bd-1", "bd-2"), "inst-000000000000", "Read AGENTS.md first.", deps)
	if !out.Success {
		t.Fatalf("out=%+v", out)
	}
	for pane, name := range map[string]string{"w1:p1": "IcyKnoll", "w1:p2": "BlueLake"} {
		if w.herdr.promptCount(pane) != 1 ||
			!strings.HasPrefix(w.herdr.prompts[pane][0], "You are "+name+".\n\nRead AGENTS.md first.\n\nWork on bead bd-") {
			t.Fatalf("pane %s prompts=%v", pane, w.herdr.prompts[pane])
		}
	}
	s := ledger(t)
	for _, id := range []string{"bd-1", "bd-2"} {
		if row := s.Get(id); row == nil || !strings.HasPrefix(row.PromptSent, "You are ") {
			t.Fatalf("ledger row %s = %+v", id, row)
		}
	}
}

// occupantInitialPrompt leaves out an empty part, so no blank lines lead.
func TestOccupantInitialPrompt(t *testing.T) {
	o := Occupant{MailName: "IcyKnoll"}
	name := func(o Occupant) string { return "You are " + o.MailName + "." }
	for _, c := range []struct {
		pre     func(Occupant) string
		initial string
		want    string
	}{
		{nil, "Read.", "Read."},
		{name, "Read.", "You are IcyKnoll.\n\nRead."},
		{name, "", "You are IcyKnoll."},
		{func(Occupant) string { return " " }, "Read.", "Read."},
	} {
		if got := occupantInitialPrompt(SpawnAssignmentDependencies{Preamble: c.pre}, o, c.initial); got != c.want {
			t.Errorf("occupantInitialPrompt(%q) = %q, want %q", c.initial, got, c.want)
		}
	}
}
