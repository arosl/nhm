package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/cm"
)

// handoffBudget bounds each handoff source.
const handoffBudget = 5 * time.Second

// excerptBytes is the size of a direct transcript excerpt.
const excerptBytes = 4000

// Handoff is what recycle hands the successor (plan-v8 2.10 step 3).
type Handoff struct {
	Text string `json:"-"`
	// Sources are the parts it holds; Degraded the ones that failed.
	Sources  []string `json:"sources"`
	Degraded []string `json:"degraded"`
}

// HandoffFunc composes a handoff from the agent's bead, its transcript
// and the project's memory.
type HandoffFunc func(ctx context.Context, b bindings.Binding, bead *Bead, transcript string) Handoff

// DefaultHandoff composes the handoff from the bead's latest handoff
// comment, `cass pack "<bead id> <title>" --sessions-from -
// --max-tokens 2000` when CASS has indexed the session (otherwise a
// direct transcript excerpt, marked as such), and `cm context "<title>"`.
func DefaultHandoff(cassBin, cmBin string) HandoffFunc {
	return func(ctx context.Context, b bindings.Binding, bead *Bead, transcript string) Handoff {
		var h Handoff
		var parts []string
		query := ""
		if bead != nil {
			query = strings.TrimSpace(bead.ID + " " + bead.Title)
			if bead.Handoff != "" {
				parts = append(parts, "## The last handoff on "+bead.ID+"\n\n"+bead.Handoff)
				h.Sources = append(h.Sources, "bead")
			} else {
				h.Degraded = append(h.Degraded, "bead: no handoff comment on "+bead.ID)
			}
		}
		if transcript != "" {
			if text, err := packSession(ctx, cassBin, b.MailProjectKey, query, transcript); err == nil && text != "" {
				parts = append(parts, "## What the last session did (cass pack)\n\n"+text)
				h.Sources = append(h.Sources, "cass")
			} else {
				if err != nil {
					h.Degraded = append(h.Degraded, "cass: "+err.Error())
				}
				if ex, err := excerpt(transcript); err == nil && ex != "" {
					parts = append(parts, "## The end of the last session's transcript (a direct excerpt; CASS has not indexed it)\n\n"+ex)
					h.Sources = append(h.Sources, "excerpt")
				} else if err != nil {
					h.Degraded = append(h.Degraded, "excerpt: "+err.Error())
				}
			}
		}
		if bead != nil && bead.Title != "" {
			cctx, cancel := context.WithTimeout(ctx, handoffBudget)
			c, err := cm.Client{Run: cm.Exec(cmBin)}.Context(cctx, bead.Title, b.MailProjectKey)
			cancel()
			switch {
			case err != nil:
				h.Degraded = append(h.Degraded, "cm: "+err.Error())
			case len(c.Rules) > 0:
				var lines []string
				for _, r := range c.Rules {
					lines = append(lines, "- "+r.Content)
				}
				parts = append(parts, "## Rules from memory (cm context)\n\n"+strings.Join(lines, "\n"))
				h.Sources = append(h.Sources, "cm")
			}
		}
		if len(parts) > 0 {
			h.Text = "# Handoff from your predecessor in this pane\n\n" + strings.Join(parts, "\n\n")
		}
		if h.Sources == nil {
			h.Sources = []string{}
		}
		if h.Degraded == nil {
			h.Degraded = []string{}
		}
		return h
	}
}

