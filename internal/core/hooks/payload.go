package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
)

// registrationTools are the Agent Mail tools whose PostToolUse hook names
// the pane (plan-v8 2.7 step 1). Claude Code and Codex call an MCP tool
// "mcp__<server>__<tool>"; the server name is the user's choice.
var registrationTools = map[string]bool{
	"register_agent":        true,
	"create_agent_identity": true,
	"macro_start_session":   true,
}

// AgentPayload is the part of a Claude Code or Codex hook payload that
// nhm reads. Both CLIs send the same field names.
type AgentPayload struct {
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	Source        string          `json:"source,omitempty"`
	Model         string          `json:"model,omitempty"`
	Cwd           string          `json:"cwd,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
}

// ParseAgentPayload decodes a hook's stdin.
func ParseAgentPayload(data []byte) (AgentPayload, error) {
	var p AgentPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return AgentPayload{}, fmt.Errorf("hooks: payload: %w", err)
	}
	if p.HookEventName == "" {
		return AgentPayload{}, errors.New("hooks: payload has no hook_event_name")
	}
	return p, nil
}

// RegistrationTool returns the Agent Mail tool a tool name calls, or "".
func RegistrationTool(toolName string) string {
	name := toolName
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	if registrationTools[name] {
		return name
	}
	return ""
}

// Registration is what an Agent Mail registration call returned.
type Registration struct {
	Name       string
	ProjectKey string
	// Token is the registration token. It is never stored in a
	// candidate row or a log.
	Token string
}

// ParseRegistration reads the identity from a registration tool call:
// the project from the tool input, the name and the token from the
// response. The response is an object, a JSON string, or a list of MCP
// content blocks whose text is JSON. macro_start_session answers with the
// identity under "agent".
func ParseRegistration(p AgentPayload) (Registration, error) {
	var in struct {
		ProjectKey string `json:"project_key"`
		HumanKey   string `json:"human_key"`
	}
	if len(p.ToolInput) > 0 {
		_ = json.Unmarshal(p.ToolInput, &in)
	}
	r := Registration{ProjectKey: in.ProjectKey}
	if r.ProjectKey == "" {
		r.ProjectKey = in.HumanKey
	}
	obj, ok := responseObject(p.ToolResponse)
	if !ok {
		return r, errors.New("hooks: the tool response holds no JSON object")
	}
	if agent, ok := obj["agent"].(map[string]any); ok {
		obj = agent
	}
	r.Name, _ = obj["name"].(string)
	r.Token, _ = obj["registration_token"].(string)
	if r.Name == "" {
		return r, errors.New("hooks: the tool response names no agent")
	}
	if r.ProjectKey == "" {
		return r, errors.New("hooks: the tool input names no project")
	}
	return r, nil
}

// responseObject finds the JSON object in a tool response.
func responseObject(raw json.RawMessage) (map[string]any, bool) {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	return objectIn(v, 0)
}

func objectIn(v any, depth int) (map[string]any, bool) {
	if depth > 6 {
		return nil, false
	}
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["name"]; ok {
			return t, true
		}
		if _, ok := t["agent"]; ok {
			return t, true
		}
		// MCP's structured result, or a wrapper of the content list.
		for _, key := range []string{"structuredContent", "structured_content", "result", "content"} {
			if inner, ok := t[key]; ok {
				if obj, ok := objectIn(inner, depth+1); ok {
					return obj, true
				}
			}
		}
		if text, ok := t["text"].(string); ok {
			return objectIn(text, depth+1)
		}
	case []any:
		for _, item := range t {
			if obj, ok := objectIn(item, depth+1); ok {
				return obj, true
			}
		}
	case string:
		var inner any
		if json.Unmarshal([]byte(t), &inner) == nil {
			return objectIn(inner, depth+1)
		}
	}
	return nil, false
}

// PluginEvent is a herdr plugin event hook's HERDR_PLUGIN_EVENT_JSON. The
// "event" name comes with an underscore there ("pane_agent_detected")
// and with a dot in HERDR_PLUGIN_EVENT (ACFS exp.1); nhm accepts both.
type PluginEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// PaneEvent is the data nhm reads from pane.agent_detected and
// pane.agent_status_changed.
type PaneEvent struct {
	PaneID      string            `json:"pane_id"`
	Agent       string            `json:"agent,omitempty"`
	AgentStatus herdr.AgentStatus `json:"agent_status,omitempty"`
	// Released: pane.agent_detected also fires when the agent leaves.
	Released    bool              `json:"released,omitempty"`
	FinalStatus herdr.AgentStatus `json:"final_status,omitempty"`
}

// ParsePluginEvent decodes HERDR_PLUGIN_EVENT_JSON. An empty value gives
// an empty event; the caller then takes the pane from HERDR_PANE_ID.
func ParsePluginEvent(raw string) (PaneEvent, error) {
	if strings.TrimSpace(raw) == "" {
		return PaneEvent{}, nil
	}
	var ev PluginEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return PaneEvent{}, fmt.Errorf("hooks: plugin event: %w", err)
	}
	var d PaneEvent
	if len(ev.Data) > 0 {
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			return PaneEvent{}, fmt.Errorf("hooks: plugin event %s: %w", ev.Event, err)
		}
	}
	return d, nil
}
