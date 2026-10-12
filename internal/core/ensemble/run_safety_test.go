package ensemble

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Another process holding the run's lock makes synthesize refuse with
// ErrRunBusy, and nothing is sent (acfs-9hkh.69).
func TestSynthesizeRefusesABusyRun(t *testing.T) {
	f := &fakes{notReady: -1}
	env, r := readyRun(t, f)
	unlock, err := lockRun(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	sends := len(f.sent)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID}); !errors.Is(err, ErrRunBusy) {
		t.Fatalf("synthesize of a locked run: %v", err)
	}
	if len(f.sent) != sends {
		t.Fatal("a busy run's synthesis prompt went out")
	}
}

// Two runs never share a directory: a start whose run directory exists
// fails before it spawns, and the first run stays as it was.
func TestStartRefusesAnExistingRunDirectory(t *testing.T) {
	old := newRunID
	newRunID = func(time.Time) (string, error) { return "ens-20261012T060000-abcdef", nil }
	t.Cleanup(func() { newRunID = old })
	f := &fakes{notReady: -1}
	env := f.env(t)
	first := start(t, f, env)
	before, err := os.ReadFile(filepath.Join(first.Dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := Builtin()
	if _, err := Start(context.Background(), env, cat, StartRequest{Preset: "project-diagnosis", Question: "q", ProjectRoot: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "run directory") {
		t.Fatalf("a second start with the same id: %v", err)
	}
	if len(f.spawned) != 1 {
		t.Fatalf("spawns = %d", len(f.spawned))
	}
	if after, _ := os.ReadFile(filepath.Join(first.Dir, "run.json")); string(after) != string(before) {
		t.Fatal("the first run's run.json changed")
	}
}

// A file swapped in between the check (Lstat) and the open is refused.
func TestASwappedArtifactIsRefused(t *testing.T) {
	f := &fakes{notReady: -1}
	_, r := readyRun(t, f)
	target := r.Angles[0].Artifact
	other := target + ".other"
	if err := os.WriteFile(other, []byte(Marker(r.ID, 0, r.Angles[0].Mode)+"\nswapped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := afterLstat
	afterLstat = func(path string) {
		if path == target {
			if err := os.Rename(other, target); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { afterLstat = old })
	if _, reason := readArtifact(r, r.Angles[0]); reason != "it changed while it was checked" {
		t.Fatalf("swapped artifact: %q", reason)
	}
}

// A synthesis claim with no operation is taken again only once it is
// SynthesisClaimTimeout old: a synthesize died between its claim and its
// record. A claim with an operation never is.
func TestAStaleSynthesisClaimIsTakenAgain(t *testing.T) {
	f := &fakes{notReady: -1}
	env, r := readyRun(t, f)
	ctx := context.Background()
	claim := func(at time.Time, op string) {
		t.Helper()
		if _, err := update(ctx, env.RunsDir, r.ID, func(cur *Run) error {
			cur.Status, cur.SynthesisTo, cur.SynthesisClaimedAt, cur.SynthesisOp = RunSynthesizing, "IcyKnoll", &at, op
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sends := len(f.sent)
	claim(t0.Add(-time.Minute), "")
	if _, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID}); !errors.Is(err, ErrSynthesizing) {
		t.Fatalf("a fresh claim: %v", err)
	}
	claim(t0.Add(-SynthesisClaimTimeout), "op-sent")
	if _, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID}); !errors.Is(err, ErrSynthesizing) {
		t.Fatalf("an old claim with an operation: %v", err)
	}
	if len(f.sent) != sends {
		t.Fatal("a live claim was sent again")
	}
	claim(t0.Add(-SynthesisClaimTimeout), "")
	got, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID})
	if err != nil || len(f.sent) != sends+1 || got.SynthesisOp == "" || got.Status != RunSynthesizing {
		t.Fatalf("a stale claim: %+v %v, %d sends", got, err, len(f.sent)-sends)
	}
}

// The agents write inside the project (a sandboxed agent may), and what
// nhm checked stays in its state directory, which no agent is told to
// write (acfs-9hkh.69).
func TestResultsInTheProjectCheckedCopiesInState(t *testing.T) {
	f := &fakes{notReady: -1}
	env, r := readyRun(t, f)
	if r.ArtifactDir != filepath.Join(r.ProjectRoot, ".nhm", "ensemble", r.ID) {
		t.Fatalf("artifact dir = %q", r.ArtifactDir)
	}
	for _, a := range r.Angles {
		if filepath.Dir(a.Artifact) != r.ArtifactDir {
			t.Fatalf("angle %d writes to %s", a.Index, a.Artifact)
		}
	}
	if data, err := os.ReadFile(filepath.Join(r.ProjectRoot, ".nhm", "ensemble", ".gitignore")); err != nil || string(data) != "*\n" {
		t.Fatalf(".gitignore = %q %v", data, err)
	}
	got, err := Synthesize(context.Background(), env, SynthesizeRequest{RunID: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Synthesis != filepath.Join(r.ArtifactDir, "synthesis.md") {
		t.Fatalf("synthesis = %q", got.Synthesis)
	}
	for _, a := range r.Angles {
		if c := checkedFile(r, a); filepath.Dir(c) != r.Dir {
			t.Fatalf("checked copy %s is not in the state directory", c)
		} else if _, err := os.Stat(c); err != nil {
			t.Fatal(err)
		}
	}
	// A run without a project keeps everything in its own directory.
	cat, _ := Builtin()
	bare, err := Start(context.Background(), env, cat, StartRequest{Preset: "project-diagnosis", Question: "q"})
	if err != nil || bare.ArtifactDir != "" || bare.ResultsDir() != bare.Dir || filepath.Dir(bare.Angles[0].Artifact) != bare.Dir {
		t.Fatalf("a run without a project = %+v %v", bare, err)
	}
}
