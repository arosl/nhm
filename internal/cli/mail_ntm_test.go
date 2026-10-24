package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// overseerMail is a fake Agent Mail for mail send, read and ack: it
// records each tool's last arguments and the marks, and answers
// fetch_inbox with inbox.
type overseerMail struct {
	mu      sync.Mutex
	agents  []string
	inbox   []map[string]any
	down    bool
	args    map[string]map[string]any
	marks   []string
	refuseM int
}

func newOverseerMail(t *testing.T, agents []string, inbox []map[string]any) *overseerMail {
	t.Helper()
	m := &overseerMail{agents: agents, inbox: inbox, args: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		m.mu.Lock()
		defer m.mu.Unlock()
		a := call.Params.Arguments
		m.args[call.Params.Name] = a
		text, isErr := "{}", false
		switch call.Params.Name {
		case "health_check":
			if m.down {
				text, isErr = "database is locked", true
			}
		case "ensure_project":
			text = `{"slug":"p"}`
		case "list_agents":
			var list []map[string]any
			for _, n := range m.agents {
				list = append(list, map[string]any{"name": n})
			}
			data, _ := json.Marshal(list)
			text = string(data)
		case "send_message":
			text = `{"deliveries":[{"project":"/p","payload":{"id":5812,"created_ts":"2026-10-24T01:49:27Z"}}],"count":1}`
		case "fetch_inbox":
			data, _ := json.Marshal(m.inbox)
			text = string(data)
		case "mark_message_read", "acknowledge_message":
			id := fmt.Sprint(a["message_id"])
			if id == fmt.Sprint(m.refuseM) {
				text, isErr = "message not found", true
				break
			}
			m.marks = append(m.marks, call.Params.Name+":"+fmt.Sprint(a["agent_name"])+"#"+id)
		}
		res, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"isError": isErr, "content": []any{map[string]any{"type": "text", "text": text}}}})
		_, _ = w.Write(res)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENT_MAIL_URL", srv.URL+"/mcp/")
	t.Setenv("AGENT_NAME", "")
	return m
}

func (m *overseerMail) last(tool string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.args[tool]
}

// As ntm's: bare mail, and a word that is no sub-verb, print the help and
// exit 0.
func TestMailBarePrintsHelp(t *testing.T) {
	for _, args := range [][]string{{"mail"}, {"mail", "outbox"}, {"mail", "--help"}} {
		if code, out, errOut := run(args...); code != 0 || out != mailVerbUsage || errOut != "" {
			t.Fatalf("%v = %d %q %q", args, code, out, errOut)
		}
	}
	if code, out, _ := run("mail", "inbox", "--help"); code != 0 || out != mailUsage {
		t.Fatalf("mail inbox --help = %d %q", code, out)
	}
}

// mail send goes as HumanOverseer to the session's project: high
// importance, the preamble, a subject from the first line.
func TestMailSendAsOverseer(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newOverseerMail(t, nil, nil)
	code, out, errOut := run("mail", "send", "swarm", "--to", "BlueLake", "--socket", s.SocketPath, "Please review", "the API")
	want := "🚨 Human Overseer message sent to 1 agent(s)\n  → BlueLake\n\nAgents will receive this with high priority and a directive preamble.\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("mail send = %d %q %q", code, out, errOut)
	}
	send := m.last("send_message")
	if send["project_key"] != "/p" || send["sender_name"] != "HumanOverseer" || send["importance"] != "high" ||
		send["subject"] != "Please review the API" || !strings.HasPrefix(fmt.Sprint(send["body_md"]), "> **Human Overseer message**") ||
		!strings.HasSuffix(fmt.Sprint(send["body_md"]), "\n\nPlease review the API") || fmt.Sprint(send["to"]) != "[BlueLake]" {
		t.Fatalf("send_message = %v", send)
	}
	if reg := m.last("register_agent"); reg["name"] != "HumanOverseer" || reg["project_key"] != "/p" {
		t.Fatalf("register_agent = %v", reg)
	}
	code, out, _ = run("mail", "send", "swarm", "--to", "BlueLake", "--to", "IcyKnoll", "-s", "Stop", "--thread", "FEAT-1",
		"--json", "--socket", s.SocketPath, "now")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || got["overseer"] != true || got["success"] != true ||
		got["message_id"] != float64(5812) || got["subject"] != "Stop" || fmt.Sprint(got["recipients"]) != "[BlueLake IcyKnoll]" ||
		got["sent_at"] != "2026-10-24T01:49:27Z" {
		t.Fatalf("mail send --json = %d %v %q", code, err, out)
	}
	if send := m.last("send_message"); send["thread_id"] != "FEAT-1" {
		t.Fatalf("thread = %v", send)
	}
}

