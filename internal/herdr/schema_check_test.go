package herdr

// The schema check: every method nhm calls, every parameter it sends and
// every result key it reads must exist in the pinned protocol-22 schema
// (package schema). CI runs it with the other tests. A herdr upgrade
// replaces the pinned schema; this test then names each place where the
// client no longer matches.

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/schema"
)

// paramTypes maps each method to the parameter type the client sends;
// nil means the client sends {}.
var paramTypes = map[string]any{
	"ping":                      nil,
	"session.snapshot":          nil,
	"agent.list":                nil,
	"popup.close":               nil,
	"workspace.create":          WorkspaceCreateParams{},
	"tab.create":                TabCreateParams{},
	"tab.get":                   tabParams{},
	"tab.rename":                tabRenameParams{},
	"agent.get":                 targetParams{},
	"agent.read":                AgentReadParams{},
	"agent.send_keys":           sendKeysParams{},
	"agent.rename":              agentRenameParams{},
	"agent.start":               AgentStartParams{},
	"agent.prompt":              AgentPromptParams{},
	"agent.wait":                AgentWaitParams{},
	"pane.split":                PaneSplitParams{},
	"pane.zoom":                 paneZoomParams{},
	"pane.process_info":         paneParams{},
	"pane.rename":               paneRenameParams{},
	"pane.read":                 PaneReadParams{},
	"pane.report_agent_session": ReportAgentSessionParams{},
	"pane.report_metadata":      ReportMetadataParams{},
	"pane.close":                paneParams{},
	"workspace.close":           workspaceCloseParams{},
	"workspace.focus":           workspaceParams{},
	"events.subscribe":          subscribeParams{},
	"events.wait":               eventsWaitParams{},
	"pane.wait_for_output":      WaitForOutputParams{},
	"plugin.action.invoke":      actionInvokeParams{},
	"pane.move":                 PaneMoveParams{},
	"pane.send_keys":            paneSendKeysParams{},
	"plugin.list":               pluginListParams{},
	"plugin.pane.open":          PluginPaneOpenParams{},
	"plugin.pane.focus":         paneParams{},
	"plugin.pane.close":         paneParams{},
}

// alwaysSent lists optional parameters that the client sends even when
// empty, on purpose: a null name or label clears it, and pane.process_info
// always names its pane.
var alwaysSent = map[string]bool{
	"agent.rename name":         true,
	"pane.rename label":         true,
	"pane.process_info pane_id": true,
}

// resultTypes maps each result type to the type the client decodes it
// into; nil means the client reads only "type".
var resultTypes = map[string]any{
	"pong":                  Pong{},
	"session_snapshot":      snapshotResult{},
	"workspace_created":     Created{},
	"tab_created":           Created{},
	"tab_info":              tabResult{},
	"agent_list":            agentListResult{},
	"agent_info":            agentResult{},
	"agent_started":         AgentStarted{},
	"agent_prompted":        agentResult{},
	"pane_info":             paneResult{},
	"pane_read":             readResult{},
	"pane_zoom":             zoomResult{},
	"pane_move":             paneMoveResult{},
	"plugin_list":           pluginListResult{},
	"pane_process_info":     processInfoResult{},
	"subscription_started":  nil,
	"wait_matched":          waitMatchedResult{},
	"output_matched":        OutputMatched{},
	"plugin_action_invoked": PluginActionInvoked{},
	"plugin_pane_opened":    pluginPaneResult{},
	"plugin_pane_focused":   nil,
	"plugin_pane_closed":    nil,
	"workspace_info":        nil, // workspace.focus: nhm reads nothing from it
	"ok":                    nil,
}

// resultSkips lists keys a shared Go type has that one result type lacks.
var resultSkips = map[string][]string{
	"tab_created": {"workspace"}, // Created serves workspace.create too
}

func TestMethodTableMatchesSchema(t *testing.T) {
	s := schema.MustLoad()
	if s.Protocol != Protocol {
		t.Fatalf("pinned schema is protocol %d, the client speaks %d", s.Protocol, Protocol)
	}
	for method, results := range methodResults {
		if _, ok := s.Methods[method]; !ok {
			t.Errorf("method %s is not in the pinned schema", method)
		}
		if _, ok := paramTypes[method]; !ok {
			t.Errorf("method %s has no entry in paramTypes", method)
		}
		for _, r := range results {
			if !s.ResultTypes[r] {
				t.Errorf("method %s: result type %s is not in the pinned schema", method, r)
			}
			if _, ok := resultTypes[r]; !ok {
				t.Errorf("method %s: result type %s has no entry in resultTypes", method, r)
			}
		}
	}
	for method := range paramTypes {
		if _, ok := methodResults[method]; !ok {
			t.Errorf("paramTypes has %s, methodResults does not", method)
		}
	}
}

