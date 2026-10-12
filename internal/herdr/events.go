package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/arosl/nhm/internal/herdr/schema"
)

// EventKind is the "event" field of an event. herdr spells most kinds
// with an underscore ("pane_agent_detected"); the three subscription-only
// kinds use a dot ("pane.agent_status_changed"). events.wait answers
// with the underscore spelling of every kind. A kind outside the pinned
// schema stays as it came, and Valid reports false.
type EventKind string

const (
	EventPaneCreated               EventKind = "pane_created"
	EventPaneClosed                EventKind = "pane_closed"
	EventPaneExited                EventKind = "pane_exited"
	EventPaneAgentDetected         EventKind = "pane_agent_detected"
	EventPaneAgentStatusChanged    EventKind = "pane_agent_status_changed"
	EventPaneAgentStatusChangedSub EventKind = "pane.agent_status_changed"
	EventPaneOutputMatchedSub      EventKind = "pane.output_matched"
)

// Subscription types: the request side, always with a dot.
const (
	subscriptionAgentDetected      = "pane.agent_detected"
	subscriptionPaneCreated        = "pane.created"
	subscriptionPaneClosed         = "pane.closed"
	subscriptionPaneExited         = "pane.exited"
	subscriptionAgentStatusChanged = "pane.agent_status_changed"
	subscriptionOutputMatched      = "pane.output_matched"
)

// Valid reports whether k is an event kind of the pinned schema.
func (k EventKind) Valid() bool { return schema.MustLoad().EventKinds[string(k)] }

// Event is one event: its kind and its data, undecoded. Use the typed
// accessors for the kinds nhm reads.
type Event struct {
	Kind EventKind       `json:"event"`
	Data json.RawMessage `json:"data"`
}

// AgentStatusChanged is the data of a pane's agent state change. Agent
// is empty when the agent left the pane.
type AgentStatusChanged struct {
	PaneID       string            `json:"pane_id"`
	WorkspaceID  string            `json:"workspace_id"`
	AgentStatus  AgentStatus       `json:"agent_status"`
	Agent        string            `json:"agent,omitempty"`
	DisplayAgent string            `json:"display_agent,omitempty"`
	Title        string            `json:"title,omitempty"`
	StateLabels  map[string]string `json:"state_labels,omitempty"`
}

// AgentDetected is the data of pane_agent_detected. herdr sends it when
// an agent appears in a pane and also when it leaves: then Released is
// true and FinalStatus is its last state.
type AgentDetected struct {
	PaneID      string      `json:"pane_id"`
	WorkspaceID string      `json:"workspace_id"`
	Agent       string      `json:"agent,omitempty"`
	Released    bool        `json:"released,omitempty"`
	FinalStatus AgentStatus `json:"final_status,omitempty"`
}

// PaneRef is the data of pane_closed and pane_exited.
type PaneRef struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
}

// AgentStatusChanged decodes the data of either spelling of the kind.
func (e Event) AgentStatusChanged() (AgentStatusChanged, error) {
	var d AgentStatusChanged
	err := e.decode(&d, EventPaneAgentStatusChanged, EventPaneAgentStatusChangedSub)
	return d, err
}

// AgentDetected decodes pane_agent_detected.
func (e Event) AgentDetected() (AgentDetected, error) {
	var d AgentDetected
	err := e.decode(&d, EventPaneAgentDetected)
	return d, err
}

// PaneRef decodes pane_closed or pane_exited.
func (e Event) PaneRef() (PaneRef, error) {
	var d PaneRef
	err := e.decode(&d, EventPaneClosed, EventPaneExited)
	return d, err
}

// PaneCreated decodes pane_created.
func (e Event) PaneCreated() (PaneInfo, error) {
	var d struct {
		Pane PaneInfo `json:"pane"`
	}
	err := e.decode(&d, EventPaneCreated)
	return d.Pane, err
}

