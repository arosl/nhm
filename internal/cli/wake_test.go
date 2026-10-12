package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/store"
)

// fakeAgentMail serves fetch_inbox from inbox (by agent) and records
// mark_message_read; AGENT_MAIL_URL points nhm at it.
type fakeAgentMail struct {
	mu     sync.Mutex
	inbox  map[string][]map[string]any
	marked []string
}

func newFakeAgentMail(t *testing.T) *fakeAgentMail {
	t.Helper()
	m := &fakeAgentMail{inbox: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		args := call.Params.Arguments
		m.mu.Lock()
		text, isErr := "{}", false
		switch call.Params.Name {
		case "fetch_inbox":
			if args["mark_read"] != false {
				text, isErr = "fetch_inbox must not consume", true
				break
			}
			data, _ := json.Marshal(m.inbox[args["agent_name"].(string)])
			if data == nil || string(data) == "null" {
				data = []byte("[]")
			}
			text = string(data)
		case "mark_message_read":
			m.marked = append(m.marked, args["agent_name"].(string)+"#"+jsonNumber(args["message_id"]))
		}
		m.mu.Unlock()
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"isError": isErr, "content": []any{map[string]any{"type": "text", "text": text}}}})
		_, _ = w.Write(res)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENT_MAIL_URL", srv.URL)
	return m
}

