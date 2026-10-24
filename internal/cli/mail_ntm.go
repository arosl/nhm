// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/mail.go
// (newMailCmd, newMailSendCmd, runMailSendOverseer, newMailMarkCmd,
// resolveMailAgentIdentity, parseMessageIDs, markSummary, runMailMark,
// mailMessageMatchesAction, truncateSubject, openEditorForMessage),
// internal/cli/root.go (buildEditorCommandWithFallback, editorTokensSafe),
// internal/cli/util.go (parseEditorCommand) and internal/cli/redaction_io.go
// (RedactionSummary, formatRedactionCategoryCounts) at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: on herdr, a
// session is a workspace (nhm's resolveSession), its project is that of its
// bound agents, else the cwd's, and the calling pane's agent is its binding;
// the redaction scan is nhm's (internal/redaction) in ntm's default mode,
// warn, since nhm has no [redaction] config yet (nhm-2p6.20), and
// --prepared-redaction is refused until redact prepare-mail lands; the
// inbox fetch of read and ack marks nothing read; the inbox sub-verb is
// nhm's (mail.go); the output goes to cobra's writers.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/redaction"
)

// mailVerbUsage is `nhm mail`'s help; `nhm mail inbox --help` prints
// mailUsage.
const mailVerbUsage = `Usage: nhm mail send <session> [message] [--to NAME]... [-s|--subject S] [--thread ID] [--all] [-f|--file PATH]
       nhm mail inbox [session] [--agent NAME] [--urgent] [--session-agents] [--limit N]
       nhm mail inbox (--mark-read | --keep-unread) [--agent NAME]
       nhm mail read|ack <session> [id...] [--agent NAME] [--urgent] [--from NAME] [--all] [--limit N]

ntm's Human Overseer mail over Agent Mail. Each form takes --json.

send sends one message as HumanOverseer, at high importance and past the
agents' contact policies, with a preamble that asks them to put it first:
to each --to agent, or with --all to every agent of the session's project.
Without a message or --file it opens $EDITOR, whose first heading is the
subject; else the subject is the message's first line.

inbox lists the project's mail (nhm mail inbox --help).

read and ack mark messages read or acknowledged for --agent (else the
calling pane's agent, else $AGENT_NAME): the ids given, or those that
--all, --urgent or --from select from its inbox (the newest --limit, 50).

The session is a herdr workspace; its project is that of its bound
agents, else AGENT_MAIL_PROJECT, else the cwd's work tree.
`

func newMailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail",
		Short: "Human Overseer messaging to agents",
		Long: `Send Human Overseer messages to AI agents via Agent Mail.

This command provides the CLI interface for the Human Overseer functionality
in Agent Mail. Messages sent this way:
  - Come from the special "HumanOverseer" identity
  - Bypass contact policies (humans can always reach agents)
  - Auto-inject a preamble telling agents to prioritize your instructions
  - Are marked as high importance

This is the CLI equivalent of the Agent Mail web UI at /mail/{project}/overseer/compose

Examples:
  nhm mail send myproject --to GreenCastle "Please review the API changes"
  nhm mail send myproject --all "Checkpoint: sync and report status"`,
	}
	cmd.AddCommand(newMailSendCmd(), newMailInboxCmd(), newMailMarkCmd(mailActionRead), newMailMarkCmd(mailActionAck))
	return cmd
}

