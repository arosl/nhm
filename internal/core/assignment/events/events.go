// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/events/webhook_event.go and internal/events/bus.go (BusEvent,
// BaseEvent) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: this header; only what the assignment
// package calls is kept: the webhook event types, WebhookEvent,
// NewWebhookEvent, BusEvent and BaseEvent. EventEmitter and
// DefaultEmitter are nhm's stand-in: nhm has no webhook or event-stream
// sink, so Emit drops the notice. The assignment ledger's own completion
// events and their leases (store.go) do not go through here.

// Package events is the stand-in for ntm's event bus that nhm's port of
// ntm's assignment package calls.
package events

import "time"

// Webhook event types (shared with .ntm.yaml webhooks config).
const (
	WebhookSessionCreated = "session.created"
	WebhookSessionKilled  = "session.killed"
	WebhookSessionEnded   = "session.ended" // Alias for session.killed (legacy/alternate naming)
	WebhookAgentStarted   = "agent.started"
	WebhookAgentStopped   = "agent.stopped"
	WebhookAgentError     = "agent.error"
	WebhookAgentCrashed   = "agent.crashed"
	WebhookAgentRestarted = "agent.restarted"
	WebhookAgentIdle      = "agent.idle"
	WebhookAgentBusy      = "agent.busy"
	WebhookAgentRateLimit = "agent.rate_limit"
	WebhookAgentCompleted = "agent.completed"
	WebhookRotationNeeded = "rotation.needed"
	WebhookHealthDegraded = "health.degraded"
	WebhookBeadAssigned   = "bead.assigned"
	WebhookBeadCompleted  = "bead.completed"
	WebhookBeadFailed     = "bead.failed"
)

// BusEvent is the interface that all bus events must implement
type BusEvent interface {
	EventType() string
	EventTimestamp() time.Time
	EventSession() string
}

// BaseEvent provides common fields for all events
type BaseEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session,omitempty"`
}

// EventType returns the event type
func (e BaseEvent) EventType() string { return e.Type }

// EventTimestamp returns the event timestamp
func (e BaseEvent) EventTimestamp() time.Time { return e.Timestamp }

// EventSession returns the session name
func (e BaseEvent) EventSession() string { return e.Session }

// WebhookEvent is a BusEvent intended for downstream dispatch to webhooks and
// for robot/server event streaming (SSE/WebSocket). It mirrors the core webhook
// payload shape while remaining lightweight and easy to emit throughout NTM.
type WebhookEvent struct {
	BaseEvent

	Pane    string            `json:"pane,omitempty"`
	Agent   string            `json:"agent,omitempty"`
	Message string            `json:"message,omitempty"`
	Details map[string]string `json:"details,omitempty"`
}

// NewWebhookEvent constructs a webhook event with UTC timestamp.
func NewWebhookEvent(eventType, session, pane, agent, message string, details map[string]string) WebhookEvent {
	return WebhookEvent{
		BaseEvent: BaseEvent{
			Type:      eventType,
			Timestamp: time.Now().UTC(),
			Session:   session,
		},
		Pane:    pane,
		Agent:   agent,
		Message: message,
		Details: cloneStringMap(details),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// EventEmitter is nhm's stand-in for ntm's asynchronous emitter. nhm has no
// webhook or event-stream sink, so it drops every event.
type EventEmitter struct{}

var defaultEmitter EventEmitter

// DefaultEmitter returns the process-wide emitter.
func DefaultEmitter() *EventEmitter { return &defaultEmitter }

// Emit drops ev: nhm has nowhere to send a webhook notice.
func (e *EventEmitter) Emit(ev BusEvent) {}
