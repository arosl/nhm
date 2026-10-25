package robot

import (
	"strings"
	"testing"
	"time"
)

// GetDocs answers as ntm's does: the topic index, a topic's content, or
// INVALID_FLAG for an unknown topic with the docs fields kept; nhm's text
// names nhm's commands, never ntm's (nhm-2p6.2.7).
func TestGetDocs(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	index := GetDocs("", "1.2.3", nil, "auto", now)
	if !index.Success || index.Version != "1.2.3" || index.SchemaVersion != "1.0.0" || len(index.Topics) != 4 || index.Content != nil {
		t.Fatalf("index = %+v", index)
	}
	for _, topic := range []string{"quickstart", "examples", "exit-codes"} {
		out := GetDocs(topic, "1.2.3", nil, "auto", now)
		if !out.Success || out.Content == nil || out.Topic != topic || out.Topics != nil {
			t.Fatalf("%s = %+v", topic, out)
		}
		for _, e := range out.Content.Examples {
			if !strings.HasPrefix(e.Command, "nhm --robot-") {
				t.Errorf("%s example %s: %q is not an nhm robot command", topic, e.Name, e.Command)
			}
		}
		for _, s := range out.Content.Sections {
			if strings.Contains(s.Body, "ntm --") || strings.Contains(s.Body, "tmux") {
				t.Errorf("%s section %s gives an ntm command or names tmux: %q", topic, s.Heading, s.Body)
			}
		}
	}
	commands := []DocsSection{{Heading: "State Inspection", Body: "--robot-status"}}
	if out := GetDocs("commands", "1.2.3", commands, "json", now); out.Content == nil || len(out.Content.Sections) != 1 || out.OutputFormat != "json" {
		t.Fatalf("commands = %+v", out)
	}
	bad := GetDocs("nope", "1.2.3", nil, "auto", now)
	if bad.Success || bad.Error != "invalid flag" || bad.ErrorCode != ErrCodeInvalidFlag || bad.Topic != "nope" ||
		bad.Hint != "unknown topic: nope. Use --robot-docs without a topic to see available topics." || bad.Version != "1.2.3" {
		t.Fatalf("unknown topic = %+v", bad)
	}
}
