// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/verbosity_test.go at commit 9d5ed977; see LICENSE in
// this directory and PROVENANCE.md. Local changes: this header; the calls
// pass the format to ApplyVerbosity and applyDebugProfile; the two
// encodeJSON tests, which set ntm's process globals, are left out (the
// cli tests cover the encoder); TestApplyVerbosity_DebugNamesTheFormat is
// nhm's.

package robot

import (
	"sort"
	"testing"
)

func TestParseRobotVerbosity(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    RobotVerbosity
		wantErr bool
	}{
		{name: "empty defaults", input: "", want: VerbosityDefault},
		{name: "default", input: "default", want: VerbosityDefault},
		{name: "terse", input: "terse", want: VerbosityTerse},
		{name: "debug", input: "debug", want: VerbosityDebug},
		{name: "invalid", input: "loud", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRobotVerbosity(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseRobotVerbosity(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("ParseRobotVerbosity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestApplyVerbosity_TerseShortensKeysAndDropsHints(t *testing.T) {
	payload := map[string]any{
		"success":   true,
		"timestamp": "2026-01-01T00:00:00Z",
		"_agent_hints": map[string]any{
			"summary": "ok",
		},
	}

	typed, ok := ApplyVerbosity(payload, VerbosityTerse, "auto").(map[string]any)
	if !ok {
		t.Fatalf("ApplyVerbosity() returned %T, want map[string]any", typed)
	}

	if _, exists := typed["_agent_hints"]; exists {
		t.Fatal("expected _agent_hints to be removed in terse profile")
	}
	if _, exists := typed["success"]; exists {
		t.Fatal("expected success key to be shortened in terse profile")
	}
	if _, exists := typed["ok"]; !exists {
		t.Fatal("expected ok key in terse profile output")
	}
	if _, exists := typed["ts"]; !exists {
		t.Fatal("expected ts key in terse profile output")
	}
}

func TestApplyVerbosity_TersePreservesTimestampWhenTSAlreadyExists(t *testing.T) {
	payload := map[string]any{
		"timestamp": "envelope-timestamp",
		"ts":        "snapshot-timestamp",
	}

	for range 200 {
		typed, ok := ApplyVerbosity(payload, VerbosityTerse, "auto").(map[string]any)
		if !ok {
			t.Fatalf("ApplyVerbosity() returned %T, want map[string]any", typed)
		}
		if got := typed["timestamp"]; got != "envelope-timestamp" {
			t.Fatalf("timestamp = %v, want envelope timestamp; output=%v", got, typed)
		}
		if got := typed["ts"]; got != "snapshot-timestamp" {
			t.Fatalf("ts = %v, want snapshot timestamp; output=%v", got, typed)
		}
	}
}

func TestApplyVerbosity_DebugAddsMetadata(t *testing.T) {
	payload := map[string]any{
		"success": true,
	}

	typed, ok := ApplyVerbosity(payload, VerbosityDebug, "auto").(map[string]any)
	if !ok {
		t.Fatalf("ApplyVerbosity() returned %T, want map[string]any", typed)
	}
	debug, ok := typed["_debug"].(map[string]any)
	if !ok {
		t.Fatalf("expected _debug map, got %T", typed["_debug"])
	}
	if debug["verbosity"] != "debug" {
		t.Fatalf("expected debug verbosity, got %v", debug["verbosity"])
	}
	if debug["payload_type"] == "" {
		t.Fatalf("expected payload_type to be populated")
	}
}

// _debug names the call's format, as ntm's names its global one.
func TestApplyVerbosity_DebugNamesTheFormat(t *testing.T) {
	for _, format := range []string{"auto", "json"} {
		typed := ApplyVerbosity(map[string]any{"success": true}, VerbosityDebug, format).(map[string]any)
		if debug := typed["_debug"].(map[string]any); debug["format"] != format {
			t.Fatalf("_debug.format = %v, want %s", debug["format"], format)
		}
	}
	payload := map[string]any{"success": true}
	if got := ApplyVerbosity(payload, VerbosityDefault, "auto"); len(got.(map[string]any)) != 1 {
		t.Fatalf("default changed the payload: %v", got)
	}
}

func TestApplyVerbosity_DebugWrapsSlices(t *testing.T) {
	payload := []map[string]any{
		{"success": true},
	}

	typed, ok := ApplyVerbosity(payload, VerbosityDebug, "auto").(map[string]any)
	if !ok {
		t.Fatalf("ApplyVerbosity() returned %T, want map[string]any", typed)
	}
	if _, ok := typed["_debug"].(map[string]any); !ok {
		t.Fatalf("expected _debug map for slice payload")
	}
	if _, ok := typed["items"].([]any); !ok {
		t.Fatalf("expected items array for slice payload")
	}
}

func TestApplyVerbosity_TerseNestedShortKeys(t *testing.T) {
	payload := map[string]any{
		"sessions": []any{
			map[string]any{
				"timestamp": "2026-01-01T00:00:00Z",
				"agents": []any{
					map[string]any{"success": true},
				},
			},
		},
		"_agent_hints": map[string]any{"summary": "ok"},
		"custom_field": "kept",
	}

	typed, ok := ApplyVerbosity(payload, VerbosityTerse, "auto").(map[string]any)
	if !ok {
		t.Fatalf("ApplyVerbosity() returned %T, want map[string]any", typed)
	}

	keys := make([]string, 0, len(typed))
	for k := range typed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("ROBOT_TEST: shortkeys=true fields=%v", keys)

	if _, exists := typed["_agent_hints"]; exists {
		t.Fatal("expected _agent_hints to be removed in terse profile")
	}
	if _, exists := typed["s"]; !exists {
		t.Fatal("expected sessions to be shortened to s")
	}
	if _, exists := typed["custom_field"]; !exists {
		t.Fatal("expected custom_field to remain unchanged")
	}

	sessions, ok := typed["s"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("expected s to be a single-element array, got %#v", typed["s"])
	}
	session, ok := sessions[0].(map[string]any)
	if !ok {
		t.Fatalf("expected session entry to be map, got %T", sessions[0])
	}
	if _, exists := session["ts"]; !exists {
		t.Fatal("expected timestamp to be shortened to ts in nested map")
	}
	agents, ok := session["a"].([]any)
	if !ok || len(agents) != 1 {
		t.Fatalf("expected agents to be shortened to a, got %#v", session["a"])
	}
	agent, ok := agents[0].(map[string]any)
	if !ok {
		t.Fatalf("expected agent entry to be map, got %T", agents[0])
	}
	if _, exists := agent["ok"]; !exists {
		t.Fatal("expected success to be shortened to ok in nested map")
	}
}

// ============== Edge Case Tests ==============

func TestPayloadTypeName_Nil(t *testing.T) {
	got := payloadTypeName(nil)
	if got != "nil" {
		t.Errorf("payloadTypeName(nil) = %q, want %q", got, "nil")
	}
}

func TestPayloadTypeName_VariousTypes(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{name: "string", payload: "hello", want: "string"},
		{name: "int", payload: 42, want: "int"},
		{name: "float64", payload: 3.14, want: "float64"},
		{name: "bool", payload: true, want: "bool"},
		{name: "map", payload: map[string]any{"a": 1}, want: "map[string]interface {}"},
		{name: "slice", payload: []int{1, 2, 3}, want: "[]int"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := payloadTypeName(tt.payload)
			if got != tt.want {
				t.Errorf("payloadTypeName(%v) = %q, want %q", tt.payload, got, tt.want)
			}
		})
	}
}

func TestApplyDebugProfile_PrimitiveTypes(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{name: "string", payload: "hello world"},
		{name: "int", payload: 42},
		{name: "float64", payload: 3.14},
		{name: "bool", payload: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyDebugProfile(tt.payload, "auto")
			typed, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("applyDebugProfile(%v) returned %T, want map[string]any", tt.payload, result)
			}
			if _, exists := typed["_debug"]; !exists {
				t.Errorf("expected _debug map for primitive payload")
			}
			if _, exists := typed["value"]; !exists {
				t.Errorf("expected value key for primitive payload")
			}
		})
	}
}

func TestApplyTerseProfile_UnmarshalablePayload(t *testing.T) {
	// A channel cannot be marshaled to JSON
	payload := make(chan int)
	result := applyTerseProfile(payload)
	// Should return the original payload unchanged when normalization fails
	if result != payload {
		t.Errorf("expected original payload to be returned on marshal error")
	}
}

func TestApplyDebugProfile_UnmarshalablePayload(t *testing.T) {
	// A function cannot be marshaled to JSON
	payload := func() {}
	result := applyDebugProfile(payload, "auto")
	// Should return the original payload unchanged when normalization fails
	// Note: We compare function pointers which should be equal
	if result == nil {
		t.Errorf("expected non-nil result on marshal error")
	}
}

func TestNormalizePayload_ValidData(t *testing.T) {
	payload := map[string]any{"key": "value", "num": 42}
	result, err := normalizePayload(payload)
	if err != nil {
		t.Fatalf("normalizePayload() error = %v", err)
	}
	typed, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("normalizePayload() returned %T, want map[string]any", result)
	}
	if typed["key"] != "value" {
		t.Errorf("expected key='value', got %v", typed["key"])
	}
}
