// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/verbosity.go, with TerseKeyMap and TerseKeyFor from
// internal/robot/types.go, at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; ApplyVerbosity
// (ntm's applyVerbosity) is exported and takes the call's format, which
// ntm reads from its process global; ntm's Get/SetOutputVerbosity globals
// are left out, the cli holds the call's verbosity (nhm-2p6.2.3).

package robot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
)

// RobotVerbosity controls how much detail is included in robot outputs.
// This only applies to JSON/TOON rendering, not --robot-terse.
type RobotVerbosity string

const (
	VerbosityDefault RobotVerbosity = "default"
	VerbosityTerse   RobotVerbosity = "terse"
	VerbosityDebug   RobotVerbosity = "debug"
)

// ParseRobotVerbosity converts a string to RobotVerbosity.
// Returns VerbosityDefault for empty string and error for invalid values.
func ParseRobotVerbosity(s string) (RobotVerbosity, error) {
	if s == "" {
		return VerbosityDefault, nil
	}
	v := RobotVerbosity(s)
	switch v {
	case VerbosityDefault, VerbosityTerse, VerbosityDebug:
		return v, nil
	default:
		return "", fmt.Errorf("invalid robot verbosity %q: must be terse, default, or debug", s)
	}
}

// ApplyVerbosity shapes a payload for the verbosity: terse shortens keys
// and drops _agent_hints, debug adds _debug, default leaves it as it is.
// format is the call's output format, which _debug names.
func ApplyVerbosity(payload any, verbosity RobotVerbosity, format string) any {
	switch verbosity {
	case VerbosityTerse:
		return applyTerseProfile(payload)
	case VerbosityDebug:
		return applyDebugProfile(payload, format)
	default:
		return payload
	}
}

func applyTerseProfile(payload any) any {
	normalized, err := normalizePayload(payload)
	if err != nil {
		return payload
	}
	return shortenKeys(normalized)
}

func applyDebugProfile(payload any, format string) any {
	normalized, err := normalizePayload(payload)
	if err != nil {
		return payload
	}

	debugInfo := map[string]any{
		"format":       format,
		"verbosity":    string(VerbosityDebug),
		"go_version":   runtime.Version(),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"payload_type": payloadTypeName(payload),
	}

	switch typed := normalized.(type) {
	case map[string]any:
		if _, exists := typed["_debug"]; !exists {
			typed["_debug"] = debugInfo
		}
		return typed
	case []any:
		return map[string]any{
			"_debug": debugInfo,
			"items":  typed,
		}
	default:
		return map[string]any{
			"_debug": debugInfo,
			"value":  typed,
		}
	}
}

func payloadTypeName(payload any) string {
	if payload == nil {
		return "nil"
	}
	return reflect.TypeOf(payload).String()
}

func normalizePayload(payload any) (any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func shortenKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, val := range typed {
			if key == "_agent_hints" {
				continue
			}
			shortKey := key
			if mapped, ok := TerseKeyFor(key); ok {
				if _, occupied := typed[mapped]; occupied && mapped != key {
					out[key] = shortenKeys(val)
					continue
				}
				shortKey = mapped
			}
			out[shortKey] = shortenKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = shortenKeys(val)
		}
		return out
	default:
		return value
	}
}

// TerseKeyMap defines the short-key mapping for --robot-verbosity=terse JSON/TOON output.
// Mapping rules:
// - Only keys listed here are shortened.
// - Values must be unique to keep the mapping reversible.
// - Keep the mapping stable; add new keys only when needed by terse profile outputs.
var TerseKeyMap = map[string]string{
	// Envelope fields (v1.0.0)
	"success":       "ok",
	"timestamp":     "ts",
	"version":       "v",
	"output_format": "of",
	"_meta":         "mt",
	"error":         "err",
	"error_code":    "ec",
	"hint":          "h",

	// ResponseMeta fields
	"duration_ms": "dm",
	"exit_code":   "ex",
	"command":     "cmd",

	// Optional agent guidance (only if included by profile)
	"_agent_hints": "ah",

	// Critical top-level arrays (always present per envelope spec)
	"sessions": "s",
	"panes":    "p",
	"targets":  "t",
	"agents":   "a",

	// Common top-level collections
	"alerts":   "al",
	"beads":    "b",
	"messages": "m",

	// Common meta fields
	"count":        "n",
	"generated_at": "ga",
	"summary":      "sum",

	// Structured error fields (bd-3vc3s)
	"structured_error": "se",
	"phase":            "ph",
	"details":          "d",
	"recovery_hint":    "rh",
}

// TerseKeyFor returns the short key for a long field name.
// If no mapping exists, ok is false and key is empty.
func TerseKeyFor(field string) (key string, ok bool) {
	key, ok = TerseKeyMap[field]
	return key, ok
}