func TestParamsMatchSchema(t *testing.T) {
	s := schema.MustLoad()
	for method, v := range paramTypes {
		m := s.Methods[method]
		if v == nil {
			if len(m.Required) > 0 {
				t.Errorf("%s: the client sends {}, the schema requires %v", method, m.Required)
			}
			continue
		}
		if m.Params == nil {
			t.Errorf("%s: the schema's params are a union; check them by hand", method)
			continue
		}
		fields := jsonFields(reflect.TypeOf(v))
		for name, omitempty := range fields {
			if !slices.Contains(m.Params, name) {
				t.Errorf("%s: parameter %q is not in the schema (%v)", method, name, m.Params)
				continue
			}
			required := slices.Contains(m.Required, name)
			switch {
			case required && omitempty:
				t.Errorf("%s: required parameter %q must not be omitempty", method, name)
			case !required && !omitempty && !alwaysSent[method+" "+name]:
				t.Errorf("%s: optional parameter %q must be omitempty, so herdr's default applies", method, name)
			}
		}
		for _, name := range m.Required {
			if _, ok := fields[name]; !ok {
				t.Errorf("%s: the schema requires %q, the client never sends it", method, name)
			}
		}
	}
}

func TestResultsMatchSchema(t *testing.T) {
	s := schema.MustLoad()
	for typ, v := range resultTypes {
		obj, ok := s.Results[typ]
		if !ok {
			t.Errorf("result type %s is not in the pinned schema", typ)
			continue
		}
		if v != nil {
			checkObject(t, "result "+typ, obj, v, resultSkips[typ]...)
		}
	}
}

func TestObjectsMatchSchema(t *testing.T) {
	s := schema.MustLoad()
	for def, v := range map[string]any{
		"AgentInfo":               AgentInfo{},
		"PaneInfo":                PaneInfo{},
		"TabInfo":                 TabInfo{},
		"WorkspaceInfo":           WorkspaceInfo{},
		"AgentSessionInfo":        AgentSession{},
		"SessionSnapshot":         SessionSnapshot{},
		"PaneReadResult":          PaneRead{},
		"PaneProcessInfo":         ProcessInfo{},
		"PaneProcessInfoProcess":  Process{},
		"PaneZoomResult":          ZoomResult{},
		"PaneMoveResult":          PaneMoveResult{},
		"InstalledPluginInfo":     PluginInfo{},
		"PluginManifestPane":      PluginSpec{},
		"PluginActionInfo":        PluginAction{},
		"PluginInvocationContext": InvocationContext{},
		"PluginCommandLogInfo":    PluginCommandLog{},
		"PluginPaneInfo":          PluginPane{},
		"EventEnvelope":           Event{},
	} {
		checkDef(t, "response", s.ResponseDefs, def, v)
	}
	for def, v := range map[string]any{
		"AgentPromptWaitOptions":  PromptWait{},
		"PluginInvocationContext": InvocationContext{},
		"EventsSubscribeParams":   subscribeParams{},
	} {
		checkDef(t, "request", s.RequestDefs, def, v)
	}
	for typ, v := range map[string]any{
		"pane_agent_detected":       AgentDetected{},
		"pane_agent_status_changed": AgentStatusChanged{},
		"pane_closed":               PaneRef{},
		"pane_exited":               PaneRef{},
	} {
		obj, ok := s.EventData[typ]
		if !ok {
			t.Errorf("event data %s is not in the pinned schema", typ)
			continue
		}
		checkObject(t, "event "+typ, obj, v)
	}
	checkDef(t, "subscription", s.SubscriptionDefs, "PaneAgentStatusChangedEvent", AgentStatusChanged{})
}

// The unions: each value the client builds must fit the variant that its
// discriminator selects.
func TestUnionsMatchSchema(t *testing.T) {
	subs := variants(t, "request", "Subscription", "type")
	for _, sub := range []Subscription{
		SubscribeAgentStatus("w1:p1"),
		SubscribeAgentDetected(),
		SubscribePaneCreated(),
		SubscribePaneClosed(),
		SubscribePaneExited(),
		SubscribeOutputMatched("w1:p1", ReadRecent, OutputMatch{Type: "substring", Value: "x"}),
	} {
		checkVariant(t, "subscription", subs, sub.Type, sub)
	}
	matches := variants(t, "request", "EventMatch", "event")
	for _, m := range []EventMatch{
		{Event: EventPaneAgentStatusChanged, PaneID: "w1:p1", AgentStatus: StatusIdle},
		{Event: EventPaneAgentDetected, PaneID: "w1:p1", Agent: "claude"},
	} {
		checkVariant(t, "events.wait match", matches, string(m.Event), m)
	}
	dests := variants(t, "request", "PaneMoveDestination", "type")
	checkVariant(t, "pane.move destination", dests, "new_tab", NewTabIn("w2"))
	outputs := variants(t, "request", "OutputMatch", "type")
	for _, m := range []OutputMatch{{Type: "substring", Value: "x"}, {Type: "regex", Value: "x"}} {
		checkVariant(t, "output match", outputs, m.Type, m)
	}
}

