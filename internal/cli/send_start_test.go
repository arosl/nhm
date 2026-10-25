package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// brenner's cockpit (ACFS E21): spawn with no prompt returns before the
// agent is ready, as ntm's does, and `nhm send THREAD --all` follows at
// once. herdr reports the new agent launch-pending and refuses prompts to
// it; the send waits for the launch and delivers (nhm-2p6.5.12).
func TestSendRightAfterASpawn(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	readyAt := time.Now().Add(500 * time.Millisecond)
	pending := func() bool { return time.Now().Before(readyAt) }
	s.Handle("agent.get", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		h.mu.Lock()
		kind, status, seq := map[string]string{"w1:p1": "claude", "w1:p2": "codex"}[p.Target], h.status[p.Target], h.seq[p.Target]
		h.mu.Unlock()
		a := agentMap(p.Target, kind, status, seq, seq)
		if p.Target == "w1:p1" && pending() {
			a["launch_pending"] = true
		}
		return map[string]any{"type": "agent_info", "agent": a}, nil
	})
	s.Handle("agent.prompt", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(r.Params, &p)
		if p.Target == "w1:p1" && pending() {
			return nil, &herdrtest.Error{Code: "agent_not_ready", Message: "agent w1:p1 is not an active named agent"}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		h.prompts = append(h.prompts, p.Target+"="+p.Text)
		h.status[p.Target] = "working"
		h.seq[p.Target]++
		kind := map[string]string{"w1:p1": "claude", "w1:p2": "codex"}[p.Target]
		return map[string]any{"type": "agent_prompted", "agent": agentMap(p.Target, kind, "working", h.seq[p.Target], 0)}, nil
	})
	code, out, errOut := run("send", "swarm", "--all", "Please check your Agent Mail inbox.", "--socket", s.SocketPath)
	if code != exitOK {
		t.Fatalf("send = %d %s %s", code, out, errOut)
	}
	got := map[string]bool{}
	for _, p := range h.sent() {
		got[p] = true
	}
	if !got["w1:p1=Please check your Agent Mail inbox."] || !got["w1:p2=Please check your Agent Mail inbox."] {
		t.Fatalf("prompts = %v", h.sent())
	}
	if time.Now().Before(readyAt) {
		t.Fatal("the send ended before the agent's launch finished")
	}
}
