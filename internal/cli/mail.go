package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

const mailUsage = `Usage: nhm mail inbox [session] [--agent NAME] [--urgent] [--session-agents] [--limit N] [--project KEY] [--socket PATH] [--json]
       nhm mail inbox (--mark-read | --keep-unread) [--agent NAME] [--project KEY] [--limit N] [--socket PATH] [--json]

ntm's mail inbox: the project's inbox, every agent's recent mail
(--limit per agent, default 50), read or not, each message once with the
agents that got it, oldest first. --agent keeps one agent, --urgent the
urgent mail, --session-agents the agents bound in the session (without
a session, the calling pane's). The session is a workspace id or label.
The project is --project (Agent Mail's project key, the project's
absolute path), else that of the session's bound agents, else
AGENT_MAIL_PROJECT, else the git work tree of the cwd. Unlike ntm's, it
marks nothing read.

--mark-read (nhm's own) lists every unread message sent to one agent (to
or bcc), oldest first, grouped by thread, with bodies; then it marks each
listed message read. cc mail is counted and stays unread. --keep-unread
lists the same and marks nothing. The agent is --agent, else
AGENT_MAIL_AGENT or AGENT_NAME, else the binding of the calling pane.
The AGENT_MAIL_AGENT that nhm spawn set in the pane does not count once
the pane's binding names another agent. The project is --project, else
AGENT_MAIL_PROJECT, else the binding's project, else the git work tree
of the cwd. A listing that reaches --limit (default 200) is refused:
older mail would sink out of sight.
`

func runMail(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "inbox" {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			fmt.Fprint(stdout, mailUsage)
			return exitOK
		}
		fmt.Fprint(stderr, mailUsage)
		return exitRefused
	}
	fs := flag.NewFlagSet("mail inbox", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, mailUsage) }
	agent := fs.String("agent", "", "the Agent Mail name")
	project := fs.String("project", "", "Agent Mail's project key")
	markRead := fs.Bool("mark-read", false, "nhm's form: list one agent's unread mail and mark it read")
	keep := fs.Bool("keep-unread", false, "nhm's form: list one agent's unread mail and mark nothing read")
	limit := fs.Int("limit", 0, "the most messages one listing reads (50 per agent; nhm's form: 200)")
	urgent := fs.Bool("urgent", false, "only the urgent mail")
	sessionAgents := fs.Bool("session-agents", false, "only the agents bound in the session")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args[1:])
	limitSet := false
	fs.Visit(func(f *flag.Flag) { limitSet = limitSet || f.Name == "limit" })
	if err != nil || len(pos) > 1 || (len(pos) == 1 && pos[0] == "") || (limitSet && *limit <= 0) {
		fmt.Fprint(stderr, mailUsage)
		return exitRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch {
	case *markRead && *keep:
		fmt.Fprintf(stderr, "nhm mail inbox: give --mark-read or --keep-unread, not both\n%s", mailUsage)
		return exitRefused
	case (*markRead || *keep) && (len(pos) == 1 || *urgent || *sessionAgents):
		fmt.Fprintf(stderr, "nhm mail inbox: --mark-read and --keep-unread list one agent's unread mail; they take no session, --urgent or --session-agents\n%s", mailUsage)
		return exitRefused
	case !*markRead && !*keep:
		// ntm's mail inbox [session], with or without a session (the
		// operator's rulings: read-only, nhm-k9d, 2026-10-23; the bare
		// form too, nhm-2p6, 2026-10-24).
		o := projectInbox{agent: *agent, project: *project, urgent: *urgent, sessionAgents: *sessionAgents,
			limit: 50, socket: *socket, asJSON: *asJSON}
		if len(pos) == 1 {
			o.session = pos[0]
		}
		if limitSet {
			o.limit = *limit
		}
		return runProjectInbox(ctx, o, stdout, stderr)
	}
	if !limitSet {
		*limit = 200
	}
	env := envelope{Command: "mail inbox"}

	// The instance is optional: it names the calling pane's agent and
	// keeps the wake table in step with what this listing marks read.
	var ie *instanceEnv
	var self *bindings.Binding
	if ie, err = openInstance(*socket); err == nil {
		defer ie.db.Close()
		if pane := ie.callerPane(ctx); pane != "" {
			if b, err := bindings.NewStore(ie.db).ActiveByPane(ctx, pane); err == nil {
				self = &b
			}
		}
	} else {
		ie = nil
	}
	name := inboxAgent(*agent, self, os.Getenv)
	if name == "" {
		return output(stdout, stderr, *asJSON, env, errors.New("no agent: give --agent, or set AGENT_MAIL_AGENT or AGENT_NAME"), exitRefused, nil)
	}
	key := firstOf(*project, os.Getenv("AGENT_MAIL_PROJECT"))
	if key == "" && self != nil && strings.EqualFold(self.MailName, name) {
		key = self.MailProjectKey
	}
	if key == "" {
		key = workTree(ctx)
	}
	// A relative directory becomes its absolute path; a project slug
	// stays as given.
	if !filepath.IsAbs(key) {
		if st, err := os.Stat(key); err == nil && st.IsDir() {
			key, _ = filepath.Abs(key)
		}
	}
	mail, err := agentmail.NewClient("")
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	l, err := wake.Inbox(ctx, mail, key, name, *limit, *keep)
	env.Result = l
	if ie != nil {
		markWakeRead(ctx, ie, key, name, l.MarkedIDs(), &env)
	}
	code := exitOK
	if err != nil {
		code = exitError
	}
	if *asJSON {
		return output(stdout, stderr, true, env, err, code, nil)
	}
	// The listing comes first, also when some marks failed.
	if err == nil || len(l.Messages) > 0 {
		printInbox(stdout, l)
	}
	return output(stdout, stderr, false, env, err, code, nil)
}