// packSession runs cass pack over the session, only after cass lists it.
func packSession(ctx context.Context, cassBin, project, query, transcript string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, handoffBudget)
	defer cancel()
	sessions, err := cass.Client{Run: cass.Exec(cassBin)}.Sessions(cctx, project, 200)
	if err != nil {
		return "", err
	}
	indexed := false
	for _, s := range sessions {
		indexed = indexed || s.Path == transcript
	}
	if !indexed {
		return "", fmt.Errorf("the session is not indexed yet")
	}
	if query == "" {
		query = "handoff"
	}
	if cassBin == "" {
		cassBin = "cass"
	}
	cmd := exec.CommandContext(cctx, cassBin, "pack", "--sessions-from=-", "--max-tokens=2000", "--", query)
	// As cass.Exec runs cass: no prompts, no refresh, no progress events,
	// and a child that holds stdout open cannot keep the call past the
	// budget and WaitDelay (nhm-gne).
	cmd.WaitDelay = cass.WaitDelay
	cmd.Env = append(os.Environ(), "CODING_AGENT_SEARCH_NO_UPDATE_PROMPT=1", "CASS_AUTO_REFRESH=0",
		"CASS_INDEX_NO_PROGRESS_EVENTS=1", "NO_COLOR=1")
	cmd.Stdin = strings.NewReader(transcript + "\n")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("cass pack: %v: %s", err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// excerpt is the end of the last session's conversation, from its
// transcript (nhm-ygi). A Claude Code or Codex transcript is JSONL, where
// the last turns sit among system and tool records far bigger than the
// excerpt: from the transcript's last excerptWindow, it keeps what the user
// typed and the agent wrote, newest last, within excerptBytes. A tail that
// is not JSON records goes as it is, its last excerptBytes. A JSONL tail
// with no conversation text gives none, and says so.
func excerpt(path string) (string, error) {
	tail, cut, err := readTail(path, excerptWindow)
	if err != nil {
		return "", err
	}
	lines := strings.Split(tail, "\n")
	if cut {
		// The window has one byte before it: its first line is partial,
		// or empty when the window starts at a line start.
		lines = lines[1:]
		if strings.TrimSpace(strings.Join(lines, "")) == "" {
			return "", fmt.Errorf("the transcript's last record is larger than %d KB", excerptWindow>>10)
		}
	}
	turns, records := transcriptTurns(lines)
	if records == 0 {
		raw := rawExcerpt(tail, cut)
		if raw == "" {
			return "", fmt.Errorf("the transcript's end is empty")
		}
		return raw, nil
	}
	if len(turns) == 0 {
		return "", fmt.Errorf("no conversation text in the transcript's last %d KB", excerptWindow>>10)
	}
	return fenced(budgetTurns(turns, excerptBytes)), nil
}

// excerptWindow is how much of a transcript's end excerpt reads.
const excerptWindow = 512 << 10

// readTail reads the last n bytes of a file; cut says the file is longer,
// and then the byte before them comes too, so the caller can tell a window
// that starts at a line start.
func readTail(path string, n int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	cut := fi.Size() > n
	if cut {
		n++
		if _, err := f.Seek(-n, io.SeekEnd); err != nil {
			return "", false, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, n))
	if err != nil {
		return "", false, err
	}
	return string(bytes.ToValidUTF8(data, nil)), cut, nil
}

// rawExcerpt is a tail's last excerptBytes as they are, from a line start;
// "" when nothing is left.
func rawExcerpt(tail string, cut bool) string {
	if len(tail) > excerptBytes {
		tail, cut = tail[len(tail)-excerptBytes:], true
	}
	if i := strings.IndexByte(tail, '\n'); i >= 0 && cut {
		tail = tail[i+1:]
	}
	text := strings.TrimSpace(strings.ToValidUTF8(tail, ""))
	if text == "" {
		return ""
	}
	return fenced(text)
}

// turn is one conversation turn of a transcript.
type turn struct{ role, text string }

// transcriptRecord is what excerpt reads of a transcript line: Claude
// Code's user and assistant records (message), and Codex's response items
// (payload).
type transcriptRecord struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Message          *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Payload *struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"payload"`
	// Attachment holds what Claude Code records of a prompt typed while it
	// works: a queued_command in prompt mode.
	Attachment *struct {
		Type        string          `json:"type"`
		CommandMode string          `json:"commandMode"`
		Prompt      json.RawMessage `json:"prompt"`
	} `json:"attachment"`
}

// transcriptTurns reads the conversation turns of transcript lines, and
// how many lines are JSON records at all: a line that starts as an object
// counts, so a partial or odd record never makes a transcript look like
// plain text.
func transcriptTurns(lines []string) ([]turn, int) {
	var turns []turn
	records := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		records++
		var r transcriptRecord
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		var role string
		var texts []string
		switch {
		case r.Type == "attachment" && r.Attachment != nil && r.Attachment.Type == "queued_command" &&
			r.Attachment.CommandMode == "prompt":
			// Claude Code: a prompt typed while it worked.
			role, texts = "user", contentTexts(r.Attachment.Prompt, "text")
		case (r.Type == "user" || r.Type == "assistant") && r.Message != nil:
			// Claude Code. A meta record and a compaction summary are not
			// what was said.
			if r.IsMeta || r.IsCompactSummary {
				continue
			}
			role, texts = r.Message.Role, contentTexts(r.Message.Content, "text")
		case r.Type == "response_item" && r.Payload != nil && r.Payload.Type == "message":
			// Codex; its developer messages are instructions.
			role, texts = r.Payload.Role, contentTexts(r.Payload.Content, "input_text", "output_text")
		}
		if role != "user" && role != "assistant" {
			continue
		}
		var kept []string
		for _, t := range texts {
			t = strings.TrimSpace(t)
			if t == "" || (role == "user" && injectedText(t)) {
				continue
			}
			kept = append(kept, t)
		}
		if len(kept) > 0 {
			turns = append(turns, turn{role: role, text: strings.Join(kept, "\n\n")})
		}
	}
	return turns, records
}

// contentTexts is a message content's text: the content itself when it is
// a string, else its items of the given types.
func contentTexts(raw json.RawMessage, types ...string) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		for _, t := range types {
			if it.Type == t {
				out = append(out, it.Text)
			}
		}
	}
	return out
}

// injectedPrefixes start the text that a harness puts in a user turn:
// Claude Code's notices, command echoes and shell output, Codex's context,
// hooks, shell commands and image tags. What the user typed stays, a
// shell command (<bash-input>) and the interrupt marker too.
var injectedPrefixes = []string{"<task-notification", "<system-reminder", "<command-name", "<command-message",
	"<command-args", "<local-command-stdout", "<local-command-stderr", "<local-command-caveat",
	"<bash-stdout", "<bash-stderr", "<environment_context", "<user_instructions", "# AGENTS.md instructions",
	"<hook_prompt", "<user_shell_command", "<image name=", "<no retained transcript"}

func injectedText(t string) bool {
	for _, p := range injectedPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// budgetTurns renders the newest turns that fit in budget bytes, oldest
// first. When even the newest does not fit, its end is kept.
func budgetTurns(turns []turn, budget int) string {
	var kept []string
	used := 0
	for i := len(turns) - 1; i >= 0; i-- {
		label := "[" + turns[i].role + "] "
		s := label + turns[i].text
		sep := 0
		if len(kept) > 0 {
			sep = 2
		}
		if used+sep+len(s) > budget {
			if len(kept) == 0 {
				kept = append(kept, label+"…"+tailBytes(turns[i].text, budget-len(label)-len("…")))
			}
			break
		}
		kept = append(kept, s)
		used += sep + len(s)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return strings.Join(kept, "\n\n")
}

// tailBytes is s's last n bytes or fewer, from a rune start.
func tailBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// fenced puts text in a code fence longer than any backtick run in it.
func fenced(text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence + "\n" + text + "\n" + fence
}
