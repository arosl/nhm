package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
)

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A Claude Code transcript gives what the user typed and what the agent
// wrote, newest last (nhm-ygi): no system or tool record, no thinking, no
// meta record, no harness notice, no compaction summary.
func TestExcerptClaudeCodeTranscript(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"system","subtype":"stop_hook_summary","parentUuid":"a","content":"hook ran"}`,
		`{"type":"user","message":{"role":"user","content":"Please fix the parser in nhm-1."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private thoughts"},{"type":"text","text":"I will read the parser first."},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file body"}]}}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"Stop hook feedback: run the gates"}}`,
		`{"type":"user","message":{"role":"user","content":"<task-notification>job done</task-notification>"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation"}}`,
		`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"prompt","prompt":"Also update the docs.","origin":{"kind":"human"}}}`,
		`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"job done"}}`,
		`{"type":"user","message":{"role":"user","content":"<bash-stdout>ok</bash-stdout><bash-stderr></bash-stderr>"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The parser is fixed; tests pass."}]}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":1200}`,
	)
	got, err := excerpt(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "```\n[user] Please fix the parser in nhm-1.\n\n[assistant] I will read the parser first.\n\n" +
		"[user] Also update the docs.\n\n[assistant] The parser is fixed; tests pass.\n```"
	if got != want {
		t.Fatalf("excerpt =\n%s\nwant\n%s", got, want)
	}
}

// A Codex rollout gives the user's and the agent's messages: no developer
// instructions, no injected context, no reasoning, tool call or event.
func TestExcerptCodexTranscript(t *testing.T) {
	path := writeTranscript(t,
		`{"timestamp":"2026-10-13T00:00:00Z","type":"session_meta","payload":{"id":"s1","cwd":"/p"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are Codex."}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n<cwd>/p</cwd>\n</environment_context>"},{"type":"input_text","text":"Close nhm-2 after the tests."}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<hook_prompt name=\"stop\">check mail</hook_prompt>"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","summary":[]}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"shell","input":"go test"}}`,
		`{"type":"event_msg","payload":{"type":"token_count"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Tests pass; nhm-2 is closed."}]}}`,
	)
	got, err := excerpt(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "```\n[user] Close nhm-2 after the tests.\n\n[assistant] Tests pass; nhm-2 is closed.\n```"; got != want {
		t.Fatalf("excerpt =\n%s\nwant\n%s", got, want)
	}
}

// The newest turns that fit the budget, oldest first; a single turn longer
// than the budget keeps its end.
func TestExcerptBudget(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"turn %03d %s"}]}}`,
			i, strings.Repeat("x", 90)))
	}
	got, err := excerpt(writeTranscript(t, lines...))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, "```\n"), "\n```")
	if len(body) > excerptBytes || !strings.HasSuffix(body, "turn 099 "+strings.Repeat("x", 90)) ||
		strings.Contains(body, "turn 000") || strings.Index(body, "turn 098") > strings.Index(body, "turn 099") {
		t.Fatalf("budgeted excerpt (%d bytes):\n%s", len(body), body)
	}

	long := strings.Repeat("é", 3000) + " the end"
	got, err = excerpt(writeTranscript(t, `{"type":"assistant","message":{"role":"assistant","content":"`+long+`"}}`))
	if err != nil {
		t.Fatal(err)
	}
	body = strings.TrimSuffix(strings.TrimPrefix(got, "```\n"), "\n```")
	if len(body) > excerptBytes || !strings.HasPrefix(body, "[assistant] …é") || !strings.HasSuffix(body, " the end") {
		t.Fatalf("long turn (%d bytes): %q…%q", len(body), body[:40], body[len(body)-20:])
	}
}

// The window can start inside a record far bigger than the excerpt (a tool
// result): that partial line is dropped, and the turns after it are read.
func TestExcerptWindowStartsInsideARecord(t *testing.T) {
	big := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` +
		strings.Repeat("y", excerptWindow) + `"}]}}`
	path := writeTranscript(t, big,
		`{"type":"user","message":{"role":"user","content":"What changed?"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Only the parser."}]}}`)
	got, err := excerpt(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "```\n[user] What changed?\n\n[assistant] Only the parser.\n```"; got != want {
		t.Fatalf("excerpt = %q", got)
	}
}

