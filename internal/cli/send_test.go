package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// sendHerdr serves agent.list, agent.get and agent.prompt for the two
// agents of waitInstance. A prompt makes the agent work; finish makes
// it done.
type sendHerdr struct {
	mu      sync.Mutex
	status  map[string]string
	seq     map[string]int
	prompts []string
	// screens are the panes' last lines (none: an empty screen).
	screens map[string]string
}

func newSendInstance(t *testing.T) (*herdrtest.Server, store.Instance, *sendHerdr) {
	t.Helper()
	h := &sendHerdr{status: map[string]string{"w1:p1": "idle", "w1:p2": "idle"}, seq: map[string]int{"w1:p1": 10, "w1:p2": 10}}
	kinds := map[string]string{"w1:p1": "claude", "w1:p2": "codex"}
	names := map[string]string{"w1:p1": "icyknoll", "w1:p2": "bluelake"}
	agent := func(pane string) map[string]any {
		completion := 0
		if h.status[pane] == "idle" || h.status[pane] == "done" {
			completion = h.seq[pane]
		}
		a := agentMap(pane, kinds[pane], h.status[pane], h.seq[pane], completion)
		a["name"] = names[pane]
		return a
	}
	s, inst, _ := waitInstance(t, func(pane string) map[string]any {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := kinds[pane]; !ok {
			return nil
		}
		return agent(pane)
	})
	s.Handle("agent.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return map[string]any{"type": "agent_list", "agents": []any{agent("w1:p1"), agent("w1:p2")}}, nil
	})
	s.Handle("agent.prompt", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(r.Params, &p)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.prompts = append(h.prompts, p.Target+"="+p.Text)
		h.status[p.Target] = "working"
		h.seq[p.Target]++
		return map[string]any{"type": "agent_prompted", "agent": agent(p.Target)}, nil
	})
	s.Handle("agent.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		h.mu.Lock()
		defer h.mu.Unlock()
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.Target, "tab_id": "w1:t1", "workspace_id": "w1",
			"source": "recent", "format": "text", "revision": 1, "text": h.screens[p.Target], "truncated": false}}, nil
	})
	return s, inst, h
}

func (h *sendHerdr) finish(pane string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status[pane] = "done"
	h.seq[pane]++
}

func (h *sendHerdr) sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.prompts...)
}

// A "!" that a person left in an agent's input box refuses that target
// (nhm-mowp): the send says why, --robot-send lists it as failed, and
// the other agent gets its prompt.
func TestSendRefusesATargetWithAShellDraft(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	rule := strings.Repeat("─", 40)
	h.screens = map[string]string{"w1:p1": rule + "\n!\n" + rule + "\n  ! for shell mode\n"}
	why := `the input box shows Claude Code's bash mode (a "!" draft): herdr types nhm's text after what a person left there, ` +
		"so the agent would run it as a shell command that no guard such as dcg screens; clear the input box in pane w1:p1"
	code, out, errOut := run("send", "swarm", "hello", "--socket", s.SocketPath)
	if code != 1 || !strings.Contains(out, "IcyKnoll (0.1): refused (ready): "+why) || !strings.Contains(out, "BlueLake (1.1): submitted") {
		t.Fatalf("send = %d %s %s", code, out, errOut)
	}
	rout, rcode := robotJSON(t, "--robot-send=swarm", "--socket="+s.SocketPath, "--msg=hello again")
	failed, _ := rout["failed"].([]any)
	if rcode != 1 || rout["success"] != false || rout["blocked"] != false || len(failed) != 1 {
		t.Fatalf("robot send = %d %v", rcode, rout)
	}
	if first, _ := failed[0].(map[string]any); first["pane"] != "0.1" || first["error"] != "shell_draft: "+why {
		t.Fatalf("robot send failed = %v", failed)
	}
	if got := h.sent(); len(got) != 2 || got[0] != "w1:p2=hello" || got[1] != "w1:p2=hello again" {
		t.Fatalf("prompts = %v", got)
	}
}

