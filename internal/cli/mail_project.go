package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// projectInbox is ntm's mail inbox [session] form: the project's mail,
// read-only (the operator's ruling, nhm-k9d, 2026-10-23).
type projectInbox struct {
	session       string
	agent         string
	project       string
	urgent        bool
	sessionAgents bool
	limit         int
	socket        string
	asJSON        bool
}

// ProjectMessage is one message of the project's inbox: ntm's
// aggregatedMessage, with the agents that got it.
type ProjectMessage struct {
	ID          int64    `json:"id"`
	Subject     string   `json:"subject"`
	From        string   `json:"from"`
	CreatedTS   string   `json:"created_ts"`
	Importance  string   `json:"importance"`
	AckRequired bool     `json:"ack_required"`
	Kind        string   `json:"kind"`
	Recipients  []string `json:"recipients"`
}

// projectInboxReaders bounds the inbox reads at once.
const projectInboxReaders = 8

// runProjectInbox is ntm's mail inbox [session]: every agent's recent
// mail in the session's project, read or not, each message once with the
// agents that got it, oldest first. Unlike ntm's, it marks nothing read:
// ntm's fetch would mark every agent's listed mail read on this Agent
// Mail, and nhm maintain wakes agents on unread mail.
func runProjectInbox(ctx context.Context, o projectInbox, stdout, stderr io.Writer) int {
	env := envelope{Command: "mail inbox"}
	session := o.session
	if session == "" && o.sessionAgents {
		if session = os.Getenv("HERDR_WORKSPACE_ID"); session == "" {
			return output(stdout, stderr, o.asJSON, env, errors.New("--session-agents: name the session, or run inside herdr"), exitRefused, nil)
		}
	}
	var bound []bindings.Binding
	if session != "" {
		ie, err := openInstance(o.socket)
		if err != nil {
			return output(stdout, stderr, o.asJSON, env, err, exitError, nil)
		}
		defer ie.db.Close()
		ws, err := sessionWorkspace(ctx, ie.herdr, session)
		if err != nil {
			return output(stdout, stderr, o.asJSON, env, fmt.Errorf("%w (nhm list shows the sessions)", err), exitRefused, nil)
		}
		if bound, err = bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}}); err != nil {
			return output(stdout, stderr, o.asJSON, env, err, exitError, nil)
		}
	}
	key := firstOf(o.project, sessionProject(bound))
	if key == "" && session != "" {
		return output(stdout, stderr, o.asJSON, env, fmt.Errorf("session %s has no bound agent to name its project: give --project", session), exitRefused, nil)
	}
	if key == "" {
		key = firstOf(os.Getenv("AGENT_MAIL_PROJECT"), workTree(ctx))
	}
	if !filepath.IsAbs(key) {
		if st, err := os.Stat(key); err == nil && st.IsDir() {
			key, _ = filepath.Abs(key)
		}
	}
	mail, err := agentmail.NewClient("")
	if err != nil {
		return output(stdout, stderr, o.asJSON, env, err, exitError, nil)
	}
	names := []string{o.agent}
	if o.agent == "" {
		all, err := mail.ListAgents(ctx, key)
		if err != nil {
			return output(stdout, stderr, o.asJSON, env, fmt.Errorf("listing agents: %w", err), exitError, nil)
		}
		names = names[:0]
		for _, n := range all {
			if n != "HumanOverseer" {
				names = append(names, n)
			}
		}
	}
	if o.sessionAgents {
		in := map[string]bool{}
		for _, b := range bound {
			in[strings.ToLower(b.MailName)] = true
		}
		kept := names[:0]
		for _, n := range names {
			if in[strings.ToLower(n)] {
				kept = append(kept, n)
			}
		}
		if names = kept; len(names) == 0 {
			return output(stdout, stderr, o.asJSON, env, fmt.Errorf("no agents found in session '%s'", session), exitError, nil)
		}
	}
	msgs, err := readProjectInbox(ctx, mail, key, names, o.limit, o.urgent)
	if err != nil {
		return output(stdout, stderr, o.asJSON, env, err, exitError, nil)
	}
	env.Result = msgs
	return output(stdout, stderr, o.asJSON, env, nil, exitOK, func(w io.Writer) {
		if len(msgs) == 0 {
			fmt.Fprintln(w, "Inbox empty")
			return
		}
		fmt.Fprintf(w, "Project Inbox: %s\n", mailField(filepath.Base(key)))
		for _, m := range msgs {
			prefix := ""
			if strings.EqualFold(m.Importance, "urgent") || strings.EqualFold(m.Importance, "high") {
				prefix = "[URGENT] "
			}
			fmt.Fprintf(w, "%s%s\n", prefix, mailField(m.Subject))
			var to []string
			for _, r := range m.Recipients {
				to = append(to, mailField(r))
			}
			if from := mailField(m.From); from != "" || len(to) > 0 {
				fmt.Fprintf(w, "%s → %s\n", from, strings.Join(to, ", "))
			}
		}
	})
}

// sessionProject is the project key of most of the session's bound
// agents (on a tie, the one that comes first by slot), or "".
func sessionProject(bound []bindings.Binding) string {
	bound = append([]bindings.Binding(nil), bound...)
	sort.SliceStable(bound, func(i, j int) bool {
		a, aerr := bindings.ParseSlot(bound[i].Slot)
		b, berr := bindings.ParseSlot(bound[j].Slot)
		if aerr != nil || berr != nil {
			return aerr == nil && berr != nil
		}
		return a.Tab < b.Tab || (a.Tab == b.Tab && a.Pane < b.Pane)
	})
	count := map[string]int{}
	best := ""
	for _, b := range bound {
		if b.MailProjectKey == "" {
			continue
		}
		count[b.MailProjectKey]++
		if best == "" || count[b.MailProjectKey] > count[best] {
			best = b.MailProjectKey
		}
	}
	return best
}

// readProjectInbox reads each agent's recent mail, a few at once, and
// joins the copies of a message: the first copy's fields, and each agent
// that got it, in the order of names. Messages come oldest first, by id.
func readProjectInbox(ctx context.Context, mail *agentmail.Client, key string, names []string, limit int, urgent bool) ([]ProjectMessage, error) {
	got := make([][]agentmail.Message, len(names))
	errs := make([]error, len(names))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(projectInboxReaders, len(names)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				got[i], errs[i] = mail.RecentInbox(ctx, key, names[i], limit, urgent)
			}
		}()
	}
	for i := range names {
		next <- i
	}
	close(next)
	wg.Wait()
	byID := map[int64]*ProjectMessage{}
	for i, name := range names {
		if errs[i] != nil {
			return nil, fmt.Errorf("fetching inbox for %s: %w", name, errs[i])
		}
		for _, m := range got[i] {
			pm, ok := byID[m.ID]
			if !ok {
				pm = &ProjectMessage{ID: m.ID, Subject: m.Subject, From: m.From, CreatedTS: m.CreatedTS, Importance: m.Importance,
					AckRequired: m.AckRequired, Kind: m.Kind}
				byID[m.ID] = pm
			}
			pm.Recipients = append(pm.Recipients, name)
		}
	}
	out := make([]ProjectMessage, 0, len(byID))
	for _, pm := range byID {
		out = append(out, *pm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// mailField is a mail field on one line for the terminal, as ntm shows
// it: no control characters or escape sequences, whitespace collapsed.
func mailField(s string) string {
	return strings.Join(strings.Fields(stripControl(s)), " ")
}
