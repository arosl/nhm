package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

func runVerb(fn func([]string, io.Writer, io.Writer) int, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := fn(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// waitInstance serves workspace w1 ("swarm") with IcyKnoll (claude, in
// w1:p1) and BlueLake (codex, in w1:p2), both bound. agentFor answers
// agent.get.
func waitInstance(t *testing.T, agentFor func(pane string) map[string]any) (*herdrtest.Server, store.Instance, []bindings.Binding) {
	t.Helper()
	s, inst := cliInstance(t)
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": []any{},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 2, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		a := agentFor(p.Target)
		if a == nil {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "no agent"}
		}
		return map[string]any{"type": "agent_info", "agent": a}, nil
	})
	s.Handle("events.subscribe", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "subscription_started"}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	var out []bindings.Binding
	for _, n := range []struct{ name, tab, pane, kind string }{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude"}, {"BlueLake", "w1:t2", "w1:p2", "codex"},
	} {
		b, err := bs.Create(context.Background(), bindings.New{MailName: n.name, MailProjectKey: "/p", ProjectID: "proj",
			Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: n.tab, PaneID: n.pane,
				HerdrAlias: strings.ToLower(n.name), Kind: n.kind}})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return s, inst, out
}

func agentMap(pane, kind, status string, seq, completion int) map[string]any {
	a := map[string]any{"pane_id": pane, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t-" + pane,
		"agent": kind, "agent_status": status, "focused": false, "revision": 1, "state_change_seq": seq}
	if completion > 0 {
		a["completion_seq"] = completion
	}
	return a
}

func TestWaitVerbStateAndSelection(t *testing.T) {
	s, _, _ := waitInstance(t, func(pane string) map[string]any {
		if pane == "w1:p1" {
			return agentMap(pane, "claude", "idle", 4, 4)
		}
		return agentMap(pane, "codex", "working", 7, 0)
	})
	s.Handle("agent.wait", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "timeout", Message: "timed out"}
	})
	code, out, errOut := runVerb(runWait, "swarm", "--name", "IcyKnoll", "--until", "idle", "--socket", s.SocketPath, "--json")
	if code != 0 {
		t.Fatalf("wait = %d %s %s", code, out, errOut)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	targets := res["targets"].([]any)
	if res["met"] != true || res["session"] != "w1" || len(targets) != 1 || targets[0].(map[string]any)["outcome"] != "idle" {
		t.Fatalf("result = %v", res)
	}
	// All agents: BlueLake stays working, so idle times out with exit 1.
	code, out, _ = runVerb(runWait, "w1", "--until", "idle", "--timeout", "100ms", "--poll", "10ms", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, "IcyKnoll (0.1): idle") || !strings.Contains(out, "BlueLake (1.1): timeout") {
		t.Fatalf("all = %d %s", code, out)
	}
	// --any is met by IcyKnoll alone.
	if code, out, _ = runVerb(runWait, "swarm", "--any", "--until", "idle", "--socket", s.SocketPath); code != 0 {
		t.Fatalf("--any = %d %s", code, out)
	}
	// --type selects the kind.
	if code, out, _ = runVerb(runWait, "swarm", "--type", "codex", "--until", "generating", "--socket", s.SocketPath); code != 0 || !strings.Contains(out, "BlueLake (1.1): generating") {
		t.Fatalf("--type = %d %s", code, out)
	}
	for _, args := range [][]string{
		{"--until", "finished"}, {"a", "b"}, {"--count", "0"}, {"--timeout", "0s"},
		{"swarm", "--name", "Nobody", "--socket", s.SocketPath}, {"nowhere", "--socket", s.SocketPath},
	} {
		if code, _, _ := runVerb(runWait, args...); code != 2 {
			t.Errorf("%v = %d, want 2", args, code)
		}
	}
}