func ownNhm(t *testing.T, inst store.Instance) {
	t.Helper()
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestSendVerb(t *testing.T) {
	s, inst, h := newSendInstance(t)
	sock := s.SocketPath
	// Under the Bash, a send is refused; a dry run reads only.
	code, out, _ := run("send", "swarm", "hello", "--socket", sock, "--json")
	if code != 2 || !strings.Contains(decodeEnvelope(t, out)["error"].(string), "owned by another") {
		t.Fatalf("under the Bash = %d %s", code, out)
	}
	code, out, _ = run("send", "swarm", "hello", "--dry-run", "--socket", sock)
	if code != 0 || !strings.Contains(out, "IcyKnoll (0.1): would send") || !strings.Contains(out, "--- BlueLake (1.1) ---\nhello") || len(h.sent()) != 0 {
		t.Fatalf("dry run = %d %s", code, out)
	}
	ownNhm(t, inst)
	code, out, errOut := run("send", "swarm", "hello", "there", "--cc", "--op-id", "op-v1", "--socket", sock)
	if code != 0 || !strings.Contains(out, "IcyKnoll (0.1): submitted (working)") || strings.Contains(out, "BlueLake") ||
		!strings.Contains(out, "operation op-v1") {
		t.Fatalf("send = %d %s %s", code, out, errOut)
	}
	if got := h.sent(); len(got) != 1 || got[0] != "w1:p1=hello there" {
		t.Fatalf("prompts = %v", got)
	}
	// The same id: the stored receipt, nothing sent.
	code, out, _ = run("send", "swarm", "hello", "there", "--cc", "--op-id", "op-v1", "--socket", sock)
	if code != 0 || !strings.Contains(out, "was claimed before") || len(h.sent()) != 1 {
		t.Fatalf("replay = %d %s", code, out)
	}
	if code, _, _ = run("send", "swarm", "other", "--cc", "--op-id", "op-v1", "--socket", sock); code != 2 {
		t.Fatalf("conflict = %d", code)
	}
	// IcyKnoll works now. --when ready refuses it (exit 3); the default
	// types the prompt in, as ntm's send does (nhm-r0v).
	code, out, _ = run("send", "swarm", "again", "--name", "IcyKnoll", "--when", "ready", "--socket", sock)
	if code != exitAdmission || !strings.Contains(out, "refused (busy)") {
		t.Fatalf("busy, --when ready = %d %s", code, out)
	}
	code, out, _ = run("send", "swarm", "again", "--name", "IcyKnoll", "--socket", sock)
	if got := h.sent(); code != 0 || got[len(got)-1] != "w1:p1=again" {
		t.Fatalf("busy, the default = %d %s %v", code, out, got)
	}
	// --msg-file - reads stdin; --wait records the completion.
	stdin = strings.NewReader("from stdin")
	t.Cleanup(func() { stdin = os.Stdin })
	go func() {
		time.Sleep(100 * time.Millisecond)
		h.finish("w1:p2")
	}()
	code, out, errOut = run("send", "swarm", "--name", "BlueLake", "--msg-file", "-", "--wait", "--poll", "20ms", "--timeout", "5s", "--socket", sock, "--json")
	if code != 0 {
		t.Fatalf("wait = %d %s %s", code, out, errOut)
	}
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	tg := res["targets"].([]any)[0].(map[string]any)
	obs := tg["result"].(map[string]any)["completion_observation"].(map[string]any)
	if tg["submission"] != "submitted" || obs["observation"] != "completed" || obs["correlation"] != "strong" {
		t.Fatalf("target = %v", tg)
	}
	for _, args := range [][]string{
		{"send", "swarm", "x", "--with-cass", "--no-cass", "--socket", sock},
		{"send", "swarm", "x", "--msg", "y", "--socket", sock},
		{"send", "swarm", "--socket", sock},
		{"send", "swarm", "x", "--state", "sleepy", "--socket", sock},
		{"send", "swarm", "x", "--name", "Nobody", "--socket", sock},
		{"send", "swarm", "-t", "no_such_template", "--socket", sock},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Errorf("%v = %d, want 2", args[2:], code)
		}
	}
	// --with-memory=false is accepted.
	if code, out, _ = run("send", "swarm", "x", "--name", "IcyKnoll", "--with-memory=false", "--no-cass", "--dry-run", "--socket", sock); code != 0 {
		t.Fatalf("memory off = %d %s", code, out)
	}
}

// packetOutcome is upstream swarm_packet.sh's outcome(): the contract
// that a robot send of one packet to one pane must meet.
func packetOutcome(resp map[string]any, session, opID, digest string, bytes int, target string, receiptQuery bool) bool {
	op, ok := resp["operation"].(map[string]any)
	if !ok || resp["session"] != session || op["operation_id"] != opID || op["payload_sha256"] != digest ||
		op["payload_bytes"] != float64(bytes) {
		return false
	}
	result := resp
	if receiptQuery {
		result, _ = resp["outcome"].(map[string]any)
	}
	adm, _ := json.Marshal(op["admissions"])
	list := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	return resp["success"] == true && op["status"] == "completed" && result != nil && result["success"] == true &&
		list(result["targets"]) == `["`+target+`"]` && list(result["successful"]) == `["`+target+`"]` &&
		list(result["failed"]) == `[]` && string(adm) == `[{"state":"submitted","target":"`+target+`"}]`
}

func robotJSON(t *testing.T, args ...string) (map[string]any, int) {
	t.Helper()
	code, out, _ := run(args...)
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%v: not JSON: %s", args, out)
	}
	return m, code
}

