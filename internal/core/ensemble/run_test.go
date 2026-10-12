package ensemble

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
)

var t0 = time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)

// fakes records what a run asked of spawn and send.
type fakes struct {
	spawned []spawn.Request
	sent    []send.Request
	// notReady makes the spawn report this angle's agent not ready.
	notReady int
}

func (f *fakes) env(t *testing.T) RunEnv {
	return RunEnv{RunsDir: filepath.Join(t.TempDir(), "runs"), Now: func() time.Time { return t0 },
		Spawn: func(_ context.Context, req spawn.Request) (spawn.Result, error) {
			f.spawned = append(f.spawned, req)
			res := spawn.Result{OperationID: "op-spawn", WorkspaceID: "w7"}
			for i := 0; i < req.Agents[0].Count; i++ {
				t := spawn.Target{Index: i, Kind: req.Agents[0].Kind, MailName: []string{"IcyKnoll", "BlueLake", "RedStone", "GreenCastle", "PinkFox", "TealHorizon"}[i],
					PaneID: "w7:p" + string(rune('1'+i)), Ready: true, Status: "ready"}
				if i == f.notReady {
					t.Ready, t.Status, t.Error = false, "unresolved", "agent.start timed out"
				}
				res.Targets = append(res.Targets, t)
			}
			return res, nil
		},
		Send: func(_ context.Context, req send.Request) (send.Result, error) {
			f.sent = append(f.sent, req)
			return send.Result{OperationID: "op-send-" + req.Selector.Names[0],
				Targets: []send.Receipt{{MailName: req.Selector.Names[0], Status: "submitted"}}}, nil
		}}
}

func start(t *testing.T, f *fakes, env RunEnv) Run {
	t.Helper()
	cat, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Start(context.Background(), env, cat, StartRequest{Preset: "project-diagnosis", Question: "Is the cache safe?", ProjectRoot: "/p"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStartSpawnsAGroupAndPromptsEachAngle(t *testing.T) {
	f := &fakes{notReady: -1}
	env := f.env(t)
	r := start(t, f, env)
	if len(f.spawned) != 1 {
		t.Fatalf("spawns = %d", len(f.spawned))
	}
	sp := f.spawned[0]
	if sp.Group != r.ID || sp.Session != r.ID || !sp.NoKickoff || sp.Agents[0].Count != 5 || sp.Agents[0].Kind != "claude" {
		t.Fatalf("spawn request = %+v", sp)
	}
	if r.Status != RunRunning || r.WorkspaceID != "w7" || r.SpawnOp != "op-spawn" || len(r.Angles) != 5 || len(f.sent) != 5 {
		t.Fatalf("run = %+v, %d sends", r, len(f.sent))
	}
	for i, a := range r.Angles {
		msg := f.sent[i].Message
		if f.sent[i].Selector.Names[0] != a.MailName || !strings.Contains(msg, a.Artifact) || !strings.Contains(msg, Marker(r.ID, i, a.Mode)) ||
			!strings.Contains(msg, "Is the cache safe?") || a.PromptStatus != "submitted" {
			t.Fatalf("angle %d: %+v, prompt %q", i, a, msg)
		}
	}
	got, err := Load(env.RunsDir, r.ID)
	if err != nil || got.Angles[2].Mode != "dependency-mapping" || got.Status != RunRunning {
		t.Fatalf("Load = %+v %v", got, err)
	}
	if info, err := os.Stat(r.Dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("run dir mode = %v %v", info, err)
	}
}

func TestStartRecordsAnAngleWhoseAgentFailed(t *testing.T) {
	f := &fakes{notReady: 1}
	r := start(t, f, f.env(t))
	if !strings.Contains(r.Angles[1].Error, "agent.start timed out") || r.Angles[1].PromptOp != "" || len(f.sent) != 4 {
		t.Fatalf("angle 1 = %+v, %d sends", r.Angles[1], len(f.sent))
	}
}

func writeArtifact(t *testing.T, r Run, i int, first string) {
	t.Helper()
	if err := os.WriteFile(r.Angles[i].Artifact, []byte(first+"\n\nfindings\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSynthesizeChecksEveryArtifact(t *testing.T) {
	f := &fakes{notReady: -1}
	env := f.env(t)
	r := start(t, f, env)
	ctx := context.Background()
	for i := range r.Angles {
		if i != 3 {
			writeArtifact(t, r, i, Marker(r.ID, i, r.Angles[i].Mode))
		}
	}
	// Angle 3 is missing: refused, nothing sent.
	sends := len(f.sent)
	if _, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID}); !errors.Is(err, ErrMissingAngles) || !strings.Contains(err.Error(), "angle 3") {
		t.Fatalf("missing angle: %v", err)
	}
	// An artifact with another angle's marker does not count.
	writeArtifact(t, r, 3, Marker(r.ID, 2, r.Angles[2].Mode))
	if _, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID}); !errors.Is(err, ErrMissingAngles) || !strings.Contains(err.Error(), "marker names") {
		t.Fatalf("wrong marker: %v", err)
	}
	// A symlink does not count either.
	if err := os.Remove(r.Angles[3].Artifact); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(r.Angles[2].Artifact, r.Angles[3].Artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID}); !errors.Is(err, ErrMissingAngles) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink: %v", err)
	}
	if len(f.sent) != sends {
		t.Fatalf("a refused synthesis sent %d prompts", len(f.sent)-sends)
	}
	// --partial records the omission and sends to the first valid angle.
	got, err := Synthesize(ctx, env, SynthesizeRequest{RunID: r.ID, Partial: true})
	if err != nil || len(got.Omitted) != 1 || got.Omitted[0] != 3 || got.SynthesisTo != "IcyKnoll" || got.Status != RunSynthesizing {
		t.Fatalf("partial = %+v %v", got, err)
	}
	last := f.sent[len(f.sent)-1].Message
	if !strings.Contains(last, r.Angles[0].Artifact) || strings.Contains(last, r.Angles[3].Artifact) || !strings.Contains(last, "left out: [3]") {
		t.Fatalf("synthesis prompt = %q", last)
	}
}