// ntm's idle is an agent at its prompt, so --until idle is met by herdr's
// done, where an unviewed pane ends its turn; --until done is herdr's done
// alone (nhm-y56).
func TestWaitIdleMeetsDone(t *testing.T) {
	var mu sync.Mutex
	state := "done"
	var waitedFor []string
	s, _, _ := waitInstance(t, func(pane string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		if pane == "w1:p1" {
			return agentMap(pane, "claude", state, 4, 4)
		}
		return agentMap(pane, "codex", "working", 7, 0)
	})
	s.Handle("agent.wait", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Until []string `json:"until"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		waitedFor = p.Until
		mu.Unlock()
		return nil, &herdrtest.Error{Code: "timeout", Message: "timed out"}
	})
	code, out, _ := runVerb(runWait, "swarm", "--name", "IcyKnoll", "--until", "idle", "--timeout", "2s", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "IcyKnoll (0.1): done") || !strings.Contains(out, "idle: met") {
		t.Fatalf("idle at done = %d %s", code, out)
	}
	if code, out, _ = runVerb(runWait, "swarm", "--name", "IcyKnoll", "--until", "done", "--timeout", "2s", "--socket", s.SocketPath); code != 0 {
		t.Fatalf("done at done = %d %s", code, out)
	}
	// herdr's agent.wait sleeps until either state.
	code, out, _ = runVerb(runWait, "swarm", "--name", "BlueLake", "--until", "idle", "--timeout", "100ms", "--poll", "10ms", "--socket", s.SocketPath)
	mu.Lock()
	got := strings.Join(waitedFor, ",")
	mu.Unlock()
	if code != 1 || got != "idle,done" {
		t.Fatalf("working = %d %s, agent.wait until %q", code, out, got)
	}
	mu.Lock()
	state = "idle"
	mu.Unlock()
	code, out, _ = runVerb(runWait, "swarm", "--name", "IcyKnoll", "--until", "done", "--timeout", "100ms", "--poll", "10ms", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, "IcyKnoll (0.1): timeout") {
		t.Fatalf("done at idle = %d %s", code, out)
	}
}

// sendReceipt records a send operation with one receipt for b, as
// nhm.send writes it.
func sendReceipt(t *testing.T, inst store.Instance, opID, status string, b bindings.Binding, base *wait.Baseline) {
	t.Helper()
	opReceipt(t, inst, "send", opID, status, b, base)
}

// opReceipt records an operation of the verb with one receipt for b.
func opReceipt(t *testing.T, inst store.Instance, verb, opID, status string, b bindings.Binding, base *wait.Baseline) {
	t.Helper()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ClaimOperation(ctx, store.ClaimRequest{ID: opID, Verb: verb, Request: opID}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var baseJSON any
	if base != nil {
		data, _ := json.Marshal(base)
		baseJSON = string(data)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO receipts (operation_id, target_index, binding_id, generation, status, baseline_json, updated_at)
		VALUES (?, 0, ?, ?, ?, ?, ?)`, opID, b.ID, b.Generation, status, baseJSON, store.FormatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
}

func TestWaitVerbCompleteAfterOp(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	s, inst, bs := waitInstance(t, func(pane string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads < 3 {
			return agentMap(pane, "claude", "working", 11, 0)
		}
		return agentMap(pane, "claude", "done", 12, 12)
	})
	icy := bs[0]
	base := &wait.Baseline{BindingID: icy.ID, Generation: icy.Generation, PaneID: "w1:p1", State: "idle", StateChangeSeq: 10}
	sendReceipt(t, inst, "op-send-1", wait.PossiblySubmitted, icy, base)
	code, out, errOut := runVerb(runWait, "--after-op", "op-send-1", "--until", "complete", "--poll", "10ms", "--socket", s.SocketPath, "--json")
	if code != 0 {
		t.Fatalf("complete = %d %s %s", code, out, errOut)
	}
	env := decodeEnvelope(t, out)
	tg := env["result"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	if env["operation_id"] != "op-send-1" || tg["outcome"] != "complete" || tg["correlation"] != "strong" || tg["started_seq"].(float64) != 11 {
		t.Fatalf("target = %v", tg)
	}

	// A fast finish that herdr's gate did not see is ambiguous: exit 4.
	sendReceipt(t, inst, "op-send-2", wait.PossiblySubmitted, icy, base)
	code, out, _ = runVerb(runWait, "--after-op", "op-send-2", "--until", "complete", "--socket", s.SocketPath)
	if code != exitAmbiguous || !strings.Contains(out, "IcyKnoll (0.1): ambiguous (weak)") {
		t.Fatalf("ambiguous = %d %s", code, out)
	}

	// A receipt of an older generation: replaced, exit 1.
	old := *base
	old.Generation = icy.Generation - 1
	sendReceipt(t, inst, "op-send-3", wait.Submitted, icy, &old)
	code, out, _ = runVerb(runWait, "--after-op", "op-send-3", "--until", "complete", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, "replaced") {
		t.Fatalf("replaced = %d %s", code, out)
	}

	if code, _, errOut := runVerb(runWait, "--after-op", "op-missing", "--socket", s.SocketPath); code != 2 || !strings.Contains(errOut, "no receipts") {
		t.Fatalf("unknown op = %d %s", code, errOut)
	}
}

// --after-op takes a nudge's operation (nhm-qaa). Its receipt is
// submitted, but the nudge did not wait for the turn to start: a turn
// that this wait saw start is complete, one that had already ended is
// ambiguous, where a send's would be complete.
func TestWaitAfterANudge(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	s, inst, bs := waitInstance(t, func(pane string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads < 3 {
			return agentMap(pane, "claude", "working", 11, 0)
		}
		return agentMap(pane, "claude", "done", 12, 12)
	})
	icy := bs[0]
	base := &wait.Baseline{BindingID: icy.ID, Generation: icy.Generation, PaneID: "w1:p1", State: "idle", StateChangeSeq: 10}
	opReceipt(t, inst, wake.Verb, "op-wake-1", wait.Submitted, icy, base)
	code, out, errOut := runVerb(runWait, "--after-op", "op-wake-1", "--until", "complete", "--poll", "10ms", "--socket", s.SocketPath, "--json")
	tg := decodeEnvelope(t, out)["result"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	if code != 0 || tg["outcome"] != "complete" || tg["correlation"] != "strong" || tg["started_seq"].(float64) != 11 {
		t.Fatalf("seen start = %d %v %s", code, tg, errOut)
	}
	// The turn had ended before the wait's first read.
	opReceipt(t, inst, wake.Verb, "op-wake-2", wait.Submitted, icy, base)
	code, out, _ = runVerb(runWait, "--after-op", "op-wake-2", "--until", "complete", "--socket", s.SocketPath)
	if code != exitAmbiguous || !strings.Contains(out, "IcyKnoll (0.1): ambiguous (weak)") {
		t.Fatalf("unseen start = %d %s", code, out)
	}
	sendReceipt(t, inst, "op-send-1", wait.Submitted, icy, base)
	if code, out, _ = runVerb(runWait, "--after-op", "op-send-1", "--until", "complete", "--socket", s.SocketPath); code != 0 ||
		!strings.Contains(out, "IcyKnoll (0.1): complete (strong)") {
		t.Fatalf("a send's = %d %s", code, out)
	}
	// A state wait after the nudge: the done after its baseline meets idle.
	if code, out, _ = runVerb(runWait, "--after-op", "op-wake-2", "--until", "idle", "--socket", s.SocketPath); code != 0 ||
		!strings.Contains(out, "IcyKnoll (0.1): done") {
		t.Fatalf("idle after the nudge = %d %s", code, out)
	}
}