func TestRobotSendContract(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	sock := "--socket=" + s.SocketPath
	packet := "# ACFS Swarm Startup Packet\nDo the bead.\n"
	sum, n := sha256Hex(packet)
	argv := []string{"--robot-send=swarm", "--panes=w1:p1", "--type=claude", "--msg-file=-", "--robot-format=json", "--no-cass", "--with-memory=false", sock}

	// The preview: exactly one target, no injection, nothing sent.
	stdin = strings.NewReader(packet)
	t.Cleanup(func() { stdin = os.Stdin })
	preview, code := robotJSON(t, append(argv, "--dry-run")...)
	would, _ := json.Marshal(preview["would_send_to"])
	if code != 0 || preview["success"] != true || preview["dry_run"] != true || preview["blocked"] != false ||
		string(would) != `["0.1"]` || len(preview["successful"].([]any)) != 0 || len(preview["failed"].([]any)) != 0 ||
		preview["memory_injection"].(map[string]any)["enabled"] != false || preview["cass_injection"].(map[string]any)["enabled"] != false {
		t.Fatalf("preview = %d %v", code, preview)
	}
	if len(h.sent()) != 0 {
		t.Fatal("the preview sent")
	}

	stdin = strings.NewReader(packet)
	sent, code := robotJSON(t, append(argv, "--op-id=pkt-1")...)
	if code != 0 || !packetOutcome(sent, "swarm", "pkt-1", sum, n, "0.1", false) {
		t.Fatalf("send = %d %v", code, sent)
	}
	if got := h.sent(); len(got) != 1 || got[0] != "w1:p1="+packet {
		t.Fatalf("prompts = %q", got)
	}
	receipt, code := robotJSON(t, "--robot-send-receipt=pkt-1", "--robot-format=json", sock)
	if code != 0 || !packetOutcome(receipt, "swarm", "pkt-1", sum, n, "0.1", true) {
		t.Fatalf("receipt = %d %v", code, receipt)
	}
	// A retry with the same id replays; another payload conflicts.
	stdin = strings.NewReader(packet)
	again, code := robotJSON(t, append(argv, "--op-id=pkt-1")...)
	if code != 0 || again["operation"].(map[string]any)["replayed"] != true || len(h.sent()) != 1 {
		t.Fatalf("replay = %d %v", code, again)
	}
	stdin = strings.NewReader("another packet")
	conflict, code := robotJSON(t, append(argv, "--op-id=pkt-1")...)
	if code != 1 || conflict["error_code"] != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflict = %d %v", code, conflict)
	}
	if nf, code := robotJSON(t, "--robot-send-receipt=nope", sock); code != 1 || nf["error_code"] != "NOT_FOUND" {
		t.Fatalf("unknown receipt = %d %v", code, nf)
	}
	// IcyKnoll is busy now: as ntm's robot send, the prompt is typed in
	// (nhm-r0v).
	busy, code := robotJSON(t, "--robot-send=swarm", "--panes=0.1", "--msg=hi", sock)
	if got := h.sent(); code != 0 || busy["success"] != true || got[len(got)-1] != "w1:p1=hi" {
		t.Fatalf("busy = %d %v %v", code, busy, got)
	}
	// --with-memory is best-effort: without cm the send goes on, and the
	// injection says why it is empty.
	m, _ := robotJSON(t, "--robot-send=swarm", "--msg=hi", "--with-memory", sock)
	mi, _ := m["memory_injection"].(map[string]any)
	if mi["enabled"] != true || !strings.Contains(fmt.Sprint(mi["skipped_reason"]), "cm is not installed") {
		t.Fatalf("with memory = %v", m)
	}
}

func TestRobotAck(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	sock := "--socket=" + s.SocketPath
	if _, code := robotJSON(t, "--robot-send=swarm", "--panes=1.1", "--msg=review", "--op-id=ack-1", sock); code != 0 {
		t.Fatalf("send = %d", code)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		h.finish("w1:p2")
	}()
	ack, code := robotJSON(t, "--robot-ack=swarm", "--timeout=5s", sock)
	conf, _ := ack["confirmations"].([]any)
	if code != 0 || ack["success"] != true || len(conf) != 1 || conf[0].(map[string]any)["pane"] != "1.1" ||
		conf[0].(map[string]any)["ack_type"] != "prompt_returned" {
		t.Fatalf("ack = %d %v", code, ack)
	}
	if m, code := robotJSON(t, "--robot-ack=swarm", "--op-id=nope", sock); code != 1 || m["error_code"] != "NOT_FOUND" {
		t.Fatalf("unknown op = %d %v", code, m)
	}
}

func sha256Hex(s string) (string, int) {
	sum, n := send.Digest(s)
	return sum, int(n)
}
