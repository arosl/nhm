package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
)

// projectMail is a fake Agent Mail for ntm's mail inbox: list_agents
// names the project's agents, and fetch_inbox answers each agent's mail.
// Every fetch_inbox call's arguments are recorded.
type projectMail struct {
	mu      sync.Mutex
	agents  []string
	inbox   map[string][]map[string]any
	fetches []map[string]any
	project []string
}

func newProjectMail(t *testing.T, agents []string, inbox map[string][]map[string]any) *projectMail {
	t.Helper()
	m := &projectMail{agents: agents, inbox: inbox}
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
		text, isErr := "[]", false
		switch call.Params.Name {
		case "list_agents":
			m.project = append(m.project, fmt.Sprint(args["project_key"]))
			var list []map[string]any
			for _, a := range m.agents {
				list = append(list, map[string]any{"name": a, "program": "claude-code"})
			}
			data, _ := json.Marshal(list)
			text = string(data)
		case "fetch_inbox":
			m.fetches = append(m.fetches, args)
			name := fmt.Sprint(args["agent_name"])
			msgs, ok := m.inbox[name]
			if !ok {
				text, isErr = "agent not found: "+name, true
				break
			}
			if args["urgent_only"] == true {
				var urgent []map[string]any
				for _, msg := range msgs {
					if msg["importance"] == "urgent" || msg["importance"] == "high" {
						urgent = append(urgent, msg)
					}
				}
				msgs = urgent
			}
			if msgs == nil {
				msgs = []map[string]any{}
			}
			data, _ := json.Marshal(msgs)
			text = string(data)
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

// fetched is the agents whose inbox was read, sorted, and fails the test
// when a read could mark mail read or read only the unread mail.
func (m *projectMail) fetched(t *testing.T) []string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for _, f := range m.fetches {
		if f["mark_read"] != false || f["unread_only"] == true {
			t.Errorf("fetch_inbox %v: ntm's inbox reads without marking, read or not", f)
		}
		names = append(names, fmt.Sprint(f["agent_name"]))
	}
	m.fetches = nil
	sort.Strings(names)
	return names
}

func swarmMail(t *testing.T) *projectMail {
	return newProjectMail(t, []string{"IcyKnoll", "BlueLake", "HumanOverseer", "Other"}, map[string][]map[string]any{
		"IcyKnoll": {
			{"id": 9, "subject": "shared", "from": "Other", "created_ts": "2026-10-23T10:00:00Z", "importance": "normal", "kind": "to"},
			{"id": 7, "subject": "Build\nplan \x1b[31m", "from": "BlueLake", "created_ts": "2026-10-23T09:00:00Z", "importance": "urgent",
				"ack_required": true, "kind": "to"},
		},
		"BlueLake":      {{"id": 9, "subject": "shared", "from": "Other", "created_ts": "2026-10-23T10:00:00Z", "importance": "normal", "kind": "cc"}},
		"HumanOverseer": {{"id": 11, "subject": "never read", "from": "x"}},
		"Other":         {},
	})
}

// nhm mail inbox <session> is ntm's mail inbox: every agent's recent mail
// in the session's project, each message once with the agents that got
// it, oldest first, and nothing marked read (the operator's ruling,
// nhm-k9d, 2026-10-23).
func TestMailInboxSessionIsNTMsProjectInbox(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := swarmMail(t)
	code, out, errOut := run("mail", "inbox", "swarm", "--socket", s.SocketPath)
	want := "Project Inbox: p\n[URGENT] Build plan\nBlueLake → IcyKnoll\nshared\nOther → IcyKnoll, BlueLake\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("mail inbox swarm = %d %q %q, want %q", code, out, errOut, want)
	}
	if got := m.fetched(t); fmt.Sprint(got) != "[BlueLake IcyKnoll Other]" {
		t.Errorf("read the inboxes of %v; HumanOverseer is left out, as ntm's", got)
	}
	if fmt.Sprint(m.project) != "[/p]" {
		t.Errorf("listed the agents of %v, want the bound agents' project /p", m.project)
	}
	// ntm's JSON fields under nhm's envelope.
	code, out, _ = run("mail", "inbox", "w1", "--json", "--socket", s.SocketPath)
	res := envelopeOf(t, out)["result"].([]any)
	first, second := res[0].(map[string]any), res[1].(map[string]any)
	if code != 0 || len(res) != 2 || first["id"] != float64(7) || first["ack_required"] != true || first["kind"] != "to" ||
		fmt.Sprint(first["recipients"]) != "[IcyKnoll]" || fmt.Sprint(second["recipients"]) != "[IcyKnoll BlueLake]" {
		t.Fatalf("mail inbox --json = %d %s", code, out)
	}
	m.fetched(t)
}

