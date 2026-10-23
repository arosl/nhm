package cli

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// replayWorld is a spawn --assign whose first run ended after the spawn
// and before the assignment (nhm-vcf): two agents up, with no prompt.
type replayWorld struct {
	s     *herdrtest.Server
	inst  store.Instance
	h     *sendHerdr
	args  []string
	first spawn.Result
}

// newReplayWorld runs the first run's spawn alone, with the request that
// the same command line gives, so its rerun replays it.
func newReplayWorld(t *testing.T) *replayWorld {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	s, inst, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	spawnAgentMail(t, "GreenPike", "RedFox")
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAll(t, filepath.Join(home, ".acfs/onboard/docs/ntm/command_palette.md"),
		"## A\n### default_new_agent | Default\nRead AGENTS.md.\n")
	project := assignTools(t)
	ownNhm(t, inst)
	w := &replayWorld{s: s, inst: inst, h: h, args: []string{"spawn", "swarm", "--cc=2", "--per-tab=1", "--project", project,
		"--allow-unguarded", "--assign", "--yes", "--init-prompt", "Then check your mail.", "--op-id", "sp-r1", "--socket", s.SocketPath}}
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := addSpawnFlags(fs)
	pos, err := parseInterleaved(fs, w.args[1:])
	if err != nil {
		t.Fatal(err)
	}
	req, err := f.request(pos[0], false)
	if err != nil {
		t.Fatal(err)
	}
	newHumanAssign(f, &req)
	ie, err := openInstance(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ie.db.Close()
	res, _, err := spawnUnderOwner(context.Background(), ie, &req, project)
	if err != nil || !res.Ready() || len(res.Targets) != 2 || len(h.sent()) != 0 {
		t.Fatalf("first run's spawn = %+v, %v, %q", res, err, h.sent())
	}
	w.first = res
	return w
}

// The kickoff and --init-prompt with no bead, as an agent without a bead
// gets them.
const replayKickoff = "Read AGENTS.md.\n\nThen check your mail."

// assertGatedKickoffs checks that each kickoff the spawn's receipts record
// went through the delivery gate, frozen at the binding's first
// generation (spawn.KickoffGated), as send recorded the request; want is
// how many there are.
func assertGatedKickoffs(t *testing.T, w *replayWorld, want int) {
	t.Helper()
	db, err := store.Open(w.inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	rows, err := db.SQL().QueryContext(ctx, `SELECT result_json FROM receipts WHERE operation_id = 'sp-r1' ORDER BY target_index`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var targets []spawn.Target
	for rows.Next() {
		var text string
		var tg spawn.Target
		if err := rows.Scan(&text); err != nil || json.Unmarshal([]byte(text), &tg) != nil {
			t.Fatalf("receipt: %v %q", err, text)
		}
		targets = append(targets, tg)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, tg := range targets {
		if tg.KickoffOp == "" {
			continue
		}
		got++
		op, err := db.Operation(ctx, tg.KickoffOp)
		if err != nil {
			t.Fatal(err)
		}
		var sent struct {
			WhenAny bool             `json:"when_any"`
			Expect  map[string]int64 `json:"expect"`
		}
		if err := json.Unmarshal([]byte(op.RequestJSON), &sent); err != nil {
			t.Fatal(err)
		}
		if sent.WhenAny || !reflect.DeepEqual(sent.Expect, map[string]int64{tg.BindingID: spawn.FirstGeneration}) {
			t.Fatalf("%s's kickoff went with when_any %v, expect %v", tg.MailName, sent.WhenAny, sent.Expect)
		}
	}
	if got != want {
		t.Fatalf("%d kickoffs recorded, want %d: %+v", got, want, targets)
	}
}

// A first run that reached none of its agents: the rerun assigns as the
// first run would have, one agent the bead with its kickoff, the other
// its kickoff. A rerun after that one finds every agent reached and sends
// nothing.
func TestSpawnAssignReplayAfterACrash(t *testing.T) {
	w := newReplayWorld(t)
	code, out, errOut := run(w.args...)
	if code != 0 || !strings.Contains(out, "operation sp-r1 was claimed before; nothing started") ||
		!strings.Contains(out, "none of its agents had a prompt; assigning now") || !strings.Contains(out, "✓ Successfully assigned 1 beads") {
		t.Fatalf("rerun = %d\n%s\n%s", code, out, errOut)
	}
	sent := w.h.sent()
	var worked, kicked int
	for _, p := range sent {
		switch {
		case strings.Contains(p, "Work on bead nhm-1"):
			worked++
		case strings.HasSuffix(p, replayKickoff):
			kicked++
		}
	}
	if len(sent) != 2 || worked != 1 || kicked != 1 {
		t.Fatalf("prompts = %q", sent)
	}
	assertGatedKickoffs(t, w, 1)
	code, out, _ = run(w.args...)
	if code != 0 || !strings.Contains(out, "nothing is assigned again") || len(w.h.sent()) != 2 {
		t.Fatalf("second rerun = %d %s %q", code, out, w.h.sent())
	}
}

// After a hand send to one agent, the rerun gives the other agent its
// kickoff alone: no bead is claimed or sent. The send's receipt says so,
// though the agent is done and ready again. An agent that herdr shows
// working, with no record of nhm's (a human typed into it), gets nothing
// either.
func TestSpawnAssignReplayKicksOffTheOthersOnly(t *testing.T) {
	w := newReplayWorld(t)
	green, red := w.first.Targets[0], w.first.Targets[1]
	if code, out, errOut := run("send", "swarm", "hello", "--name", green.MailName, "--socket", w.s.SocketPath); code != 0 {
		t.Fatalf("hand send = %d %s %s", code, out, errOut)
	}
	w.h.finish(green.PaneID)
	code, out, errOut := run(w.args...)
	if code != 0 || !strings.Contains(out, "1 of its agents had no prompt, and 1 got the kickoff now; no bead is assigned again") {
		t.Fatalf("rerun = %d\n%s\n%s", code, out, errOut)
	}
	sent := w.h.sent()
	if len(sent) != 2 || sent[0] != green.PaneID+"=hello" || !strings.HasPrefix(sent[1], red.PaneID+"=") ||
		!strings.HasSuffix(sent[1], replayKickoff) || strings.Contains(sent[1], "nhm-1") {
		t.Fatalf("prompts = %q", sent)
	}
	assertGatedKickoffs(t, w, 1)

	w2 := newReplayWorld(t)
	func() {
		w2.h.mu.Lock()
		defer w2.h.mu.Unlock()
		w2.h.status[w2.first.Targets[0].PaneID] = "working"
	}()
	code, out, errOut = run(w2.args...)
	sent = w2.h.sent()
	if code != 0 || len(sent) != 1 || !strings.HasPrefix(sent[0], w2.first.Targets[1].PaneID+"=") || !strings.HasSuffix(sent[0], replayKickoff) {
		t.Fatalf("a working agent: %d\n%s\n%s\n%q", code, out, errOut, sent)
	}
}

// A ledger row of any status that names an agent's binding counts as
// reached: the first run recorded a bead's intent for it, so it may have
// the bead. The other agent gets its kickoff alone.
func TestSpawnAssignReplayHonoursTheLedger(t *testing.T) {
	w := newReplayWorld(t)
	green := w.first.Targets[0]
	ledger, err := assignment.LoadStoreStrict(w.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := assignment.AtomicRequest{BeadID: "nhm-1", BeadTitle: "Fix the thing", Target: green.BindingID + "@1",
		OccupancyKey: green.BindingID, AgentType: "claude", AgentName: green.MailName, Actor: green.MailName,
		Prompt: "Work on bead nhm-1", IdempotencyKey: "replay-intent"}
	if _, err := ledger.RecordAtomicIntent(req, assignment.StableClaimActor(req.Actor, req.IdempotencyKey), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(w.args...)
	sent := w.h.sent()
	if code != 0 || !strings.Contains(out, "1 of its agents had no prompt") || len(sent) != 1 ||
		strings.HasPrefix(sent[0], green.PaneID+"=") || !strings.HasSuffix(sent[0], replayKickoff) {
		t.Fatalf("rerun = %d\n%s\n%s\n%q", code, out, errOut, sent)
	}
}

// While the first run still runs (it holds the operation's lock), a rerun
// refuses and sends nothing; once it ends, the rerun goes on.
func TestSpawnAssignReplayWaitsForTheFirstRun(t *testing.T) {
	w := newReplayWorld(t)
	unlock, err := store.LockOperation(w.inst.Dir, "sp-r1")
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(w.args...)
	if code != exitRefused || !strings.Contains(errOut, "operation sp-r1 still runs in another nhm process; nothing started") ||
		len(w.h.sent()) != 0 {
		t.Fatalf("rerun under the lock = %d\n%s\n%s", code, out, errOut)
	}
	unlock()
	if code, out, errOut = run(w.args...); code != 0 || len(w.h.sent()) != 2 {
		t.Fatalf("rerun after the lock = %d\n%s\n%s", code, out, errOut)
	}
}
