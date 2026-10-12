package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// InboxMessage is one message of fetch_inbox's answer.
type InboxMessage struct {
	ID          int64  `json:"id"`
	Subject     string `json:"subject"`
	From        string `json:"from"`
	ThreadID    string `json:"thread_id,omitempty"`
	CreatedTS   string `json:"created_ts"`
	Importance  string `json:"importance"`
	AckRequired bool   `json:"ack_required"`
	AckTS       string `json:"ack_ts,omitempty"`
	ReadTS      string `json:"read_ts,omitempty"`
	// Kind is how the agent got it: to, cc or bcc.
	Kind   string `json:"kind"`
	BodyMD string `json:"body_md,omitempty"`
}

// unread peeks at an agent's unread mail: fetch_inbox with mark_read
// false, so nothing is consumed (plan-v8 2.5). full says the answer
// reached the limit, so it may be cut short.
func unread(ctx context.Context, mail Mail, projectKey, agent string, limit int, bodies bool) ([]InboxMessage, bool, error) {
	var msgs []InboxMessage
	err := mail.Call(ctx, "fetch_inbox", map[string]any{"project_key": projectKey, "agent_name": agent,
		"unread_only": true, "mark_read": false, "limit": limit, "include_bodies": bodies}, &msgs)
	var te *agentmail.ToolError
	if errors.As(err, &te) && strings.Contains(strings.ToLower(te.Message), "not found") {
		return nil, false, fmt.Errorf("%w: %s in %s", agentmail.ErrNotRegistered, agent, projectKey)
	}
	if err != nil {
		return nil, false, err
	}
	return msgs, len(msgs) >= limit, nil
}

// ReadInbox reads a recipient's unread mail without consuming it: mail
// that no hook saw becomes pending, and pending or announced rows of mail
// the recipient has read are marked read. A listing at the limit marks
// nothing read, since it may be cut short.
func ReadInbox(ctx context.Context, env Env, b bindings.Binding) (added, read int, err error) {
	cfg := env.config()
	msgs, full, err := unread(ctx, env.Mail, b.MailProjectKey, b.MailName, cfg.InboxLimit, false)
	if err != nil {
		return 0, 0, err
	}
	list := make([]Message, 0, len(msgs))
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.ReadTS != "" {
			continue
		}
		list = append(list, Message{ID: m.ID, ThreadID: m.ThreadID, Sender: m.From})
		ids = append(ids, strconv.FormatInt(m.ID, 10))
	}
	if added, err = Record(ctx, env, b, list); err != nil {
		return 0, 0, err
	}
	if full {
		return added, 0, nil
	}
	keep, _ := json.Marshal(ids)
	res, err := env.DB.SQL().ExecContext(ctx, `UPDATE wake SET last_outcome = ?, announced_at = COALESCE(announced_at, ?)
		WHERE binding_id = ? AND COALESCE(last_outcome, '') NOT IN (?, ?)
		AND message_id NOT IN (SELECT value FROM json_each(?))`,
		OutcomeRead, store.FormatTime(env.now()), b.ID, OutcomeRead, send.StatusAttemptStarted, string(keep))
	if err != nil {
		return added, 0, fmt.Errorf("wake: inbox %s: %w", b.MailName, err)
	}
	n, _ := res.RowsAffected()
	return added, int(n), nil
}

// Listing is `nhm mail inbox`: the unread mail sent to the agent (to or
// bcc), oldest first, by thread.
type Listing struct {
	Agent      string         `json:"agent"`
	ProjectKey string         `json:"project_key"`
	Messages   []InboxMessage `json:"messages"`
	// CCUnread counts unread cc mail; it is not listed and stays unread.
	CCUnread int `json:"cc_unread"`
	// Marked counts the listed messages marked read.
	Marked     int     `json:"marked_read"`
	AckPending []int64 `json:"ack_pending"`
	KeptUnread bool    `json:"kept_unread"`
	MarkFailed []int64 `json:"mark_failed"`
}

// Threads groups the messages by thread, in the order of each thread's
// oldest message.
func (l Listing) Threads() [][]InboxMessage {
	var order []string
	groups := map[string][]InboxMessage{}
	for _, m := range l.Messages {
		if _, ok := groups[m.ThreadID]; !ok {
			order = append(order, m.ThreadID)
		}
		groups[m.ThreadID] = append(groups[m.ThreadID], m)
	}
	out := make([][]InboxMessage, len(order))
	for i, t := range order {
		out[i] = groups[t]
	}
	return out
}

// Inbox lists an agent's unread mail sent to it (to or bcc), oldest
// first and with bodies, and then marks each listed message read unless
// keepUnread. It refuses a listing at the limit: older mail would sink
// out of sight. cc mail is counted and left unread.
func Inbox(ctx context.Context, mail Mail, projectKey, agent string, limit int, keepUnread bool) (Listing, error) {
	l := Listing{Agent: agent, ProjectKey: projectKey, Messages: []InboxMessage{}, AckPending: []int64{},
		KeptUnread: keepUnread, MarkFailed: []int64{}}
	msgs, full, err := unread(ctx, mail, projectKey, agent, limit, true)
	if err != nil {
		return l, err
	}
	if full {
		return l, fmt.Errorf("Agent Mail returned %d messages, so the listing may be cut short; give a larger --limit", limit)
	}
	for _, m := range msgs {
		switch {
		case m.ReadTS != "":
		case m.Kind == "cc":
			l.CCUnread++
		default:
			l.Messages = append(l.Messages, m)
		}
	}
	sort.Slice(l.Messages, func(i, j int) bool { return l.Messages[i].ID < l.Messages[j].ID })
	for _, m := range l.Messages {
		if m.AckRequired && m.AckTS == "" {
			l.AckPending = append(l.AckPending, m.ID)
		}
	}
	if keepUnread {
		return l, nil
	}
	for _, m := range l.Messages {
		err := mail.Call(ctx, "mark_message_read", map[string]any{"project_key": projectKey, "agent_name": agent,
			"message_id": m.ID}, nil)
		if err != nil {
			l.MarkFailed = append(l.MarkFailed, m.ID)
			continue
		}
		l.Marked++
	}
	if len(l.MarkFailed) > 0 {
		return l, fmt.Errorf("could not mark %d message(s) read", len(l.MarkFailed))
	}
	return l, nil
}

// MarkedIDs returns the ids that Inbox marked read.
func (l Listing) MarkedIDs() []string {
	failed := map[int64]bool{}
	for _, id := range l.MarkFailed {
		failed[id] = true
	}
	var out []string
	if l.KeptUnread {
		return out
	}
	for _, m := range l.Messages {
		if !failed[m.ID] {
			out = append(out, strconv.FormatInt(m.ID, 10))
		}
	}
	return out
}