// The bare form is ntm's project inbox too (the operator's ruling,
// nhm-2p6, 2026-10-24): the cwd's project, or AGENT_MAIL_PROJECT, every
// agent's mail, nothing marked read; --agent keeps one agent.
func TestMailInboxBareIsNTMsProjectInbox(t *testing.T) {
	m := swarmMail(t)
	t.Setenv("AGENT_MAIL_PROJECT", "/q")
	t.Setenv("HERDR_SOCKET_PATH", "")
	code, out, errOut := run("mail", "inbox")
	want := "Project Inbox: q\n[URGENT] Build plan\nBlueLake → IcyKnoll\nshared\nOther → IcyKnoll, BlueLake\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("mail inbox = %d %q %q, want %q", code, out, errOut, want)
	}
	if got := m.fetched(t); fmt.Sprint(got) != "[BlueLake IcyKnoll Other]" || fmt.Sprint(m.project) != "[/q]" {
		t.Fatalf("read %v of project %v", got, m.project)
	}
	code, out, _ = run("mail", "inbox", "--agent", "BlueLake", "--project", "/p")
	if code != 0 || out != "Project Inbox: p\nshared\nOther → BlueLake\n" {
		t.Fatalf("mail inbox --agent BlueLake = %d %q", code, out)
	}
	if got := m.fetched(t); fmt.Sprint(got) != "[BlueLake]" {
		t.Fatalf("--agent read %v", got)
	}
}

// --limit is per agent (50 by default), --urgent asks for the urgent
// mail, and --agent and --session-agents keep the agents.
func TestMailInboxSessionFlags(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := swarmMail(t)
	limits := func() []string {
		m.mu.Lock()
		defer m.mu.Unlock()
		var out []string
		for _, f := range m.fetches {
			out = append(out, fmt.Sprint(f["limit"], f["urgent_only"]))
		}
		return out
	}
	run("mail", "inbox", "swarm", "--agent", "BlueLake", "--socket", s.SocketPath)
	if got := limits(); fmt.Sprint(got) != "[50 <nil>]" {
		t.Errorf("--agent: fetches %v", got)
	}
	if got := m.fetched(t); fmt.Sprint(got) != "[BlueLake]" {
		t.Errorf("--agent read %v", got)
	}
	code, out, _ := run("mail", "inbox", "swarm", "--urgent", "--limit", "5", "--socket", s.SocketPath)
	if code != 0 || out != "Project Inbox: p\n[URGENT] Build plan\nBlueLake → IcyKnoll\n" {
		t.Errorf("--urgent = %d %q", code, out)
	}
	if got := limits(); len(got) != 3 || got[0] != "5 true" {
		t.Errorf("--urgent --limit 5: fetches %v", got)
	}
	m.fetched(t)
	run("mail", "inbox", "swarm", "--session-agents", "--socket", s.SocketPath)
	if got := m.fetched(t); fmt.Sprint(got) != "[BlueLake IcyKnoll]" {
		t.Errorf("--session-agents read %v", got)
	}
	// --session-agents without a session takes the calling pane's.
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	if code, _, errOut := run("mail", "inbox", "--session-agents", "--socket", s.SocketPath); code != 0 {
		t.Errorf("--session-agents in w1 = %d %q", code, errOut)
	}
	if got := m.fetched(t); fmt.Sprint(got) != "[BlueLake IcyKnoll]" {
		t.Errorf("--session-agents in w1 read %v", got)
	}
}

