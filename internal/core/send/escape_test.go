package send

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/cm"
	"github.com/arosl/nhm/internal/robot"
)

// refusedBeforeClaim checks a shell-escape refusal: the error, no prompt,
// and the operation id left unclaimed.
func refusedBeforeClaim(t *testing.T, w *world, err error, op string, want ...string) {
	t.Helper()
	if !errors.Is(err, ErrShellEscape) {
		t.Fatalf("err = %v", err)
	}
	for _, s := range want {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("the refusal names no %q: %v", s, err)
		}
	}
	if w.fake.promptCount() != 0 {
		t.Fatalf("%d prompts", w.fake.promptCount())
	}
	if op != "" {
		if _, err := w.db.Operation(context.Background(), op); err == nil {
			t.Fatalf("the refused send claimed %s", op)
		}
	}
}

// r is the one-rune string of c: the tests spell invisible runes by code.
func r(c rune) string { return string(c) }

// Claude Code, Codex and pi run a prompt that starts with "!" as a shell
// command, with no approval and no hook (the sandbox, nhm-ubi): send
// refuses it before its claim, a dry run too, and names the agent, its
// kind and why. A kind whose escape nobody checked gets it.
func TestSendRefusesAShellEscapeForEachKind(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "pi"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", kind, herdr.StatusIdle)
			_, err := Send(ctx, w.env, base(Request{Message: "!touch /tmp/x && git reset --hard", OperationID: "op-" + kind}))
			refusedBeforeClaim(t, w, err, "op-"+kind, `the prompt for IcyKnoll (`+kind+`) starts with "!"`,
				"each agent's kind runs a prompt that starts with", "no hook that can stop it")
			_, err = Send(ctx, w.env, base(Request{Message: "!ls", DryRun: true}))
			refusedBeforeClaim(t, w, err, "")
		})
	}
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "omp", herdr.StatusIdle)
	res, err := Send(context.Background(), w.env, base(Request{Message: "!ls"}))
	if err != nil || res.Targets[0].Status != StatusSubmitted || w.fake.prompts[0].text != "!ls" {
		t.Fatalf("omp = %+v %v", res, err)
	}
}

// White space before the "!" does not count (pi and Codex skip it); a "!"
// on a later line, a fullwidth one (U+FF01), or one inside the text runs
// nothing and goes out as typed.
func TestSendShellEscapeForms(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "pi", herdr.StatusIdle)
	for _, msg := range []string{"  !ls", "\t!ls", "\n!ls", r(0xa0) + "!ls", r(0x200b) + "!ls", "!!ls"} {
		_, err := Send(ctx, w.env, base(Request{Message: msg}))
		refusedBeforeClaim(t, w, err, "", "IcyKnoll (pi)")
	}
	for i, msg := range []string{"hello\n!ls", r(0xff01) + "ls", "run `!ls` for me"} {
		res, err := Send(ctx, w.env, base(Request{Message: msg, WhenAny: true}))
		if err != nil || res.Targets[0].Status != StatusSubmitted || w.fake.prompts[i].text != msg {
			t.Fatalf("%q = %+v %v", msg, res, err)
		}
	}
	// --file without a template is the prompt, and is checked the same.
	_, err := Send(ctx, w.env, base(Request{FileContent: "!make\n", WhenAny: true}))
	if !errors.Is(err, ErrShellEscape) || w.fake.promptCount() != 3 {
		t.Fatalf("--file = %v, %d prompts", err, w.fake.promptCount())
	}
}

// A control character (the end of a bracketed paste, any ESC sequence)
// can let the text after it reach the agent as typed keys (nhm-ubi):
// refused for any kind, also one with no shell escape. Tab, newline and
// carriage return go out as typed.
func TestSendRefusesThePasteEnd(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "omp", herdr.StatusIdle)
	for _, msg := range []string{"hello\x1b[201~\r!ls", "\x1b[201~!ls", "a log\x1b[201~", "\x1b[31mred\x1b[0m", "a\x7fb", "a" + r(0x85) + "b"} {
		_, err := Send(context.Background(), w.env, base(Request{Message: msg, OperationID: "op-paste"}))
		refusedBeforeClaim(t, w, err, "op-paste", "the prompt for IcyKnoll (omp) holds the control character U+")
	}
	res, err := Send(context.Background(), w.env, base(Request{Message: "a\tb\r\nc"}))
	if err != nil || res.Targets[0].Status != StatusSubmitted {
		t.Fatalf("tab and line ends = %+v %v", res, err)
	}
}