// --all sends to every agent of the project but HumanOverseer.
func TestMailSendAll(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newOverseerMail(t, []string{"IcyKnoll", "HumanOverseer", "BlueLake"}, nil)
	if code, out, errOut := run("mail", "send", "swarm", "--all", "--socket", s.SocketPath, "Checkpoint"); code != 0 ||
		!strings.HasPrefix(out, "🚨 Human Overseer message sent to 2 agent(s)\n  → IcyKnoll\n  → BlueLake\n") {
		t.Fatalf("--all = %d %q %q", code, out, errOut)
	}
	if fmt.Sprint(m.last("send_message")["to"]) != "[IcyKnoll BlueLake]" {
		t.Fatalf("to = %v", m.last("send_message")["to"])
	}
	m.agents = []string{"HumanOverseer"}
	if code, _, errOut := run("mail", "send", "swarm", "--all", "--socket", s.SocketPath, "x"); code != exitError ||
		errOut != "Error: no agents registered in project (agents must register with Agent Mail first)\n" {
		t.Fatalf("no agents = %d %q", code, errOut)
	}
}

// With no body, $EDITOR writes it: a first heading is the subject.
func TestMailSendOpensEditor(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newOverseerMail(t, nil, nil)
	editor := filepath.Join(t.TempDir(), "ed")
	script := "#!/bin/sh\nfor f; do :; done\nprintf '# Deploy freeze\\n\\n<!-- note -->\\n\\nNo deploys today.\\nThanks.\\n' > \"$f\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	if code, _, errOut := run("mail", "send", "swarm", "--to", "BlueLake", "--socket", s.SocketPath); code != 0 {
		t.Fatalf("editor send = %d %q", code, errOut)
	}
	send := m.last("send_message")
	if send["subject"] != "Deploy freeze" || !strings.HasSuffix(fmt.Sprint(send["body_md"]), "\n\nNo deploys today.\nThanks.") {
		t.Fatalf("send_message = %v", send)
	}
	// An editor with shell metacharacters runs the fallback, not the line.
	t.Setenv("EDITOR", "vi; rm -rf /")
	t.Setenv("PATH", t.TempDir())
	if code, _, errOut := run("mail", "send", "swarm", "--to", "BlueLake", "--socket", s.SocketPath); code != exitError ||
		!strings.Contains(errOut, "composing message: configuring editor: editor not found") {
		t.Fatalf("unsafe editor = %d %q", code, errOut)
	}
}

// ntm's refusals, and the warning for a secret in the body (ntm's default
// mode, warn: the message goes as written).
func TestMailSendRefusalsAndWarnings(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newOverseerMail(t, nil, nil)
	empty := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(empty, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]string{
		"swarm hello":                                   "Error: no recipients specified (use --to <agent-name> or --all)\n",
		"swarm --to A -f " + empty:                      "Error: message body cannot be empty\n",
		"swarm --to A -f /nonexistent/body.md":          "Error: reading file: open /nonexistent/body.md: no such file or directory\n",
		"swarm --to A --prepared-redaction h -s S body": "Error: --prepared-redaction is mutually exclusive with --file and positional body\n",
		"swarm --to A --prepared-redaction h":           "Error: --prepared-redaction requires an explicit --subject to avoid leaking the raw token prefix as the auto-derived subject\n",
		"swarm --to A --prepared-redaction h -s S":      "Error: --prepared-redaction is not in this nhm yet (deferred: nhm-2p6.20)\n",
		"": "Error: requires at least 1 arg(s), only received 0\n",
	} {
		argv := append([]string{"mail", "send"}, strings.Fields(args)...)
		if args != "" {
			argv = append(argv, "--socket", s.SocketPath)
		}
		if code, out, errOut := run(argv...); code != exitError || out != "" || errOut != want {
			t.Errorf("mail send %s = %d %q %q, want %q", args, code, out, errOut, want)
		}
	}
	code, out, errOut := run("mail", "send", "swarm", "--to", "BlueLake", "--socket", s.SocketPath, "--json",
		"use", "ghp_1234567890abcdefghijklmnopqrstuvwxyzAB")
	// The body and the subject cut from it: two findings, as ntm scans
	// both.
	if code != 0 || errOut != "Warning: detected 2 potential secret(s) (GITHUB_TOKEN=2); message will be sent unmodified (mode=warn)\n" ||
		!strings.Contains(out, `"action": "warn"`) ||
		!strings.Contains(fmt.Sprint(m.last("send_message")["body_md"]), "ghp_1234567890abcdefghijklmnopqrstuvwxyzAB") {
		t.Fatalf("a secret = %d %q %q", code, out, errOut)
	}
	// A session herdr does not have: a warning, and the cwd's project.
	t.Setenv("AGENT_MAIL_PROJECT", "/q")
	if code, _, errOut := run("mail", "send", "nosuch", "--to", "BlueLake", "--socket", s.SocketPath, "hi"); code != 0 ||
		errOut != "Warning: session 'nosuch' not found (continuing anyway)\n" || m.last("send_message")["project_key"] != "/q" {
		t.Fatalf("unknown session = %d %q %v", code, errOut, m.last("send_message")["project_key"])
	}
	m.down = true
	if code, _, errOut := run("mail", "send", "swarm", "--to", "BlueLake", "--socket", s.SocketPath, "hi"); code != exitError ||
		!strings.HasPrefix(errOut, "Error: agent mail server not available at ") || !strings.Contains(errOut, "\nreason: agentmail health_check: database is locked") {
		t.Fatalf("Agent Mail down = %d %q", code, errOut)
	}
}

