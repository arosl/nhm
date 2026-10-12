package schema

import "testing"

func TestPinnedSchemaIsProtocol22(t *testing.T) {
	s := MustLoad()
	if s.Protocol != 22 {
		t.Fatalf("protocol = %d, want 22", s.Protocol)
	}
	if s.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", s.SchemaVersion)
	}
	if n := len(s.Methods); n != 102 {
		t.Fatalf("methods = %d, want 102", n)
	}
}

// The methods and fields that plan-v8 section 2 builds on must exist in
// the pinned schema.
func TestPinnedSchemaHasWhatNhmUses(t *testing.T) {
	s := MustLoad()
	required := map[string][]string{
		"ping":                      {},
		"agent.get":                 {"target"},
		"agent.start":               {"name", "kind", "pane_id"},
		"agent.prompt":              nil,
		"agent.wait":                nil,
		"agent.list":                {},
		"pane.read":                 nil,
		"events.wait":               nil,
		"events.subscribe":          {"subscriptions"},
		"plugin.pane.open":          nil,
		"pane.process_info":         nil,
		"pane.report_agent_session": nil,
	}
	for name, keys := range required {
		m, ok := s.Methods[name]
		if !ok {
			t.Errorf("method %s missing", name)
			continue
		}
		for _, k := range keys {
			if !contains(m.Required, k) {
				t.Errorf("method %s: required %v lacks %q", name, m.Required, k)
			}
		}
	}
	for _, typ := range []string{"pong", "agent_info", "agent_list", "agent_started", "agent_prompted", "subscription_started", "wait_matched"} {
		if !s.ResultTypes[typ] {
			t.Errorf("result type %s missing", typ)
		}
	}
	for _, kind := range []string{"pane_agent_status_changed", "pane.agent_status_changed", "pane_exited"} {
		if !s.EventKinds[kind] {
			t.Errorf("event kind %s missing", kind)
		}
	}
}

// The object views that the client's schema check reads.
func TestPinnedSchemaObjects(t *testing.T) {
	s := MustLoad()
	start := s.Methods["agent.start"]
	for _, p := range []string{"name", "kind", "pane_id", "args", "timeout_ms"} {
		if !contains(start.Params, p) {
			t.Errorf("agent.start params %v lack %q", start.Params, p)
		}
	}
	if s.Methods["ping"].Params != nil {
		t.Errorf("ping params = %v, want none", s.Methods["ping"].Params)
	}
	if !s.Results["agent_info"].Has("agent") || s.Results["agent_info"].Has("agents") {
		t.Errorf("result agent_info = %+v", s.Results["agent_info"])
	}
	if !s.ResponseDefs["AgentInfo"].Has("completion_seq") || !contains(s.ResponseDefs["AgentInfo"].Required, "pane_id") {
		t.Errorf("AgentInfo = %+v", s.ResponseDefs["AgentInfo"])
	}
	if _, ok := s.ResponseDefs["AgentStatus"]; ok {
		t.Error("an enum def is listed as an object")
	}
	if !s.RequestDefs["AgentPromptWaitOptions"].Has("until") {
		t.Errorf("AgentPromptWaitOptions = %+v", s.RequestDefs["AgentPromptWaitOptions"])
	}
	if !s.EventData["pane_agent_detected"].Has("released") {
		t.Errorf("pane_agent_detected = %+v", s.EventData["pane_agent_detected"])
	}
	if !s.SubscriptionDefs["PaneAgentStatusChangedEvent"].Has("agent_status") {
		t.Errorf("PaneAgentStatusChangedEvent = %+v", s.SubscriptionDefs["PaneAgentStatusChangedEvent"])
	}
}

func TestParseRejectsBrokenDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"not json":     `{`,
		"no protocol":  `{"schemas":{"request":{},"success_response":{}}}`,
		"no method":    `{"protocol":22,"schemas":{"request":{"oneOf":[{"properties":{}}]},"success_response":{"properties":{"result":{}}}}}`,
		"dangling ref": `{"protocol":22,"schemas":{"request":{"oneOf":[{"properties":{"method":{"const":"x"},"params":{"$ref":"#/schemas/request/$defs/Nope"}}}]},"success_response":{"properties":{"result":{}}}}}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: Parse accepted %s", name, doc)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
