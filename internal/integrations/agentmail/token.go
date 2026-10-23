package agentmail

// Registration tokens. Agent Mail keeps one token per identity and issues
// a new one at every register_agent or macro_start_session for that name;
// it then refuses the one before with reason token_mismatch (checked
// 2026-10-12 with am 0.3.38, acfs-9hkh.92). So a token kept at spawn goes
// stale as soon as the agent registers itself again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrIdentityRetired means Agent Mail has the identity retired already.
var ErrIdentityRetired = errors.New("agentmail: the identity is retired")

// Token states, as CheckToken reports them.
const (
	// TokenCurrent: Agent Mail accepts the token.
	TokenCurrent = "current"
	// TokenStale: Agent Mail refuses the token (token_mismatch): the
	// agent registered again after the token was kept.
	TokenStale = "stale"
	// TokenRetired: the identity is retired, and the token is not
	// checked.
	TokenRetired = "retired"
	// TokenNoAgent: Agent Mail has no such identity in the project.
	TokenNoAgent = "no_agent"
)

// Caller calls one Agent Mail tool; *Client has it.
type Caller interface {
	Call(ctx context.Context, tool string, args, out any) error
}

// CheckToken tells whether token is the current registration token of
// identity name in project, and changes nothing: unretire_agent on an
// active identity checks the token and leaves the identity active (Agent
// Mail writes its unchanged profile to the archive again). A retired
// identity is not checked, because unretire_agent would bring it back.
// Any failure other than a refusal of the token is an error.
//
// If the identity is retired between the whois and the unretire_agent
// call, a current token brings it back. Only the holder of the current
// token, or `am agents reap`, can retire it in that window.
func CheckToken(ctx context.Context, mail Caller, project, name, token string) (string, error) {
	var who struct {
		RetiredAt json.RawMessage `json:"retired_at"`
	}
	if err := mail.Call(ctx, "whois", map[string]any{"project_key": project, "agent_name": name}, &who); err != nil {
		if notFound(err) {
			return TokenNoAgent, nil
		}
		return "", err
	}
	if retired(who.RetiredAt) {
		return TokenRetired, nil
	}
	err := mail.Call(ctx, "unretire_agent", map[string]any{"project_key": project, "agent_name": name,
		"registration_token": token}, nil)
	switch {
	case err == nil:
		return TokenCurrent, nil
	case IsTokenMismatch(err):
		return TokenStale, nil
	case notFound(err):
		return TokenNoAgent, nil
	}
	return "", err
}

// NewToken registers identity name in project again and returns the new
// registration token, which makes every token before it stale (nhm-p49).
// Agent Mail takes register_agent with a name it has as an update of that
// identity, with no proof unless its registration proof gate is on
// (checked in am 0.3.38's source). The update overwrites the program, the
// model, the task (an omitted one becomes "") and the attachments policy
// (an omitted one becomes "auto"), so the call passes back the ones whois
// reports; only the last activity changes. The caller keeps the token in
// memory: it is never stored, logged or printed.
//
// A retired identity is not registered again (ErrIdentityRetired):
// register_agent would only change its token, and leave it retired.
func NewToken(ctx context.Context, mail Caller, project, name string) (string, error) {
	var who struct {
		Program           string          `json:"program"`
		Model             string          `json:"model"`
		TaskDescription   string          `json:"task_description"`
		AttachmentsPolicy string          `json:"attachments_policy"`
		RetiredAt         json.RawMessage `json:"retired_at"`
	}
	if err := mail.Call(ctx, "whois", map[string]any{"project_key": project, "agent_name": name}, &who); err != nil {
		return "", err
	}
	if retired(who.RetiredAt) {
		return "", ErrIdentityRetired
	}
	if who.Program == "" || who.Model == "" {
		return "", fmt.Errorf("agentmail: whois gives %s no program or model to register it with", name)
	}
	args := map[string]any{"project_key": project, "name": name, "program": who.Program, "model": who.Model,
		"task_description": who.TaskDescription}
	if who.AttachmentsPolicy != "" {
		args["attachments_policy"] = who.AttachmentsPolicy
	}
	var out struct {
		Name  string `json:"name"`
		Token string `json:"registration_token"`
	}
	if err := mail.Call(ctx, "register_agent", args, &out); err != nil {
		return "", err
	}
	switch {
	case !strings.EqualFold(out.Name, name):
		return "", fmt.Errorf("agentmail: register_agent for %s answered for %q", name, out.Name)
	case out.Token == "":
		return "", fmt.Errorf("agentmail: register_agent for %s returned no registration token", name)
	}
	return out.Token, nil
}

// retired reports a whois retired_at that is set.
func retired(raw json.RawMessage) bool {
	r := strings.TrimSpace(string(raw))
	return r != "" && r != "null" && r != `""`
}

// refusal is the type and reason of an Agent Mail refusal: a ToolError
// whose text is {"error": {"type": ..., "data": {"reason": ...}}}.
func refusal(err error) (typ, reason, message string, ok bool) {
	var te *ToolError
	if !errors.As(err, &te) {
		return "", "", "", false
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Data struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(te.Message), &body) == nil {
		return body.Error.Type, body.Error.Data.Reason, te.Message, true
	}
	return "", "", te.Message, true
}

// IsTokenMismatch reports Agent Mail's refusal of a registration token
// that is not the identity's current one.
func IsTokenMismatch(err error) bool {
	typ, reason, msg, ok := refusal(err)
	if !ok {
		return false
	}
	if typ != "" {
		return reason == "token_mismatch"
	}
	// A JSON-RPC error carries only the message.
	return strings.Contains(msg, "token_mismatch") || strings.Contains(msg, "registration_token does not match")
}

func notFound(err error) bool {
	typ, _, msg, ok := refusal(err)
	if !ok {
		return false
	}
	if typ != "" {
		return strings.Contains(typ, "NOT_FOUND")
	}
	return strings.Contains(strings.ToLower(msg), "not found")
}