func jsonNumber(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func (m *fakeAgentMail) set(agent string, msgs ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inbox[agent] = msgs
}

func msg(id int, from, thread, kind string) map[string]any {
	return map[string]any{"id": id, "from": from, "thread_id": thread, "kind": kind, "subject": "s" + jsonNumber(id),
		"created_ts": "2026-10-12T05:00:00Z", "importance": "normal", "ack_required": false, "body_md": "body"}
}

func TestWakeVerbNamesEachDecision(t *testing.T) {
	s, inst, h := newSendInstance(t)
	mail := newFakeAgentMail(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	// Mail to an idle agent and to a working one (E09).
	h.status["w1:p2"] = "working"
	mail.set("IcyKnoll", msg(7, "SageOak", "acfs-1", "to"))
	mail.set("BlueLake", msg(8, "SageOak", "acfs-1", "to"))

	// Under the Bash, nhm does not wake.
	if code, _, _ := runVerb(runWake, "--json"); code != exitRefused || len(h.sent()) != 0 {
		t.Fatalf("under the Bash: exit %d, sent %v", code, h.sent())
	}
	ownNhm(t, inst)
	code, out, errOut := runVerb(runWake, "--json")
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	var env struct {
		Result struct {
			Recorded  int             `json:"recorded"`
			Decisions []wake.Decision `json:"decisions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	got := map[string]wake.Decision{}
	for _, d := range env.Result.Decisions {
		got[d.MailName] = d
	}
	if env.Result.Recorded != 2 || got["IcyKnoll"].Action != wake.ActionNudged || got["BlueLake"].Action != wake.ActionSkipped ||
		!strings.Contains(got["BlueLake"].Reason, "busy") {
		t.Fatalf("result = %s", out)
	}
	if sent := h.sent(); len(sent) != 1 || sent[0] != "w1:p1=Check your Agent Mail inbox." {
		t.Fatalf("sent = %v", sent)
	}
	// A second pass announces nothing twice; --recipients limits it.
	code, out, _ = runVerb(runWake, "--recipients", "icyknoll")
	if code != exitOK || !strings.Contains(out, "no pending mail") || len(h.sent()) != 1 {
		t.Fatalf("second pass: exit %d %q, sent %v", code, out, h.sent())
	}
	if code, _, errOut := runVerb(runWake, "--recipients", "Nobody"); code != exitRefused || !strings.Contains(errOut, "no active binding") {
		t.Fatalf("unknown recipient: exit %d %q", code, errOut)
	}
}

func TestMaintainDeliversRecordsTheRunnerAndIsASingleton(t *testing.T) {
	s, inst, h := newSendInstance(t)
	mail := newFakeAgentMail(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	ownNhm(t, inst)
	h.status["w1:p2"] = "done"
	mail.set("BlueLake", msg(9, "SageOak", "", "cc"))
	code, out, errOut := runVerb(runMaintain, "--once", "--json")
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if sent := h.sent(); len(sent) != 1 || !strings.HasPrefix(sent[0], "w1:p2=") {
		t.Fatalf("sent = %v (%s)", sent, out)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runner, at, ok, err := wake.LastPass(context.Background(), db)
	if err != nil || !ok || runner != wake.RunnerOnce || time.Since(at) > time.Minute {
		t.Fatalf("last pass = %s %s %v %v", runner, at, ok, err)
	}
	if c := maintainCheck(runner, at, ok, nil, time.Now()); c.Result != checkOK {
		t.Fatalf("doctor = %+v", c)
	}
	if c := maintainCheck(runner, at, ok, nil, time.Now().Add(5*time.Minute)); c.Result != checkWarn || c.Fix == "" {
		t.Fatalf("stale doctor = %+v", c)
	}
	if c := maintainCheck("", time.Time{}, false, nil, time.Now()); c.Result != checkWarn {
		t.Fatalf("never doctor = %+v", c)
	}
	// One runner per server: a second one exits 0 and says so.
	unlock, err := tryLock(inst.Dir + "/" + maintainLockFile)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	code, out, _ = runVerb(runMaintain, "--once")
	if code != exitOK || !strings.Contains(out, "another nhm maintain runs") {
		t.Fatalf("second runner: exit %d %q", code, out)
	}
}

// send --thread runs a wake pass on the targets: the target works on
// the prompt, so its mail stays pending for maintenance.
func TestSendThreadWakesTheTargets(t *testing.T) {
	s, inst, h := newSendInstance(t)
	mail := newFakeAgentMail(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	ownNhm(t, inst)
	mail.set("IcyKnoll", msg(21, "SageOak", "acfs-2", "to"))
	code, out, errOut := run("send", "swarm", "Read mail on acfs-2.", "--name", "IcyKnoll", "--thread", "acfs-2", "--socket", s.SocketPath)
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if sent := h.sent(); len(sent) != 1 || sent[0] != "w1:p1=Read mail on acfs-2." {
		t.Fatalf("sent = %v", sent)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM wake WHERE message_id = '21' AND announced_at IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pending rows = %d, %v", n, err)
	}
}

func TestMailInboxListsAndMarksRead(t *testing.T) {
	s, inst, _ := newSendInstance(t)
	mail := newFakeAgentMail(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	ownNhm(t, inst)
	mail.set("IcyKnoll", msg(12, "B", "t2", "to"), msg(11, "A", "t1", "bcc"), msg(13, "A", "t1", "cc"))
	// The wake table knows message 11 as pending.
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.SQL().Exec(`INSERT INTO wake (binding_id, message_id, first_seen_at)
		SELECT binding_id, '11', '2026-10-12T05:00:00Z' FROM bindings WHERE mail_name = 'IcyKnoll'`); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runVerb(runMail, "inbox", "--agent", "IcyKnoll", "--project", "/p")
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "== thread t1 (1 unread)") || strings.Index(out, "#11") > strings.Index(out, "#12") ||
		!strings.Contains(out, "listed 2 unread, marked 2 read; 1 cc still unread") {
		t.Fatalf("out = %s", out)
	}
	if strings.Join(mail.marked, ",") != "IcyKnoll#11,IcyKnoll#12" {
		t.Fatalf("marked = %v", mail.marked)
	}
	var outcome string
	if err := db.SQL().QueryRow(`SELECT COALESCE(last_outcome, '') FROM wake WHERE message_id = '11'`).Scan(&outcome); err != nil || outcome != wake.OutcomeRead {
		t.Fatalf("wake row = %q, %v", outcome, err)
	}
	if code, _, _ := runVerb(runMail, "outbox"); code != exitRefused {
		t.Fatalf("unknown mail verb: exit %d", code)
	}
}