// newMailInboxCmd is mail inbox (mail.go) on the shared parser.
func newMailInboxCmd() *cobra.Command {
	var f mailInboxFlags
	cmd := &cobra.Command{
		Use:   "inbox [session]",
		Short: "Show aggregate project inbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.limitSet, f.asJSON = cmd.Flags().Changed("limit"), wantJSON(cmd)
			return runMailInbox(f, args, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.agent, "agent", "", "Filter by specific agent name")
	fl.BoolVar(&f.urgent, "urgent", false, "Show only urgent messages")
	fl.BoolVar(&f.sessionAgents, "session-agents", false, "Filter to agents currently in session")
	fl.IntVar(&f.limit, "limit", 50, "Max messages to show")
	fl.StringVar(&f.project, "project", "", "Agent Mail's project key (nhm)")
	fl.BoolVar(&f.markRead, "mark-read", false, "list one agent's unread mail and mark it read (nhm)")
	fl.BoolVar(&f.keep, "keep-unread", false, "list one agent's unread mail and mark nothing read (nhm)")
	fl.StringVar(&f.socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

func newMailSendCmd() *cobra.Command {
	var (
		to                []string
		subject, threadID string
		all               bool
		fromFile          string
		prepared          string
		socket            string
	)
	cmd := &cobra.Command{
		Use:   "send <session> [message]",
		Short: "Send Human Overseer message to agents",
		Long: `Send a Human Overseer message to agents via Agent Mail.

This uses the Agent Mail Human Overseer functionality, which:
  - Sends from the special "HumanOverseer" identity
  - Bypasses contact policies (humans can always reach any agent)
  - Auto-inject a preamble telling agents to prioritize your instructions
  - Marks all messages as high importance

Recipients must be Agent Mail agent names (e.g., "GreenCastle", "BlueLake").
Use --all to send to all registered agents in the project.

If no message body is provided, opens $EDITOR for composition.

Examples:
  nhm mail send myproject --to GreenCastle "Please review the API changes"
  nhm mail send myproject --all "Stop current work and checkpoint"
  nhm mail send myproject --to BlueLake --to RedStone --thread FEAT-123 "Status update"
  nhm mail send myproject --all --file ./instructions.md`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if prepared != "" {
				// The handle is the body: no body or file beside it, and
				// never a subject cut from a token's body.
				if fromFile != "" || len(args) > 1 {
					return errors.New("--prepared-redaction is mutually exclusive with --file and positional body")
				}
				if strings.TrimSpace(subject) == "" {
					return errors.New("--prepared-redaction requires an explicit --subject to avoid leaking the raw token prefix as the auto-derived subject")
				}
				return notBuiltError{flag: "prepared-redaction", bead: "nhm-2p6.20"}
			}
			var body string
			switch {
			case fromFile != "":
				content, err := os.ReadFile(fromFile)
				if err != nil {
					return fmt.Errorf("reading file: %w", err)
				}
				body = string(content)
			case len(args) > 1:
				body = strings.Join(args[1:], " ")
			default:
				b, s, err := openEditorForMessage(subject, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				if err != nil {
					return fmt.Errorf("composing message: %w", err)
				}
				body = b
				if subject == "" && s != "" {
					subject = s
				}
			}
			if strings.TrimSpace(body) == "" {
				return errors.New("message body cannot be empty")
			}
			return runMailSendOverseer(cmd, socket, args[0], to, subject, body, threadID, all)
		},
	}
	fl := cmd.Flags()
	fl.StringArrayVar(&to, "to", nil, "recipient agent name (e.g., GreenCastle)")
	fl.StringVarP(&subject, "subject", "s", "", "message subject (auto-derived if not provided)")
	fl.StringVar(&threadID, "thread", "", "thread ID for conversation continuity")
	fl.BoolVar(&all, "all", false, "send to all registered agents in project")
	fl.StringVarP(&fromFile, "file", "f", "", "read message body from file")
	fl.StringVar(&prepared, "prepared-redaction", "", "Consume a token-handle from `nhm redact prepare-mail` and use its stashed body as the message")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// mailScope is ntm's Agent Mail scope of a session, on herdr: the session's
// workspace and the project of its bound agents, else AGENT_MAIL_PROJECT
// or the cwd's work tree. self is the calling pane's binding when that
// pane is in the session.
type mailScope struct {
	session, workspace, project string
	self                        *bindings.Binding
}

func resolveMailScope(ctx context.Context, socket, name string, asJSON bool, stderr io.Writer) (mailScope, error) {
	s := mailScope{session: name}
	var bound []bindings.Binding
	if ie, err := openInstance(socket); err == nil {
		defer ie.db.Close()
		r, err := resolveSession(ctx, ie, name, asJSON, io.Discard, stderr)
		var re *session.ResolveExplicitSessionNameError
		switch {
		case err == nil:
			s.session, s.workspace = r.Session, r.WorkspaceID
			store := bindings.NewStore(ie.db)
			if bound, err = store.List(ctx, bindings.Filter{WorkspaceID: s.workspace, Statuses: []bindings.Status{bindings.Active}}); err != nil {
				return s, err
			}
			if pane := ie.callerPane(ctx); pane != "" {
				if b, err := store.ActiveByPane(ctx, pane); err == nil && b.WorkspaceID == s.workspace {
					s.self = &b
				}
			}
		case errors.As(err, &re) && re.Kind != session.ErrorAmbiguous:
			// As ntm's: a session that is not there does not stop the mail.
			fmt.Fprintf(stderr, "Warning: session '%s' not found (continuing anyway)\n", name)
		default:
			return s, err
		}
	}
	s.project = firstOf(sessionProject(bound), os.Getenv("AGENT_MAIL_PROJECT"))
	if s.project == "" {
		s.project = workTree(ctx)
	}
	if !filepath.IsAbs(s.project) {
		if st, err := os.Stat(s.project); err == nil && st.IsDir() {
			s.project, _ = filepath.Abs(s.project)
		}
	}
	return s, nil
}

// mailClient is the Agent Mail client, once it answers its health check.
func mailClient(ctx context.Context) (*agentmail.Client, error) {
	c, err := agentmail.NewClient("")
	if err != nil {
		return nil, err
	}
	if err := c.Available(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("checking agent mail availability: %w", ctx.Err())
		}
		return nil, fmt.Errorf("agent mail server not available at %s\nstart the server with: am serve-http --host 127.0.0.1 --no-tui --no-auth\nreason: %w", c.URL, err)
	}
	return c, nil
}

// RedactionSummary is ntm's account of the secrets that a scan found.
type RedactionSummary struct {
	Mode       string         `json:"mode"`
	Findings   int            `json:"findings"`
	Categories map[string]int `json:"categories,omitempty"`
	Action     string         `json:"action,omitempty"`
}

func runMailSendOverseer(cmd *cobra.Command, socket, name string, to []string, subject, body, threadID string, all bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	asJSON := wantJSON(cmd)
	scope, err := resolveMailScope(ctx, socket, name, asJSON, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	mail, err := mailClient(ctx)
	if err != nil {
		return err
	}
	slug, err := mail.EnsureProject(ctx, scope.project)
	if err != nil {
		return fmt.Errorf("ensuring project: %w", err)
	}
	recipients := to
	if all {
		agents, err := mail.ListAgents(ctx, scope.project)
		if err != nil {
			return fmt.Errorf("listing agents: %w", err)
		}
		recipients = nil
		for _, a := range agents {
			if a != agentmail.HumanOverseer {
				recipients = append(recipients, a)
			}
		}
		if len(recipients) == 0 {
			return errors.New("no agents registered in project (agents must register with Agent Mail first)")
		}
	}
	if len(recipients) == 0 {
		return errors.New("no recipients specified (use --to <agent-name> or --all)")
	}
	if subject == "" {
		subject = truncateSubject(body, 60)
	}
	// ntm's default redaction mode, warn: the message goes as written,
	// with a warning for each kind of secret found.
	var summary *RedactionSummary
	var warnings []string
	cats := map[string]int{}
	n := 0
	for _, text := range []string{body, subject} {
		spans, _ := redaction.Find(text)
		for _, sp := range spans {
			cats[string(sp.Category)]++
			n++
		}
	}
	if n > 0 {
		summary = &RedactionSummary{Mode: "warn", Findings: n, Categories: cats, Action: "warn"}
		msg := fmt.Sprintf("Warning: detected %d potential secret(s) (%s); message will be sent unmodified (mode=warn)", n, formatRedactionCategoryCounts(cats))
		fmt.Fprintln(cmd.ErrOrStderr(), msg)
		warnings = append(warnings, msg)
	}
	if slug == "" {
		slug = agentmail.ProjectSlugFromPath(scope.project)
	}
	r, err := mail.SendOverseer(ctx, agentmail.OverseerMessage{ProjectKey: scope.project, ProjectSlug: slug,
		Recipients: recipients, Subject: subject, BodyMD: body, ThreadID: threadID})
	if err != nil {
		return fmt.Errorf("sending overseer message: %w", err)
	}
	out := cmd.OutOrStdout()
	if asJSON {
		payload := map[string]any{"success": r.Success, "recipients": r.Recipients, "subject": subject,
			"message_id": r.MessageID, "sent_at": r.SentAt, "overseer": true}
		if summary != nil {
			payload["redaction"] = summary
		}
		if len(warnings) > 0 {
			payload["warnings"] = warnings
		}
		return encodeJSONIndent(out, payload)
	}
	fmt.Fprintf(out, "🚨 Human Overseer message sent to %d agent(s)\n", len(r.Recipients))
	for _, rc := range r.Recipients {
		fmt.Fprintf(out, "  → %s\n", rc)
	}
	fmt.Fprintf(out, "\nAgents will receive this with high priority and a directive preamble.\n")
	return nil
}

func encodeJSONIndent(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func formatRedactionCategoryCounts(categories map[string]int) string {
	keys := make([]string, 0, len(categories))
	for k := range categories {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, categories[k]))
	}
	return strings.Join(parts, ", ")
}

// truncateSubject is a subject from a body: its first line, without a
// heading's marks, cut to maxLen.
func truncateSubject(body string, maxLen int) string {
	lines := strings.SplitN(body, "\n", 2)
	subject := strings.TrimSpace(lines[0])
	subject = strings.TrimPrefix(subject, "# ")
	subject = strings.TrimPrefix(subject, "## ")
	subject = strings.TrimPrefix(subject, "### ")
	if len(subject) > maxLen {
		return subject[:maxLen-3] + "..."
	}
	return subject
}

// openEditorForMessage lets the user write the message in $EDITOR (else
// $VISUAL, else nano). A first line that is a heading becomes the
// subject; comment lines are dropped.
func openEditorForMessage(existingSubject string, stdin io.Reader, stdout, stderr io.Writer) (body, subject string, err error) {
	tmp, err := os.CreateTemp("", "nhm-mail-*.md")
	if err != nil {
		return "", "", fmt.Errorf("creating temp file: %w", err)
	}
	path := tmp.Name()
	defer os.Remove(path)
	template := "# Subject\n\n<!-- Write your message below. First line starting with # becomes the subject. -->\n\n"
	if existingSubject != "" {
		template = fmt.Sprintf("# %s\n\n<!-- Edit your message below. -->\n\n", existingSubject)
	}
	if _, err := tmp.WriteString(template); err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("writing template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("writing template: %w", err)
	}
	cmd, err := buildEditorCommand(path, "nano")
	if err != nil {
		return "", "", fmt.Errorf("configuring editor: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("running editor: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("reading composed message: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	var bodyLines []string
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "<!--") || strings.HasSuffix(t, "-->") {
			continue
		}
		if subject == "" && strings.HasPrefix(t, "#") {
			if subject = strings.TrimSpace(strings.TrimLeft(t, "#")); subject == "Subject" {
				subject = "" // the placeholder, unchanged
			}
			continue
		}
		if len(bodyLines) == 0 && t == "" {
			continue
		}
		bodyLines = append(bodyLines, lines[i])
	}
	return strings.TrimSpace(strings.Join(bodyLines, "\n")), subject, nil
}

// buildEditorCommand is $EDITOR, else $VISUAL, else fallback, on path; an
// editor with shell metacharacters is the fallback.
func buildEditorCommand(path, fallback string) (*exec.Cmd, error) {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = fallback
	}
	name, args := parseEditorCommand(editor)
	if name == "" || !editorTokensSafe(append([]string{name}, args...)) {
		name, args = parseEditorCommand(fallback)
	}
	if name == "" {
		return nil, errors.New("editor not configured")
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("editor not found: %w", err)
	}
	return &exec.Cmd{Path: bin, Args: append(append([]string{bin}, args...), path)}, nil
}

func editorTokensSafe(tokens []string) bool {
	for _, t := range tokens {
		if strings.ContainsAny(t, ";&|<>`$\n\r") {
			return false
		}
	}
	return true
}

// parseEditorCommand splits an editor command as a shell would its words:
// quotes and backslashes, no expansion.
func parseEditorCommand(editor string) (string, []string) {
	editor = strings.TrimSpace(editor)
	if editor == "" {
		return "", nil
	}
	var (
		parts        []string
		current      strings.Builder
		quote        rune
		escaped      bool
		tokenStarted bool
	)
	flush := func() {
		if !tokenStarted && current.Len() == 0 {
			return
		}
		parts = append(parts, current.String())
		current.Reset()
		tokenStarted = false
	}
	for _, r := range editor {
		switch {
		case escaped:
			current.WriteRune(r)
			tokenStarted, escaped = true, false
		case quote == '\'':
			if r == '\'' {
				quote = 0
				continue
			}
			current.WriteRune(r)
			tokenStarted = true
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				current.WriteRune(r)
				tokenStarted = true
			}
		default:
			switch {
			case r == '\'' || r == '"':
				quote, tokenStarted = r, true
			case r == '\\':
				escaped, tokenStarted = true, true
			case unicode.IsSpace(r):
				flush()
			default:
				current.WriteRune(r)
				tokenStarted = true
			}
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

type mailAction string

const (
	mailActionRead mailAction = "read"
	mailActionAck  mailAction = "ack"
)

func newMailMarkCmd(action mailAction) *cobra.Command {
	var (
		agent, from, socket string
		urgent, all         bool
		limit               int
	)
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s <session> [message-id...]", action),
		Short: fmt.Sprintf("Mark Agent Mail messages as %s", action),
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			scope, err := resolveMailScope(ctx, socket, args[0], wantJSON(cmd), cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			name, err := mailAgentIdentity(agent, scope)
			if err != nil {
				return err
			}
			if limit <= 0 {
				return errors.New("--limit must be greater than 0")
			}
			ids, err := parseMessageIDs(args[1:])
			if err != nil {
				return err
			}
			return runMailMark(ctx, cmd, scope.project, name, action, ids, urgent, from, all, limit)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&agent, "agent", "", "agent name to mark messages for (defaults to $AGENT_NAME)")
	fl.BoolVar(&urgent, "urgent", false, "only mark urgent messages")
	fl.StringVar(&from, "from", "", "only mark messages from this sender")
	fl.BoolVar(&all, "all", false, "mark all matching messages (ignores positional IDs)")
	fl.IntVar(&limit, "limit", 50, "max messages to consider when using --all/filters")
	fl.StringVar(&socket, "socket", "", "herdr API socket (nhm)")
	return cmd
}

// mailAgentIdentity is the agent whose mail read and ack mark: --agent,
// else the calling pane's agent when that pane is in the session, else
// $AGENT_NAME.
func mailAgentIdentity(agent string, scope mailScope) (string, error) {
	if a := strings.TrimSpace(agent); a != "" {
		return a, nil
	}
	if scope.self != nil && strings.TrimSpace(scope.self.MailName) != "" {
		return scope.self.MailName, nil
	}
	if a := strings.TrimSpace(os.Getenv("AGENT_NAME")); a != "" {
		return a, nil
	}
	return "", errors.New("--agent is required (or set AGENT_NAME)")
}

func parseMessageIDs(raw []string) ([]int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(raw))
	for _, s := range raw {
		id, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid message id %q", s)
		}
		ids = append(ids, int64(id))
	}
	return ids, nil
}

// markSummary is ntm's JSON of mail read and ack.
type markSummary struct {
	Action    string  `json:"action"`
	Agent     string  `json:"agent"`
	Processed int     `json:"processed"`
	Skipped   int     `json:"skipped"`
	Errors    int     `json:"errors"`
	IDs       []int64 `json:"ids"`
}

func runMailMark(ctx context.Context, cmd *cobra.Command, project, agent string, action mailAction, ids []int64, urgent bool, from string, all bool, limit int) error {
	mail, err := mailClient(ctx)
	if err != nil {
		return err
	}
	if _, err := mail.EnsureProject(ctx, project); err != nil {
		return fmt.Errorf("ensuring project: %w", err)
	}
	if len(ids) == 0 && !all && !urgent && from == "" {
		return errors.New("provide message IDs or use --all/--urgent/--from to select messages")
	}
	asJSON := wantJSON(cmd)
	out := cmd.OutOrStdout()
	if len(ids) == 0 {
		// Unlike ntm's, the fetch marks nothing read: read and ack mark
		// only what they select.
		inbox, err := mail.RecentInbox(ctx, project, agent, limit, urgent)
		if err != nil {
			return fmt.Errorf("fetching inbox: %w", err)
		}
		for _, m := range inbox {
			if from != "" && !strings.EqualFold(m.From, from) {
				continue
			}
			if (action == mailActionRead && m.ReadTS != "") || (action == mailActionAck && !m.AckRequired) {
				continue
			}
			ids = append(ids, m.ID)
		}
		if len(ids) == 0 {
			if asJSON {
				return encodeJSONIndent(out, markSummary{Action: string(action), Agent: agent})
			}
			fmt.Fprintln(out, "No matching messages found.")
			return nil
		}
	}
	var processed, errs int
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("marking messages: %w", err)
		}
		var err error
		if action == mailActionRead {
			err = mail.MarkRead(ctx, project, agent, id)
		} else {
			err = mail.Acknowledge(ctx, project, agent, id)
		}
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("marking message %d: %w", id, err)
			}
			errs++
			if !asJSON {
				fmt.Fprintf(cmd.ErrOrStderr(), "⚠ message %d: %v\n", id, err)
			}
			continue
		}
		processed++
		if !asJSON {
			if action == mailActionRead {
				fmt.Fprintf(out, "✓ Message %d marked as read\n", id)
			} else {
				fmt.Fprintf(out, "✓ Message %d acknowledged\n", id)
			}
		}
	}
	if asJSON {
		return encodeJSONIndent(out, markSummary{Action: string(action), Agent: agent, Processed: processed,
			Skipped: len(ids) - processed, Errors: errs, IDs: ids})
	}
	return nil
}