func TestEnumsMatchSchema(t *testing.T) {
	states := enum(t, "success_response", "AgentStatus")
	for _, st := range []AgentStatus{StatusIdle, StatusWorking, StatusBlocked, StatusDone, StatusUnknown} {
		if !slices.Contains(states, string(st)) || !st.Valid() {
			t.Errorf("agent status %q: schema %v, Valid %v", st, states, st.Valid())
		}
	}
	if len(states) != 5 {
		t.Errorf("schema has %d agent states %v; AgentStatus.Valid knows 5", len(states), states)
	}
	if AgentStatus("thinking").Valid() {
		t.Error(`AgentStatus("thinking").Valid() = true`)
	}
	sources := enum(t, "request", "ReadSource")
	for _, src := range []ReadSource{ReadVisible, ReadRecent, ReadRecentUnwrapped, ReadDetection} {
		if !slices.Contains(sources, string(src)) {
			t.Errorf("read source %q is not in the schema %v", src, sources)
		}
	}
	formats := enum(t, "request", "ReadFormat")
	for _, f := range []ReadFormat{FormatText, FormatANSI} {
		if !slices.Contains(formats, string(f)) {
			t.Errorf("read format %q is not in the schema %v", f, formats)
		}
	}
	for _, k := range []EventKind{
		EventPaneCreated, EventPaneClosed, EventPaneExited, EventPaneAgentDetected,
		EventPaneAgentStatusChanged, EventPaneAgentStatusChangedSub, EventPaneOutputMatchedSub,
	} {
		if !k.Valid() {
			t.Errorf("event kind %q is not in the pinned schema", k)
		}
	}
	if EventKind("pane_teleported").Valid() {
		t.Error(`EventKind("pane_teleported").Valid() = true`)
	}
}

// jsonFields maps each JSON key of a struct type to whether it is
// omitempty. Fields without a json tag use their Go name, as
// encoding/json does, so a forgotten tag shows up as a wrong key.
func jsonFields(rt reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		out[name] = strings.Contains(","+opts+",", ",omitempty,")
	}
	return out
}

func checkObject(t *testing.T, label string, obj schema.Object, v any, skip ...string) {
	t.Helper()
	for name := range jsonFields(reflect.TypeOf(v)) {
		if slices.Contains(skip, name) {
			continue
		}
		if !obj.Has(name) {
			t.Errorf("%s: key %q is not in the schema (%v)", label, name, obj.Properties)
		}
	}
}

func checkDef(t *testing.T, part string, defs map[string]schema.Object, def string, v any) {
	t.Helper()
	obj, ok := defs[def]
	if !ok {
		t.Errorf("%s def %s is not in the pinned schema", part, def)
		return
	}
	checkObject(t, part+" "+def, obj, v)
}

// rawPart returns the $defs of one part of the pinned schema as JSON.
func rawPart(t *testing.T, part string) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		Schemas map[string]struct {
			Defs map[string]json.RawMessage `json:"$defs"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(schema.Raw(), &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return doc.Schemas[part].Defs
}

type variant struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

// variants returns the oneOf variants of a $defs union, by the const of
// their discriminator key.
func variants(t *testing.T, part, def, key string) map[string]variant {
	t.Helper()
	var union struct {
		OneOf []variant `json:"oneOf"`
	}
	if err := json.Unmarshal(rawPart(t, part)[def], &union); err != nil || len(union.OneOf) == 0 {
		t.Fatalf("%s def %s is not a oneOf union: %v", part, def, err)
	}
	out := map[string]variant{}
	for _, v := range union.OneOf {
		var c struct {
			Const string `json:"const"`
		}
		_ = json.Unmarshal(v.Properties[key], &c)
		out[c.Const] = v
	}
	return out
}

// checkVariant checks that the keys v sends fit the variant named by
// disc, and that v sends every key the variant requires.
func checkVariant(t *testing.T, label string, vs map[string]variant, disc string, v any) {
	t.Helper()
	want, ok := vs[disc]
	if !ok {
		t.Errorf("%s: no schema variant %q", label, disc)
		return
	}
	data, _ := json.Marshal(v)
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(data, &sent)
	for name := range sent {
		if _, ok := want.Properties[name]; !ok {
			t.Errorf("%s %s: key %q is not in the variant", label, disc, name)
		}
	}
	for _, name := range want.Required {
		if _, ok := sent[name]; !ok {
			t.Errorf("%s %s: the variant requires %q, the client does not send it", label, disc, name)
		}
	}
}

func enum(t *testing.T, part, def string) []string {
	t.Helper()
	var e struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(rawPart(t, part)[def], &e); err != nil || len(e.Enum) == 0 {
		t.Fatalf("%s def %s is not an enum: %v", part, def, err)
	}
	sort.Strings(e.Enum)
	return e.Enum
}
