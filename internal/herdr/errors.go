package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
)

// Error codes that herdr 0.9.3 returns and nhm acts on. herdr's
// error.code is a free string: a code that is not listed here still
// reaches the caller unchanged in Error.Code.
const (
	CodeInvalidRequest            = "invalid_request"
	CodeInvalidParams             = "invalid_params"
	CodeTimeout                   = "timeout"
	CodeAgentNotFound             = "agent_not_found"
	CodeAgentNotReady             = "agent_not_ready"
	CodeAgentNotRunning           = "agent_not_running"
	CodeAgentLaunchPending        = "agent_launch_pending"
	CodeAgentNameTaken            = "agent_name_taken"
	CodeInvalidAgentName          = "invalid_agent_name"
	CodeUnsupportedAgentKind      = "unsupported_agent_kind"
	CodeAgentPromptStalled        = "agent_prompt_stalled"
	CodePaneNotFound              = "pane_not_found"
	CodePopupNotOpen              = "popup_not_open"
	CodeUIBusy                    = "ui_busy"
	CodeUnsupportedEventWaitMatch = "unsupported_event_wait_match"
)

// Error is an error reply from herdr: {"id", "error": {"code", "message"}}.
type Error struct {
	// Method is the request method; herdr does not send it.
	Method  string `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("herdr %s: %s: %s", e.Method, e.Code, e.Message)
}

// ErrorCode returns the herdr error code in err's chain, or "" when err
// is not a herdr error reply.
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// UnexpectedResultError is a success reply whose result type is not one
// that nhm expects for the method. A newer herdr can answer with a
// variant nhm does not know; nhm stops there instead of guessing.
type UnexpectedResultError struct {
	Method string
	Type   string
	Raw    json.RawMessage
}

func (e *UnexpectedResultError) Error() string {
	return fmt.Sprintf("herdr %s: unexpected result type %q", e.Method, e.Type)
}

// TransportError is a failure to reach herdr or to read its reply. A
// request may have reached herdr and been applied before the failure:
// read herdr's state before you repeat a mutation.
type TransportError struct {
	Method string
	Err    error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("herdr %s: %v", e.Method, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// ServerDown reports a call that found no herdr server: its socket does
// not exist, or nothing accepts on it. A server that answers badly is not
// down.
func ServerDown(err error) bool {
	var te *TransportError
	return errors.As(err, &te) && (errors.Is(te.Err, syscall.ENOENT) || errors.Is(te.Err, syscall.ECONNREFUSED))
}

// ErrStreamLost ends every event stream. herdr 0.9.3 has no events_lost
// error: a subscriber that falls behind gets a plain EOF (ACFS evidence
// exp1). So every end of a stream means that events may be lost; the
// caller subscribes again and reads a fresh snapshot.
var ErrStreamLost = errors.New("herdr: event stream lost")
