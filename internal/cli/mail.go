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

const mailUsage = `Usage: nhm mail inbox [--agent NAME] [--project KEY] [--keep-unread] [--limit N] [--socket PATH] [--json]

List every unread message sent to the agent (to or bcc), oldest first,
grouped by thread, with bodies; then mark each listed message read. cc
mail is counted and stays unread. --keep-unread marks nothing.

The agent is --agent, else AGENT_MAIL_AGENT or AGENT_NAME, else the
binding of the calling pane. The project is --project (Agent Mail's
project key, the project's absolute path), else AGENT_MAIL_PROJECT, else
the binding's project, else the git work tree of the cwd. A listing that
reaches --limit (default 200) is refused: older mail would sink out of
sight.
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
	keep := fs.Bool("keep-unread", false, "mark nothing read")
	limit := fs.Int("limit", 200, "the most messages one listing reads")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil || len(pos) > 0 || *limit <= 0 {
		fmt.Fprint(stderr, mailUsage)
		return exitRefused
	}
	env := envelope{Command: "mail inbox"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

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
	name := firstOf(*agent, os.Getenv("AGENT_MAIL_AGENT"), os.Getenv("AGENT_NAME"))
	if name == "" && self != nil {
		name = self.MailName
	}
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
	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output(); err == nil {
		if top := strings.TrimSpace(string(out)); top != "" {
			return top
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}
