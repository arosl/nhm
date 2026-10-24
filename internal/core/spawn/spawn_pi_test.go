package spawn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/guards"
)

// pi takes a model (--model) and an effort (--thinking) (nhm-y7q). nhm
// adds no --yolo: pi asks before each tool call, and the operator decides
// whether a guarded pi gets it (nhm-dyr). The guard: TestSpawnPiLoadsNhmsGuard.
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
	// With no effort, pi gets none: ntm has no pi default.
	w = newWorld(t)
	if _, err := Spawn(context.Background(), w.env, req(Want{Kind: "pi", Count: 1})); err != nil {
		t.Fatal(err)
	}
	if calls := strings.Join(w.h.calls, "\n"); !strings.Contains(calls, "agent.start icyknoll pi w1:p1 []") {
		t.Fatalf("calls:\n%s", calls)
	}
}

// fakeDCG installs an executable dcg for guards.Check and returns a
// lookPath that finds it (and no rch).
func fakeDCG(t *testing.T) func(string) (string, error) {
	t.Helper()
	dcg := filepath.Join(t.TempDir(), "dcg")
	if err := os.WriteFile(dcg, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := guards.DCGPath
	guards.DCGPath = dcg
	t.Cleanup(func() { guards.DCGPath = old })
	return func(bin string) (string, error) {
		if bin == "dcg" {
			return dcg, nil
		}
		return "", errors.New("not found")
	}
}

// A spawned pi loads nhm's guard extension with -e and the policy that
// lets it run dcg, and its pane makes a failed hook block the call. The
// real guards check verifies the launch (nhm-dyr).
func TestSpawnPiLoadsNhmsGuard(t *testing.T) {
	w := newWorld(t)
	lookPath := fakeDCG(t)
	settings := LaunchSettings(lookPath, w.env.InstanceDir)
	ext := settings["pi"]
	if filepath.Dir(ext) != filepath.Join(w.env.InstanceDir, guards.PiExtensionDir) {
		t.Fatalf("settings = %v", settings)
	}
	check := func(kind string) []guards.Result { return guards.Check(kind, guards.Env{Home: t.TempDir()}) }
	w.env.Admission = AdmissionEnv{Guards: check, Settings: settings}
	res, err := Spawn(context.Background(), w.env, req(Want{Kind: "pi", Count: 1, Model: "gpt-5"}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ready() || res.Admission.Decision != Admit || len(res.Admission.Exceptions) != 0 {
		t.Fatalf("res = %+v", res)
	}
	calls := strings.Join(w.h.calls, "\n")
	if !strings.Contains(calls, "agent.start icyknoll pi w1:p1 [--model gpt-5 -e "+ext+" --extension-policy permissive]") ||
		strings.Contains(calls, "--yolo") {
		t.Fatalf("calls:\n%s", calls)
	}
	if env := w.h.envs["w1:p1"]; env["PI_EXTENSION_HOOKS_FAIL_CLOSED"] != "1" || env["NHM_AGENT"] != "IcyKnoll" {
		t.Fatalf("pane env = %v", env)
	}

	// A launch without the extension is not guarded: admission refuses it.
	ad := Decide(context.Background(), AdmissionEnv{Guards: check}, req(Want{Kind: "pi", Count: 1}))
	if ad.Decision != Refuse || !strings.Contains(ad.Reason, "does not load nhm's pi guard extension") {
		t.Fatalf("no extension = %+v", ad)
	}
	// Without dcg there is no extension; --allow-unguarded starts pi with
	// the exception recorded.
	guards.DCGPath = filepath.Join(t.TempDir(), "no-dcg")
	none := LaunchSettings(func(string) (string, error) { return "", errors.New("not found") }, w.env.InstanceDir)
	if _, ok := none["pi"]; ok {
		t.Fatalf("settings without dcg = %v", none)
	}
	ad = Decide(context.Background(), AdmissionEnv{Guards: check, Settings: none}, req(Want{Kind: "pi", Count: 1}))
	if ad.Decision != Refuse || !strings.Contains(ad.Reason, "pi guard dcg is missing: dcg is not installed") {
		t.Fatalf("no dcg = %+v", ad)
	}
	r := req(Want{Kind: "pi", Count: 1})
	r.AllowUnguarded = true
	if ad = Decide(context.Background(), AdmissionEnv{Guards: check, Settings: none}, r); ad.Decision != Admit || len(ad.Exceptions) != 1 {
		t.Fatalf("unguarded = %+v", ad)
	}
}
