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
	"github.com/arosl/nhm/internal/integrations/kinds"
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
// kind and why. A kind that the sandbox has not proved has "!" by default
// (nhm-gb0), and the refusal says so.
func TestSendRefusesAShellEscapeForEachKind(t *testing.T) {
	for _, kind := range []string{"claude", "codex", "pi"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", kind, herdr.StatusIdle)
			_, err := Send(ctx, w.env, base(Request{Message: "!touch /tmp/x && git reset --hard", OperationID: "op-" + kind}))
			refusedBeforeClaim(t, w, err, "op-"+kind, `the prompt for IcyKnoll (`+kind+`) starts with "!"`,
				`"!" is a shell escape of each agent's kind`, "no guard such as dcg screens it")
			_, err = Send(ctx, w.env, base(Request{Message: "!ls", DryRun: true}))
			refusedBeforeClaim(t, w, err, "")
		})
	}
	// gemini: a kind herdr starts that nhm has no adapter for.
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "gemini", herdr.StatusIdle)
	_, err := Send(context.Background(), w.env, base(Request{Message: "!ls", OperationID: "op-gemini"}))
	refusedBeforeClaim(t, w, err, "op-gemini", `the prompt for IcyKnoll (gemini) starts with "!"`,
		"the shell escape of every kind that the sandbox has not proved to have none")
	// omp runs a "$" line as Python too.
	w = newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "omp", herdr.StatusIdle)
	_, err = Send(context.Background(), w.env, base(Request{Message: "$ open('x', 'w')"}))
	refusedBeforeClaim(t, w, err, "", `the prompt for IcyKnoll (omp) starts with "$"`)
}

// grok runs no prompt as a shell command (the sandbox, nhm-gb0): it takes
// a "!" first and a control character, as typed. The check goes by the
// binding's kind, so a grok binding whose pane now holds a Claude agent is
// the delivery gate's to refuse (unsafe), and gets no prompt.
func TestSendToAKindWithoutAnEscape(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "grok", herdr.StatusIdle)
	for i, msg := range []string{"!ls", "a log\x1b[31m red\x1b[0m"} {
		res, err := Send(context.Background(), w.env, base(Request{Message: msg, WhenAny: true}))
		if err != nil || res.Targets[0].Status != StatusSubmitted || w.fake.prompts[i].text != msg {
			t.Fatalf("%q = %+v %v", msg, res, err)
		}
	}
	w = newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "grok", herdr.StatusIdle)
	a := w.fake.agents["w1:p1"]
	a.Agent = "claude"
	w.fake.agents["w1:p1"] = a
	res, err := Send(context.Background(), w.env, base(Request{Message: "!ls", WhenAny: true}))
	if err != nil || res.Targets[0].Status != StatusRefused || res.Targets[0].ErrorCode != "gate_unsafe" || w.fake.promptCount() != 0 {
		t.Fatalf("a grok binding holding claude = %+v %v", res, err)
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
// refused for a kind with a shell escape (omp's "!" here; nhm-gb0). Tab,
// newline and carriage return go out as typed.
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

// A target that would run the prompt refuses the whole send: nothing goes
// to the others either. The error names each such target, one sentence
// per reason: a proved escape, and the default one.
func TestSendShellEscapeRefusesTheWholeSend(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	w.bind(t, "BlueLake", "w1:t2", "w1:p2", "gemini", herdr.StatusIdle)
	w.bind(t, "RedStone", "w1:t3", "w1:p3", "pi", herdr.StatusIdle)
	w.bind(t, "GreenPike", "w1:t4", "w1:p4", "grok", herdr.StatusIdle)
	_, err := Send(context.Background(), w.env, base(Request{Message: "!ls", OperationID: "op-mixed"}))
	refusedBeforeClaim(t, w, err, "op-mixed", `the prompt for IcyKnoll (claude), RedStone (pi) starts with "!"`,
		`the prompt for BlueLake (gemini) starts with "!"`)
	if strings.Count(err.Error(), "the prompt for") != 2 || strings.Contains(err.Error(), "GreenPike") {
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

// A kind that the sandbox proved has no shell escape takes any prompt:
// one that starts with "!", and one with a control character (the
// operator's ruling, 2026-10-24; nhm-gb0). A kind with one is refused
// both.
func TestShellReasonOfAKindWithoutAnEscape(t *testing.T) {
	none := kinds.Adapter{Name: "proved", NoShellEscape: true}
	for _, msg := range []string{"!ls", "  !ls", "a\x1b[201~\r!ls", "\x1b[31mred\x1b[0m"} {
		if why := shellReason(msg, none); why != "" {
			t.Errorf("%q to a kind without an escape: %s", msg, why)
		}
		if why := shellReason(msg, kinds.Adapter{Name: "default"}); why == "" {
			t.Errorf("%q to a kind with the default escape: no refusal", msg)
		}
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
