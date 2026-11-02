package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// omp has no exit command: recycle stops it with ntm's exit keys
// (SendOmpExitContext): Escape, 500 ms, Ctrl+C, 1.2 s, Ctrl+D, and types
// no text, so a draft in omp's input box cannot turn the exit into a
// shell command. The successor starts in the same pane (nhm-dv41).
func TestRecycleStopsAnOmpWithNtmsExitKeys(t *testing.T) {
	w := newWorld(t)
	b := bindKind(t, w, "omp")
	// The stub's omp quits on Ctrl+D at its empty composer.
	p, c := newDialogPane(t, "omp", "❯ \n", []string{"ctrl+d"})
	w.env.Herdr = c
	var sleeps []time.Duration
	w.env.Sleep = func(_ context.Context, d time.Duration) {
		sleeps = append(sleeps, d)
		w.clock = w.clock.Add(d)
	}
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Continue the bead."})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if fmt.Sprint(p.prompts, p.keys, p.starts) != "[] [[esc] [ctrl+c] [ctrl+d]] 1" || len(w.sent) != 1 {
		t.Fatalf("prompts %v, keys %v, starts %d, kickoffs %d", p.prompts, p.keys, p.starts, len(w.sent))
	}
	if len(sleeps) < 2 || sleeps[0] != 500*time.Millisecond || sleeps[1] != 1200*time.Millisecond {
		t.Fatalf("pauses = %v", sleeps)
	}
}

// An omp that is back at its shell before the last key gets no more keys:
// a Ctrl+D that reached the shell would end it and close the pane.
func TestRecycleSendsNoExitKeyToTheShell(t *testing.T) {
	w := newWorld(t)
	b := bindKind(t, w, "omp")
	p, c := newDialogPane(t, "omp", "❯ \n", []string{"ctrl+c"})
	w.env.Herdr = c
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if fmt.Sprint(p.keys) != "[[esc] [ctrl+c]]" || p.starts != 1 {
		t.Fatalf("keys %v, starts %d", p.keys, p.starts)
	}
}

// An omp that does not exit fails the stop at its deadline; each key went
// once, and nothing starts.
func TestRecycleOfAnOmpThatStays(t *testing.T) {
	w := newWorld(t)
	b := bindKind(t, w, "omp")
	p, c := newDialogPane(t, "omp", "❯ \n", nil)
	w.env.Herdr = c
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err == nil || res.Stage != StageStop || res.Error != "IcyKnoll did not exit within 30s; nothing started, the pane is as it was" {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if fmt.Sprint(p.prompts, p.keys, p.starts) != "[] [[esc] [ctrl+c] [ctrl+d]] 0" {
		t.Fatalf("prompts %v, keys %v, starts %d", p.prompts, p.keys, p.starts)
	}
	if nb, _ := w.env.Bindings.Get(context.Background(), b.ID); nb.Generation != b.Generation {
		t.Fatal("the generation moved")
	}
}

// A kind with neither an exit command nor ntm's exit keys (here one with
// no adapter) is refused before anything is touched.
func TestRecycleRefusesAKindWithNoExit(t *testing.T) {
	w := newWorld(t)
	bindKind(t, w, "weird")
	p, c := newDialogPane(t, "weird", "", nil)
	w.env.Herdr = c
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err == nil || res.Stage != StageCheck || !strings.Contains(res.Error, "nhm knows no exit command for weird") {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(p.prompts) != 0 || len(p.keys) != 0 || p.starts != 0 || res.OperationID != "" {
		t.Fatalf("prompts %v, keys %v, starts %d, operation %q", p.prompts, p.keys, p.starts, res.OperationID)
	}
}
