package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// The three paths of 2.8 that fill the pending table and nudge from it.

// sentTools are the Agent Mail tools whose PostToolUse hook is
// `nhm-hook mail-sent`.
var sentTools = map[string]bool{"send_message": true, "reply_message": true}

// SentTool returns the Agent Mail tool a hook's tool name calls
// ("mcp__<server>__send_message"), or "" for any other tool.
func SentTool(toolName string) string {
	name := toolName
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if sentTools[name] {
		return name
	}
	return ""
}

// Sent is one delivered message, as the send tool reported it.
type Sent struct {
	ProjectKey string
	Message    Message
	Recipients []string
}

// sentPayload is a message payload in send_message's answer, and
// reply_message's whole answer.
type sentPayload struct {
	ID       int64    `json:"id"`
	ThreadID *string  `json:"thread_id"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	CC       []string `json:"cc"`
	BCC      []string `json:"bcc"`
}

// ParseSent reads the delivered messages from a send tool's input and
// answer: send_message answers {"deliveries": [{"project", "payload"}]},
// reply_message answers the payload itself. The answer may come as an
// object, a JSON string or MCP content blocks. Recipients are to, cc and
// bcc; a "Name@project" recipient keeps its name.
func ParseSent(input, response json.RawMessage) ([]Sent, error) {
	var in struct {
		ProjectKey string `json:"project_key"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &in)
	}
	var v any
	if len(response) == 0 || json.Unmarshal(response, &v) != nil {
		return nil, errors.New("wake: the tool response is not JSON")
	}
	obj, ok := findSent(v, 0)
	if !ok {
		return nil, errors.New("wake: the tool response holds no delivered message")
	}
	type delivery struct {
		Project string      `json:"project"`
		Payload sentPayload `json:"payload"`
	}
	var list []delivery
	if raw, ok := obj["deliveries"]; ok {
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("wake: deliveries: %w", err)
		}
	} else {
		data, _ := json.Marshal(obj)
		var p sentPayload
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("wake: payload: %w", err)
		}
		list = []delivery{{Payload: p}}
	}
	var out []Sent
	for _, d := range list {
		p := d.Payload
		if p.ID == 0 {
			continue
		}
		s := Sent{ProjectKey: d.Project, Message: Message{ID: p.ID, Sender: p.From}}
		if s.ProjectKey == "" {
			s.ProjectKey = in.ProjectKey
		}
		if p.ThreadID != nil {
			s.Message.ThreadID = *p.ThreadID
		}
		seen := map[string]bool{}
		for _, group := range [][]string{p.To, p.CC, p.BCC} {
			for _, r := range group {
				name, _, _ := strings.Cut(r, "@")
				if name != "" && !seen[strings.ToLower(name)] {
					seen[strings.ToLower(name)] = true
					s.Recipients = append(s.Recipients, name)
				}
			}
		}
		if s.ProjectKey == "" {
			return nil, fmt.Errorf("wake: message %d names no project", p.ID)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("wake: the tool response holds no delivered message")
	}
	return out, nil
}

// findSent finds the object that holds the deliveries or the payload.
func findSent(v any, depth int) (map[string]any, bool) {
	if depth > 6 {
		return nil, false
	}
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["deliveries"]; ok {
			return t, true
		}
		if _, ok := t["id"]; ok {
			if _, ok := t["to"]; ok {
				return t, true
			}
		}
		for _, key := range []string{"structuredContent", "structured_content", "result", "content"} {
			if inner, ok := t[key]; ok {
				if obj, ok := findSent(inner, depth+1); ok {
					return obj, true
				}
			}
		}
		if text, ok := t["text"].(string); ok {
			return findSent(text, depth+1)
		}
	case []any:
		for _, item := range t {
			if obj, ok := findSent(item, depth+1); ok {
				return obj, true
			}
		}
	case string:
		var inner any
		if json.Unmarshal([]byte(t), &inner) == nil {
			return findSent(inner, depth+1)
		}
	}
	return nil, false
}