// One target that would run the prompt refuses the whole send: nothing
// goes to the others either. The error names each such target, one
// sentence per reason.
func TestSendShellEscapeRefusesTheWholeSend(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "omp", herdr.StatusIdle)
	w.bind(t, "RedStone", "w1:t3", "w1:p3", "pi", herdr.StatusIdle)
	_, err := Send(context.Background(), w.env, base(Request{Message: "!ls", OperationID: "op-mixed"}))
	refusedBeforeClaim(t, w, err, "op-mixed", `the prompt for IcyKnoll (claude), RedStone (pi) starts with "!"`)
	if strings.Contains(err.Error(), "BlueLake") || strings.Count(err.Error(), "the prompt for") != 1 {
		t.Fatalf("the refusal = %v", err)
	}
}

// The check reads each target's rendered prompt: a template that renders
// a "!" first is refused; an enrichment block before the "!" makes the
// prompt one the agent does not run.
func TestSendShellEscapeReadsTheRenderedPrompt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "codex", herdr.StatusIdle)
	project := t.TempDir()
	writeFile(t, project+"/.nhm/templates/run.md", "---\nname: run\n---\n{{cmd}} now")
	_, err := Send(ctx, w.env, base(Request{Template: "run", Vars: map[string]string{"cmd": "!make"}, ProjectRoot: project}))
	refusedBeforeClaim(t, w, err, "", "IcyKnoll (codex)")

	w.env.CM = &memCM{rules: twoRules}
	res, err := Send(ctx, w.env, base(Request{Message: "!make", Enrich: Enrichment{CM: enrich.BestEffort}}))
	if err != nil || res.Targets[0].Status != StatusSubmitted || strings.HasPrefix(w.fake.prompts[0].text, "!") ||
		!strings.Contains(w.fake.prompts[0].text, "!make") {
		t.Fatalf("enriched = %+v %v, prompts %+v", res, err, w.fake.prompts)
	}
	// With no rule to put first, the prompt is the "!" again.
	w.env.CM = &memCM{}
	_, err = Send(ctx, w.env, base(Request{Message: "!make", WhenAny: true, Enrich: Enrichment{CM: enrich.BestEffort}}))
	if !errors.Is(err, ErrShellEscape) || w.fake.promptCount() != 1 {
		t.Fatalf("no rules = %v, %d prompts", err, w.fake.promptCount())
	}
	// A rule that holds terminal text cannot end the paste: the block
	// keeps no control character, so nhm's own text refuses no send.
	w.env.CM = &memCM{rules: []cm.Rule{{ID: "b-9", Content: "Read the log\x1b[201~\r!rm -rf x"}}}
	res, err = Send(ctx, w.env, base(Request{Message: "Fix the login bug.", WhenAny: true, Enrich: Enrichment{CM: enrich.BestEffort}}))
	if last := w.fake.prompts[len(w.fake.prompts)-1].text; err != nil || res.Targets[0].Status != StatusSubmitted ||
		strings.ContainsAny(last, "\x1b\r") || !strings.Contains(last, "Read the log [201~ !rm -rf x") {
		t.Fatalf("a rule with ESC = %+v %v, prompt %q", res, err, last)
	}
}

// The check goes by the binding's kind: a pane whose agent is now of
// another kind is the delivery gate's (unsafe), and gets no prompt.
func TestShellEscapeAndAChangedKind(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "omp", herdr.StatusIdle)
	a := w.fake.agents["w1:p1"]
	a.Agent = "pi"
	w.fake.agents["w1:p1"] = a
	res, err := Send(context.Background(), w.env, base(Request{Message: "!ls"}))
	if err != nil || res.Targets[0].Status != StatusRefused || res.Targets[0].ErrorCode != "gate_unsafe" || w.fake.promptCount() != 0 {
		t.Fatalf("changed kind = %+v %v", res, err)
	}
}

// The robot answer is ntm's for a send its guard refused: blocked, its
// code, a "guard" failure, every selected target and the preview.
func TestGuardOutput(t *testing.T) {
	err := refuseShell([]Target{{Binding: bindings.Binding{MailName: "IcyKnoll", Kind: "pi"}, Key: "0.1"},
		{Binding: bindings.Binding{MailName: "BlueLake", Kind: "omp"}, Key: "1.1"}}, []string{"!ls", "!ls"})
	out := GuardOutput(Result{Session: "swarm", Preview: "!ls", MemoryInjection: notInjected, CASSInjection: notInjected},
		err, true, "json", time.Now())
	if out.Success || !out.Blocked || out.ErrorCode != robot.ErrCodeDestructiveCommandBlocked || out.Session != "swarm" || !out.DryRun ||
		len(out.Failed) != 1 || out.Failed[0].Pane != "guard" || out.Failed[0].Error != err.Error() || out.Error != err.Error() ||
		strings.Join(out.Targets, ",") != "0.1,1.1" || out.MessagePreview != "!ls" || out.Successful == nil || out.Warnings == nil ||
		out.Hint == "" || out.MemoryInjection == nil || out.CASSInjection == nil {
		t.Fatalf("guard output = %+v", out)
	}
}
