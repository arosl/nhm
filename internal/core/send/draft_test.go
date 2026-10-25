package send

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
)

// drafts are screens that end with a "!" draft in the input box, as the
// sandbox saw them (nhm-mowp; the kinds' tests hold more), and the name
// that the refusal gives.
var drafts = map[string]struct{ screen, shows string }{
	"claude": {strings.Repeat("─", 40) + "\n!\n" + strings.Repeat("─", 40) + "\n  ! for shell mode\n", `Claude Code's bash mode (a "!" draft)`},
	"codex":  {"›  !\n\n  mock-model xhigh · ~/proj\n                Shell mode\n", `Codex's shell mode (a "!" draft)`},
	"pi":     {"Mock " + r(0xe0b0) + " ~/proj\n !\npi\n", `a "!" draft (pi shows no shell mode)`},
}

// paneReceipt is the receipt of the target in pane.
func paneReceipt(t *testing.T, res Result, pane string) Receipt {
	t.Helper()
	for _, rc := range res.Targets {
		if rc.PaneID == pane {
			return rc
		}
	}
	t.Fatalf("no receipt for %s: %+v", pane, res.Targets)
	return Receipt{}
}

// A "!" that a person left in the input box makes the prompt that herdr
// types after it a shell command (Claude Code, Codex and pi, the sandbox,
// nhm-gb0). Send reads the box under the target's lock, right before the
// prompt, and refuses that target with the reason; the others get theirs.
// A dry run reports it too (the operator's ruling, 2026-10-24; nhm-mowp).
func TestSendRefusesATargetWithAShellDraft(t *testing.T) {
	for kind, d := range drafts {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", kind, herdr.StatusIdle)
			w.bind(t, "BlueLake", "w1:t2", "w1:p2", kind, herdr.StatusIdle)
			w.fake.screens["w1:p1"] = d.screen
			dry, err := Send(ctx, w.env, base(Request{Message: "Review the plan.", DryRun: true}))
			if err != nil {
				t.Fatal(err)
			}
			if rc := paneReceipt(t, dry, "w1:p1"); rc.Status != StatusRefused || !strings.Contains(rc.Result.Reason, "the input box shows "+d.shows) {
				t.Fatalf("dry run = %+v", rc)
			}
			if rc := paneReceipt(t, dry, "w1:p2"); rc.Status != "would_send" {
				t.Fatalf("dry run, no draft = %+v", rc)
			}
			res, err := Send(ctx, w.env, base(Request{Message: "Review the plan.", OperationID: "op-" + kind}))
			if err != nil {
				t.Fatal(err)
			}
			rc := paneReceipt(t, res, "w1:p1")
			want := "the input box shows " + d.shows + ": herdr types nhm's text after what a person left there, so the agent would run it " +
				"as a shell command that no guard such as dcg screens; clear the input box in pane w1:p1"
			if rc.Status != StatusRefused || rc.ErrorCode != CodeShellDraft || rc.Result.Reason != want || rc.Baseline != nil {
				t.Fatalf("receipt = %+v", rc)
			}
			if rc := paneReceipt(t, res, "w1:p2"); rc.Status != StatusSubmitted {
				t.Fatalf("no draft = %+v", rc)
			}
			if w.fake.promptCount() != 1 || w.fake.prompts[0].pane != "w1:p2" {
				t.Fatalf("prompts = %+v", w.fake.prompts)
			}
		})
	}
}

// nhm types into no input box of such a kind that it cannot read.
func TestSendRefusesATargetWhoseBoxItCannotRead(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.fake.readErr["w1:p1"] = errors.New("read: connection reset")
	res, err := Send(context.Background(), w.env, base(Request{Message: "Review the plan."}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := paneReceipt(t, res, "w1:p1"); rc.Status != StatusRefused || rc.ErrorCode != CodeScreenUnread ||
		!strings.Contains(rc.Result.Reason, "nhm could not read the agent's input box (read: connection reset)") || w.fake.promptCount() != 0 {
		t.Fatalf("receipt = %+v, %d prompts", rc, w.fake.promptCount())
	}
}

// A kind whose screen the sandbox has not read has no rule: send reads no
// screen for it, and types its prompt (its Cannot list says so).
func TestSendReadsNoScreenOfAKindWithoutARule(t *testing.T) {
	for _, kind := range []string{"omp", "opencode", "agy", "grok", "gemini"} {
		w := newWorld(t)
		w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", kind, herdr.StatusIdle)
		w.fake.screens["w1:p1"] = "!\n"
		res, err := Send(context.Background(), w.env, base(Request{Message: "Review the plan."}))
		if err != nil {
			t.Fatal(err)
		}
		if rc := paneReceipt(t, res, "w1:p1"); rc.Status != StatusSubmitted || w.fake.reads != 0 {
			t.Fatalf("%s: %+v, %d reads", kind, rc, w.fake.reads)
		}
	}
}

// The draft check is the last before the prompt: a target that the gate
// refuses is not read.
func TestSendReadsTheBoxOfAnAdmittedTargetOnly(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusWorking)
	w.fake.screens["w1:p1"] = drafts["claude"].screen
	res, err := Send(context.Background(), w.env, base(Request{Message: "Review the plan."}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := paneReceipt(t, res, "w1:p1"); rc.Status != StatusRefused || rc.ErrorCode != "gate_"+string(GateBusy) || w.fake.reads != 0 {
		t.Fatalf("receipt = %+v, %d reads", rc, w.fake.reads)
	}
	// --when any admits the working agent, and the draft refuses it.
	res, err = Send(context.Background(), w.env, base(Request{Message: "Review the plan.", WhenAny: true}))
	if err != nil {
		t.Fatal(err)
	}
	if rc := paneReceipt(t, res, "w1:p1"); rc.ErrorCode != CodeShellDraft || w.fake.promptCount() != 0 {
		t.Fatalf("--when any = %+v", rc)
	}
}