// markWakeRead marks the wake rows of the listed messages read, so no
// nudge announces mail the agent has read.
func markWakeRead(ctx context.Context, ie *instanceEnv, projectKey, name string, ids []string, env *envelope) {
	if len(ids) == 0 {
		return
	}
	bs := bindings.NewStore(ie.db)
	b, err := bs.ActiveByName(ctx, projectKey, name)
	if err != nil {
		return
	}
	w, _ := ie.wakeEnv(false)
	if err := wake.MarkRead(ctx, w, b.ID, ids); err != nil {
		env.Warnings = append(env.Warnings, err.Error())
	}
}

// printInbox prints the listing. Mail is text from other agents: control
// characters and escape sequences are dropped before they reach the
// terminal.
func printInbox(w io.Writer, l wake.Listing) {
	for _, thread := range l.Threads() {
		t := stripControl(thread[0].ThreadID)
		if t == "" {
			t = "(none)"
		}
		fmt.Fprintf(w, "== thread %s (%d unread)\n", t, len(thread))
		for _, m := range thread {
			line := fmt.Sprintf("--- #%d %s from %s [%s]", m.ID, stripControl(m.CreatedTS), stripControl(m.From), stripControl(m.Importance))
			if m.AckRequired && m.AckTS == "" {
				line += " [ack required]"
			}
			fmt.Fprintln(w, line)
			fmt.Fprintf(w, "Subject: %s\n\n%s\n\n", stripControl(m.Subject), stripControl(m.BodyMD))
		}
		fmt.Fprintln(w)
	}
	if l.KeptUnread {
		fmt.Fprintf(w, "listed %d unread (left unread)", len(l.Messages))
	} else {
		fmt.Fprintf(w, "listed %d unread, marked %d read", len(l.Messages), l.Marked)
	}
	fmt.Fprintf(w, "; %d cc still unread", l.CCUnread)
	if len(l.AckPending) > 0 {
		var ids []string
		for _, id := range l.AckPending {
			ids = append(ids, fmt.Sprintf("#%d", id))
		}
		fmt.Fprintf(w, "; ack pending: %s", strings.Join(ids, " "))
	}
	fmt.Fprintln(w)
}

// inboxAgent is the agent mail inbox lists: --agent, else AGENT_MAIL_AGENT
// or AGENT_NAME, else the calling pane's binding. Spawn sets
// AGENT_MAIL_AGENT and NHM_AGENT in the pane to the agent's name
// (nhm-4c4). When both still hold that name but the pane's binding now
// names another agent (the pane was reused and bound again), the value is
// spawn's and stale, and the binding names the agent.
func inboxAgent(flag string, self *bindings.Binding, getenv func(string) string) string {
	mailAgent := getenv("AGENT_MAIL_AGENT")
	if self != nil && mailAgent != "" && mailAgent == getenv("NHM_AGENT") && !strings.EqualFold(mailAgent, self.MailName) {
		mailAgent = ""
	}
	name := firstOf(flag, mailAgent, getenv("AGENT_NAME"))
	if name == "" && self != nil {
		name = self.MailName
	}
	return name
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// workTree is the cwd's git work tree, or the cwd.
func workTree(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	// A child of git that holds stdout open cannot hold the call more
	// than WaitDelay past ctx or past git's exit (nhm-fty).
	cmd.WaitDelay = time.Second
	if out, err := cmd.Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return top
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}
