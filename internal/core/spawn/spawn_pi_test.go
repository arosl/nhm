package spawn

import (
	"context"
	"strings"
	"testing"
)

// pi takes a model (--model) and an effort (--thinking). nhm adds no
// --yolo: with no guard for pi, pi asks before each tool call, and the
// admission warns that nothing guards it (nhm-y7q).
func TestSpawnPiTakesModelAndThinking(t *testing.T) {
	w := newWorld(t)
	res, err := Spawn(context.Background(), w.env, req(Want{Kind: "pi", Count: 1, Model: "gpt-5", Effort: "high"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready() || len(res.Targets) != 1 {
		t.Fatalf("res = %+v", res)
	}
	calls := strings.Join(w.h.calls, "\n")
	if !strings.Contains(calls, "agent.start icyknoll pi w1:p1 [--model gpt-5 --thinking high]") || strings.Contains(calls, "--yolo") {
		t.Fatalf("calls:\n%s", calls)
	}
	if !strings.Contains(strings.Join(res.Admission.Warnings, ";"), "pi has no guard that nhm sets up") {
		t.Fatalf("warnings = %v", res.Admission.Warnings)
	}
	// With no effort, pi gets none: ntm has no pi default.
	w = newWorld(t)
	if _, err := Spawn(context.Background(), w.env, req(Want{Kind: "pi", Count: 1})); err != nil {
		t.Fatal(err)
	}
	if calls := strings.Join(w.h.calls, "\n"); !strings.Contains(calls, "agent.start icyknoll pi w1:p1 []") {
		t.Fatalf("calls:\n%s", calls)
	}
}