func (e Event) decode(out any, kinds ...EventKind) error {
	for _, k := range kinds {
		if e.Kind == k {
			if err := json.Unmarshal(e.Data, out); err != nil {
				return fmt.Errorf("herdr: decode %s event: %w", e.Kind, err)
			}
			return nil
		}
	}
	return fmt.Errorf("herdr: event is %q, not %q", e.Kind, kinds[0])
}

// Subscription is one entry of events.subscribe. Use the constructors.
type Subscription struct {
	Type        string       `json:"type"`
	PaneID      string       `json:"pane_id,omitempty"`
	AgentStatus AgentStatus  `json:"agent_status,omitempty"`
	Source      ReadSource   `json:"source,omitempty"`
	Match       *OutputMatch `json:"match,omitempty"`
	Lines       *int         `json:"lines,omitempty"`
	StripANSI   *bool        `json:"strip_ansi,omitempty"`
}

// SubscribeAgentStatus subscribes to one pane's agent state changes. The
// pane must exist: one unknown pane makes the whole events.subscribe
// fail with CodePaneNotFound.
func SubscribeAgentStatus(paneID string) Subscription {
	return Subscription{Type: subscriptionAgentStatusChanged, PaneID: paneID}
}

// SubscribeAgentDetected subscribes to agents appearing in and leaving
// any pane.
func SubscribeAgentDetected() Subscription { return Subscription{Type: subscriptionAgentDetected} }

// SubscribePaneCreated subscribes to new panes in any workspace.
func SubscribePaneCreated() Subscription { return Subscription{Type: subscriptionPaneCreated} }

// SubscribePaneClosed subscribes to closed panes.
func SubscribePaneClosed() Subscription { return Subscription{Type: subscriptionPaneClosed} }

// SubscribePaneExited subscribes to panes whose process exited.
func SubscribePaneExited() Subscription { return Subscription{Type: subscriptionPaneExited} }

// SubscribeOutputMatched subscribes to output of one pane that matches m.
func SubscribeOutputMatched(paneID string, source ReadSource, m OutputMatch) Subscription {
	return Subscription{Type: subscriptionOutputMatched, PaneID: paneID, Source: source, Match: &m}
}

// Stream is an open subscription. Events are not durable: every end of
// the stream, for whatever cause, is ErrStreamLost.
type Stream struct {
	cn   *conn
	once sync.Once
	err  error
}

// Subscribe opens a stream. It returns when herdr has answered
// subscription_started; cancelling ctx later ends the stream.
func (c *Client) Subscribe(ctx context.Context, subs ...Subscription) (*Stream, error) {
	cn, err := c.send(ctx, "events.subscribe", subscribeParams{Subscriptions: subs}, 0)
	if err != nil {
		return nil, err
	}
	if _, err := cn.reply(nil); err != nil {
		cn.close()
		return nil, err
	}
	// The call's deadline bounded the start only.
	_ = cn.nc.SetDeadline(time.Time{})
	return &Stream{cn: cn}, nil
}

// Next returns the next event. Every error wraps ErrStreamLost, and the
// stream is then closed; an error reply from herdr is also an *Error in
// the chain.
func (s *Stream) Next() (Event, error) {
	if s.err != nil {
		return Event{}, s.err
	}
	line, err := s.cn.r.ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return Event{}, s.lost(err)
	}
	var msg struct {
		Event
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return Event{}, s.lost(fmt.Errorf("decode event: %w", err))
	}
	if msg.Error != nil {
		msg.Error.Method = "events.subscribe"
		return Event{}, s.lost(msg.Error)
	}
	if msg.Kind == "" {
		return Event{}, s.lost(fmt.Errorf("line without an event: %.200s", line))
	}
	return msg.Event, nil
}

func (s *Stream) lost(cause error) error {
	if ctxErr := s.cn.ctx.Err(); ctxErr != nil {
		cause = ctxErr
	}
	s.err = fmt.Errorf("%w: %w", ErrStreamLost, cause)
	s.Close()
	return s.err
}

// Close ends the stream. It is safe to call more than once.
func (s *Stream) Close() {
	s.once.Do(s.cn.close)
}
