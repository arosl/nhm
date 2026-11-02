package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// claudeScreen is Claude Code 2.1.316's screen with lines above its prompt
// box and footer below it, as the sandbox showed it (nhm-nng3).
func claudeScreen(above, footer string) string {
	rule := strings.Repeat("─", 60)
	return above + rule + "\n❯\n" + rule + "\n" + footer + "\n"
}

// waitingScreen is a Claude agent whose turn waits for its background
// agent: herdr reads it as working, and its input box is empty.
var waitingScreen = claudeScreen("● Agent(bg probe)\n  ⎿  Backgrounded agent (↓ to manage · ctrl+o to expand)\n\n"+
	"✻ Waiting for 1 background agent to finish\n\n\n", "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ↓ to manage\n\n  ● main\n  ◯ general-purpose  bg probe")

// An agent that herdr reads as working is refused at the check. When its
// turn only waits for background agents of its own (an empty input box,
// dive's SageMountain), the refusal names them and what ends the wait;
// when it works with one beside it, the refusal names that too. Nothing
// is claimed, typed or started.
func TestRecycleNamesTheBackgroundAgentsOfABusyAgent(t *testing.T) {
	for _, c := range []struct {
		name, status, screen, want string
	}{
		{"its turn waits for one", "working", waitingScreen,
			"IcyKnoll is working: its turn waits until 1 background agent of its own finishes; recycle it after that, " +
				"or interrupt it first (nhm interrupt <session> --name IcyKnoll), which stops that agent too"},
		{"its turn waits for two", "working", strings.Replace(waitingScreen, "Waiting for 1 background agent", "Waiting for 2 background agents", 1),
			"IcyKnoll is working: its turn waits until 2 background agents of its own finish; recycle it after that, " +
				"or interrupt it first (nhm interrupt <session> --name IcyKnoll), which stops those agents too"},
		{"it works with one", "working", claudeScreen("✶ Marinating… (17m 41s · ↓ 71.6k tokens)\n\n", "  ⏵⏵ bypass permissions on · ← 1 agent"),
			"IcyKnoll is working, and 1 background agent of its own runs; an agent is recycled only between turns (interrupt it first)"},
		{"it works", "working", claudeScreen("✶ Marinating… (2s)\n\n", "  ⏵⏵ bypass permissions on · esc to interrupt"),
			"IcyKnoll is working; an agent is recycled only between turns (interrupt it first)"},
		{"blocked at a dialog", "blocked", waitingScreen,
			"IcyKnoll is blocked; an agent is recycled only between turns (interrupt it first)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			b := bindKind(t, w, "claude")
			p, h := newDialogPane(t, "claude", backgroundWork, []string{"enter"})
			p.status, p.screen = c.status, c.screen
			w.env.Herdr = h
			res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
			if err == nil || res.Stage != StageCheck || res.Error != c.want || res.OperationID != "" {
				t.Fatalf("recycle = %+v %v", res, err)
			}
			if len(p.prompts) != 0 || len(p.keys) != 0 || p.starts != 0 || p.paneReads != 0 {
				t.Fatalf("prompts %v, keys %v, starts %d, pane reads %d", p.prompts, p.keys, p.starts, p.paneReads)
			}
			if nb, _ := w.env.Bindings.Get(context.Background(), b.ID); nb.Generation != b.Generation {
				t.Fatal("the generation moved")
			}
		})
	}
}

// A kind whose screen shows no background agents is refused without a
// read of its pane.
func TestRecycleReadsNoPaneOfABusyCodexAgent(t *testing.T) {
	w := newWorld(t)
	bindKind(t, w, "codex")
	p, h := newDialogPane(t, "codex", "", nil)
	p.status, p.screen = "working", waitingScreen
	w.env.Herdr = h
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	want := "IcyKnoll is working; an agent is recycled only between turns (interrupt it first)"
	if err == nil || res.Stage != StageCheck || res.Error != want || p.reads+p.paneReads != 0 {
		t.Fatalf("recycle = %+v %v, reads %d, pane reads %d", res, err, p.reads, p.paneReads)
	}
}

// recycle --self waits for the agent's turn to end; a turn that waits for
// a background agent past --wait fails with the background agent named,
// both when herdr's wait times out and when it answers with the state.
func TestRecycleSelfNamesTheBackgroundAgentItsTurnWaitsFor(t *testing.T) {
	for _, c := range []struct {
		name    string
		timeout bool
		want    string
	}{
		{"herdr's wait times out", true, "IcyKnoll did not end its turn within 1s (its turn waits until 1 background agent of its own finishes): " +
			"herdr agent.wait: timeout: agent did not reach the state"},
		{"herdr answers working", false, "IcyKnoll did not end its turn within 1s (it is working; its turn waits until 1 background agent of its own finishes)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			bindKind(t, w, "claude")
			p, h := newDialogPane(t, "claude", backgroundWork, []string{"enter"})
			p.status, p.screen, p.waitTimeout = "working", waitingScreen, c.timeout
			w.env.Herdr = h
			res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x", WaitTurn: time.Second})
			if err == nil || res.Stage != StageCheck || res.Error != c.want || p.starts != 0 {
				t.Fatalf("recycle = %+v %v, starts %d", res, err, p.starts)
			}
		})
	}
}

// On the host's herdr 0.9.3, agent.read failed while /exit's dialog was
// up (dive, 2026-10-31). Recycle reads the pane with pane.read then: it
// sees the dialog, answers it, and the successor starts.
func TestRecycleReadsTheDialogWhenAgentReadFails(t *testing.T) {
	w := newWorld(t)
	b := bindKind(t, w, "claude")
	p, h := newDialogPane(t, "claude", backgroundWork, []string{"enter"})
	p.exitReadErr = true
	w.env.Herdr = h
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if fmt.Sprint(p.prompts, p.keys, p.starts) != "[/exit] [[enter]] 1" || p.paneReads == 0 {
		t.Fatalf("prompts %v, keys %v, starts %d, pane reads %d", p.prompts, p.keys, p.starts, p.paneReads)
	}
}

// When neither read works, the stop fails at its deadline with herdr's
// errors, and says that a dialog may be open: not that the pane is as it
// was. No key is sent and nothing starts.
func TestRecycleNamesTheReadErrorsOfAStopThatTimesOut(t *testing.T) {
	w := newWorld(t)
	b := bindKind(t, w, "claude")
	p, h := newDialogPane(t, "claude", backgroundWork, []string{"enter"})
	p.exitReadErr, p.paneReadErr = true, true
	w.env.Herdr = h
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	want := "IcyKnoll did not exit within 30s; nothing started. herdr could not read pane w1:p1 " +
		"(herdr agent.read: internal: read failed; herdr pane.read: internal: pane read failed), " +
		"so recycle could not see what holds the exit: look at the pane"
	if err == nil || res.Stage != StageStop || res.Error != want {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(p.keys) != 0 || p.starts != 0 {
		t.Fatalf("keys %v, starts %d", p.keys, p.starts)
	}
	if nb, _ := w.env.Bindings.Get(context.Background(), b.ID); nb.Generation != b.Generation {
		t.Fatal("the generation moved")
	}
}