func TestMailInboxSessionEmptyAndRefusals(t *testing.T) {
	s, _, _ := waitInstance(t, func(string) map[string]any { return nil })
	m := newProjectMail(t, []string{"IcyKnoll"}, map[string][]map[string]any{"IcyKnoll": {}})
	if code, out, _ := run("mail", "inbox", "swarm", "--socket", s.SocketPath); code != 0 || out != "Inbox empty\n" {
		t.Errorf("empty = %d %q", code, out)
	}
	if code, out, _ := run("mail", "inbox", "swarm", "--json", "--socket", s.SocketPath); code != 0 || fmt.Sprint(envelopeOf(t, out)["result"]) != "[]" {
		t.Errorf("empty --json = %d %s", code, out)
	}
	m.fetched(t)
	// On the shared parser, as ntm's: "Error: <msg>" and exit 1 (the
	// operator's ruling, 2026-10-24: a ported verb takes ntm's codes).
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"nosuch"}, `Error: session "nosuch": no such workspace (nhm list shows the sessions)` + "\n"},
		{[]string{"swarm", "--agent", "NoSuchAgent"}, "fetching inbox for NoSuchAgent"},
		{[]string{"swarm", "extra"}, "Error: accepts at most 1 arg(s), received 2\n"},
		{[]string{"swarm", "--limit", "0"}, "Error: --limit must be greater than 0\n"},
		{[]string{"--session-agents"}, "Error: --session-agents: name the session, or run inside herdr\n"},
		// nhm's own form reads one agent's unread mail.
		{[]string{"--mark-read", "--keep-unread"}, "Error: give --mark-read or --keep-unread, not both\n"},
		{[]string{"swarm", "--keep-unread"}, "they take no session, --urgent or --session-agents"},
		{[]string{"--urgent", "--mark-read"}, "they take no session, --urgent or --session-agents"},
		{[]string{"--session-agents", "--keep-unread"}, "they take no session, --urgent or --session-agents"},
	} {
		t.Setenv("HERDR_WORKSPACE_ID", "")
		code, _, errOut := run(append(append([]string{"mail", "inbox"}, c.args...), "--socket", s.SocketPath)...)
		if code != exitError || !strings.HasPrefix(errOut, "Error: ") || !strings.Contains(errOut, c.want) {
			t.Errorf("mail inbox %v = %d %q, want 1 %q", c.args, code, errOut, c.want)
		}
	}
	// Under --json, an error is ntm's robot error envelope on stdout.
	code, out, errOut := run("mail", "inbox", "--mark-read", "--keep-unread", "--json", "--socket", s.SocketPath)
	if code != exitError || errOut != "" || !strings.Contains(out, `"success": false`) || !strings.Contains(out, "give --mark-read or --keep-unread") {
		t.Errorf("--json error = %d %q %q", code, out, errOut)
	}
}

// A session's project comes from most of its bound agents; on a tie,
// from the one first by slot, read as numbers.
func TestSessionProject(t *testing.T) {
	b := func(slot, key string) bindings.Binding { return bindings.Binding{Slot: slot, MailProjectKey: key} }
	for _, c := range []struct {
		bound []bindings.Binding
		want  string
	}{
		{nil, ""},
		{[]bindings.Binding{b("1.1", "/a"), b("1.2", "/b"), b("1.3", "/b")}, "/b"},
		{[]bindings.Binding{b("10.1", "/a"), b("2.1", "/b")}, "/b"},
		{[]bindings.Binding{b("1.1", ""), b("", "/c")}, "/c"},
	} {
		if got := sessionProject(c.bound); got != c.want {
			t.Errorf("sessionProject(%v) = %q, want %q", c.bound, got, c.want)
		}
	}
}