// mail read marks the given ids read for the agent; a failure is a line on
// stderr, and the rest goes on.
func TestMailReadMarksIDs(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newOverseerMail(t, nil, nil)
	m.refuseM = 99
	code, out, errOut := run("mail", "read", "swarm", "12", "99", "13", "--agent", "IcyKnoll", "--socket", s.SocketPath)
	if code != 0 || out != "✓ Message 12 marked as read\n✓ Message 13 marked as read\n" ||
		errOut != "⚠ message 99: agentmail mark_message_read: message not found\n" {
		t.Fatalf("mail read = %d %q %q", code, out, errOut)
	}
	if fmt.Sprint(m.marks) != "[mark_message_read:IcyKnoll#12 mark_message_read:IcyKnoll#13]" {
		t.Fatalf("marks = %v", m.marks)
	}
	code, out, _ = run("mail", "ack", "swarm", "12", "99", "--agent", "IcyKnoll", "--json", "--socket", s.SocketPath)
	var sum markSummary
	if err := json.Unmarshal([]byte(out), &sum); code != 0 || err != nil || sum.Action != "ack" || sum.Processed != 1 ||
		sum.Skipped != 1 || sum.Errors != 1 || fmt.Sprint(sum.IDs) != "[12 99]" {
		t.Fatalf("mail ack --json = %d %v %q", code, err, out)
	}
}

// --all, --urgent and --from select from the agent's inbox, read or not;
// read takes the unread ones, ack the ones that ask for an ack; the fetch
// marks nothing read.
func TestMailAckFilters(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newOverseerMail(t, nil, []map[string]any{
		{"id": 1, "from": "BlueLake", "ack_required": true, "read_ts": "2026-10-24T01:00:00Z"},
		{"id": 2, "from": "Other", "ack_required": true},
		{"id": 3, "from": "bluelake"},
	})
	t.Setenv("AGENT_NAME", "IcyKnoll")
	if code, out, _ := run("mail", "ack", "swarm", "--all", "--socket", s.SocketPath); code != 0 ||
		out != "✓ Message 1 acknowledged\n✓ Message 2 acknowledged\n" {
		t.Fatalf("ack --all = %d %q", code, out)
	}
	if f := m.last("fetch_inbox"); f["mark_read"] != false || f["agent_name"] != "IcyKnoll" || f["limit"] != float64(50) {
		t.Fatalf("fetch_inbox = %v", f)
	}
	m.marks = nil
	if code, out, _ := run("mail", "read", "swarm", "--from", "BlueLake", "--limit", "5", "--socket", s.SocketPath); code != 0 ||
		out != "✓ Message 3 marked as read\n" || m.last("fetch_inbox")["limit"] != float64(5) {
		t.Fatalf("read --from = %d %q", code, out)
	}
	if code, out, _ := run("mail", "read", "swarm", "--from", "Nobody", "--socket", s.SocketPath); code != 0 || out != "No matching messages found.\n" {
		t.Fatalf("no match = %d %q", code, out)
	}
	if code, out, _ := run("mail", "read", "swarm", "--from", "Nobody", "--json", "--socket", s.SocketPath); code != 0 ||
		!strings.Contains(out, `"action": "read"`) || !strings.Contains(out, `"ids": null`) {
		t.Fatalf("no match --json = %d %q", code, out)
	}
	if code, _, _ := run("mail", "ack", "swarm", "--urgent", "--socket", s.SocketPath); code != 0 || m.last("fetch_inbox")["urgent_only"] != true {
		t.Fatalf("--urgent: %v", m.last("fetch_inbox"))
	}
}

// read and ack need ids or a filter, an agent, a positive --limit and
// numeric ids.
func TestMailMarkNeedsIDsOrFilter(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	newOverseerMail(t, nil, nil)
	for args, want := range map[string]string{
		"read swarm --agent IcyKnoll":      "Error: provide message IDs or use --all/--urgent/--from to select messages\n",
		"read swarm 12":                    "Error: --agent is required (or set AGENT_NAME)\n",
		"ack swarm 12 --agent A --limit 0": "Error: --limit must be greater than 0\n",
		"ack swarm 12 x --agent A":         "Error: invalid message id \"x\"\n",
		"read":                             "Error: requires at least 1 arg(s), only received 0\n",
	} {
		argv := append([]string{"mail"}, strings.Fields(args)...)
		if args != "read" {
			argv = append(argv, "--socket", s.SocketPath)
		}
		if code, out, errOut := run(argv...); code != exitError || out != "" || errOut != want {
			t.Errorf("mail %s = %d %q %q, want %q", args, code, out, errOut, want)
		}
	}
}