func TestSynthesizeAllAnglesToAChosenAgent(t *testing.T) {
	f := &fakes{notReady: -1}
	env := f.env(t)
	r := start(t, f, env)
	for i := range r.Angles {
		writeArtifact(t, r, i, Marker(r.ID, i, r.Angles[i].Mode))
	}
	got, err := Synthesize(context.Background(), env, SynthesizeRequest{RunID: r.ID, To: "RedStone"})
	if err != nil || len(got.Omitted) != 0 || got.SynthesisTo != "RedStone" || got.SynthesisOp != "op-send-RedStone" {
		t.Fatalf("synthesize = %+v %v", got, err)
	}
	states := Artifacts(got)
	for _, s := range states {
		if !s.Valid {
			t.Fatalf("artifact %+v", s)
		}
	}
}

func TestStopKeepsArtifactsAndRefusesSynthesis(t *testing.T) {
	f := &fakes{notReady: -1}
	env := f.env(t)
	r := start(t, f, env)
	writeArtifact(t, r, 0, Marker(r.ID, 0, r.Angles[0].Mode))
	got, err := Stop(env, r.ID)
	if err != nil || got.Status != RunStopped || got.StoppedAt == nil {
		t.Fatalf("stop = %+v %v", got, err)
	}
	if _, err := os.Stat(r.Angles[0].Artifact); err != nil {
		t.Fatalf("artifact gone after stop: %v", err)
	}
	if _, err := Synthesize(context.Background(), env, SynthesizeRequest{RunID: r.ID, Partial: true}); !errors.Is(err, ErrRunStopped) {
		t.Fatalf("synthesize after stop: %v", err)
	}
	runs, err := List(env.RunsDir)
	if err != nil || len(runs) != 1 || runs[0].ID != r.ID {
		t.Fatalf("list = %+v %v", runs, err)
	}
	if _, err := Load(env.RunsDir, "../etc"); !errors.Is(err, ErrNoRun) {
		t.Fatalf("a bad run id: %v", err)
	}
}

func TestStartNeedsAQuestionAndAKnownPreset(t *testing.T) {
	f := &fakes{notReady: -1}
	cat, _ := Builtin()
	if _, err := Start(context.Background(), f.env(t), cat, StartRequest{Preset: "project-diagnosis"}); !errors.Is(err, ErrNoQuestion) {
		t.Fatalf("no question: %v", err)
	}
	if _, err := Start(context.Background(), f.env(t), cat, StartRequest{Preset: "nope", Question: "q"}); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("unknown preset: %v", err)
	}
	if len(f.spawned) != 0 {
		t.Fatal("a refused start spawned")
	}
}