// Report is what a path did.
type Report struct {
	// Recorded counts new pending rows.
	Recorded int `json:"recorded"`
	// Read counts rows the recipients read since the last pass.
	Read int `json:"read"`
	// Stale counts attempts settled as possibly submitted.
	Stale     int        `json:"stale"`
	Decisions []Decision `json:"decisions"`
	// Unbound names recipients without an active binding: nhm cannot
	// nudge them.
	Unbound  []string `json:"unbound"`
	Warnings []string `json:"warnings"`
}

func newReport() Report {
	return Report{Decisions: []Decision{}, Unbound: []string{}, Warnings: []string{}}
}

// MailSent records the pending ids of sent messages and nudges each
// bound recipient once. The caller holds the owner lock.
func MailSent(ctx context.Context, env Env, sent []Sent) (Report, error) {
	rep := newReport()
	var order []string
	targets := map[string]bindings.Binding{}
	for _, s := range sent {
		for _, name := range s.Recipients {
			b, err := Resolve(ctx, env.Bindings, s.ProjectKey, name)
			if err != nil {
				rep.Unbound = append(rep.Unbound, name)
				continue
			}
			n, err := Record(ctx, env, b, []Message{s.Message})
			if err != nil {
				return rep, err
			}
			rep.Recorded += n
			if _, ok := targets[b.ID]; !ok {
				targets[b.ID] = b
				order = append(order, b.ID)
			}
		}
	}
	for _, id := range order {
		d, err := Nudge(ctx, env, targets[id], TriggerMailSent)
		rep.Decisions = append(rep.Decisions, d)
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// stopHookKinds are the kinds whose Stop hook (ACFS's agent_mail_hook.sh)
// reads the mail when a turn ends; settle wake leaves them alone.
var stopHookKinds = map[string]bool{"claude": true, "codex": true}

// Settle handles a pane's move to idle or done (herdr's
// pane.agent_status_changed): for a kind without a Stop hook that reads
// mail, one non-consuming inbox read, then the same nudge. The caller
// holds the owner lock.
func Settle(ctx context.Context, env Env, paneID string, status herdr.AgentStatus) (Decision, error) {
	d := Decision{PaneID: paneID, Trigger: TriggerSettle, Pending: []string{}}
	if status != herdr.StatusIdle && status != herdr.StatusDone {
		d.skip(fmt.Sprintf("the agent is %s, not idle or done", status))
		return d, nil
	}
	b, err := env.Bindings.ActiveByPane(ctx, paneID)
	if errors.Is(err, bindings.ErrNotFound) {
		d.skip("the pane has no binding")
		return d, nil
	}
	if err != nil {
		return d, err
	}
	d.BindingID, d.MailName = b.ID, b.MailName
	if stopHookKinds[b.Kind] {
		d.skip(b.Kind + "'s Stop hook reads the mail")
		return d, nil
	}
	// An inbox that cannot be read leaves the rows that are already
	// pending: they are nudged all the same.
	var rerr error
	if env.Mail != nil {
		_, _, rerr = ReadInbox(ctx, env, b)
	}
	d, err = Nudge(ctx, env, b, TriggerSettle)
	return d, errors.Join(rerr, err)
}

// Pass is one maintenance pass over the given bindings (nil: every
// active binding): settle stale attempts, read each inbox without
// consuming it, nudge every recipient with pending mail, prune. The
// caller holds the owner lock.
func Pass(ctx context.Context, env Env, targets []bindings.Binding, trigger string) (Report, error) {
	rep := newReport()
	var err error
	if rep.Stale, err = ResolveStale(ctx, env); err != nil {
		return rep, err
	}
	if targets == nil {
		if targets, err = env.Bindings.List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}}); err != nil {
			return rep, err
		}
	}
	for _, b := range targets {
		if env.Mail != nil {
			added, read, err := ReadInbox(ctx, env, b)
			if err != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: inbox not read: %v", b.MailName, err))
			}
			rep.Recorded += added
			rep.Read += read
		}
		d, err := Nudge(ctx, env, b, trigger)
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: %v", b.MailName, err))
			continue
		}
		if d.Reason != ReasonNoPending {
			rep.Decisions = append(rep.Decisions, d)
		}
	}
	if _, err := Prune(ctx, env); err != nil {
		rep.Warnings = append(rep.Warnings, err.Error())
	}
	return rep, nil
}