// A last record bigger than the window (a tool result, inline images),
// with or without its newline (the agent may still write), gives no
// excerpt and says why; it never pastes raw JSON. An empty transcript
// gives none either.
func TestExcerptLastRecordBiggerThanTheWindow(t *testing.T) {
	small := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Reading the log."}]}}`
	huge := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` +
		strings.Repeat("z", excerptWindow+1000) + `"}]}}`
	for _, body := range []string{small + "\n" + huge + "\n", small + "\n" + huge} {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := excerpt(path); err == nil || !strings.Contains(err.Error(), "larger than") || got != "" {
			t.Fatalf("trailing newline %v: excerpt %q, err %v", strings.HasSuffix(body, "\n"), got, err)
		}
	}
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, []byte(" \n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := excerpt(empty); err == nil || got != "" {
		t.Fatalf("empty transcript: excerpt %q, err %v", got, err)
	}
}

// A window that starts exactly at a line start keeps that line's record.
func TestExcerptWindowStartsAtALineStart(t *testing.T) {
	first := `{"type":"user","message":{"role":"user","content":"FIRST"}}`
	last := `{"type":"assistant","message":{"role":"assistant","content":"LAST"}}`
	wrapper := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":""}]}}`
	pad := excerptWindow - len(first) - len(last) - len(wrapper) - 3 // three newlines
	middle := strings.Replace(wrapper, `"content":""`, `"content":"`+strings.Repeat("m", pad)+`"`, 1)
	window := first + "\n" + middle + "\n" + last + "\n"
	if len(window) != excerptWindow {
		t.Fatalf("window is %d bytes, want %d", len(window), excerptWindow)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"system","subtype":"x"}`+"\n"+window), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := excerpt(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "```\n[user] FIRST\n\n[assistant] LAST\n```"; got != want {
		t.Fatalf("excerpt = %q", got)
	}
}

// A transcript nhm cannot parse goes as it is: its last excerptBytes, from
// a line start.
func TestExcerptRawFallback(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("plain log line %03d", i))
	}
	got, err := excerpt(writeTranscript(t, lines...))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, "```\n"), "\n```")
	if len(body) > excerptBytes || !strings.HasPrefix(body, "plain log line ") || !strings.HasSuffix(body, "plain log line 199") {
		t.Fatalf("raw excerpt (%d bytes): %q…", len(body), body[:40])
	}
}

// A JSONL tail with no conversation text gives no excerpt, and the handoff
// says why in Degraded; it never pastes raw records.
func TestExcerptNoConversationText(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"system","subtype":"stop_hook_summary","content":"hook ran"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`)
	if _, err := excerpt(path); err == nil || !strings.Contains(err.Error(), "no conversation text") {
		t.Fatalf("err = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	h := DefaultHandoff(missing, missing)(context.Background(), bindings.Binding{}, nil, path)
	for _, s := range h.Sources {
		if s == "excerpt" {
			t.Fatalf("sources = %v", h.Sources)
		}
	}
	if !strings.Contains(strings.Join(h.Degraded, "\n"), "excerpt: no conversation text") || strings.Contains(h.Text, "stop_hook_summary") {
		t.Fatalf("handoff = %+v", h)
	}
}

// A turn with a code fence in it cannot close the excerpt's fence.
func TestExcerptFenceHoldsBackticks(t *testing.T) {
	path := writeTranscript(t, `{"type":"assistant","message":{"role":"assistant","content":"Run:\n`+"```"+`sh\ngo test\n`+"```"+`"}}`)
	got, err := excerpt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "````\n[assistant] Run:\n```sh") || !strings.HasSuffix(got, "```\n````") {
		t.Fatalf("excerpt = %q", got)
	}
}
