package lifecycle

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

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
	// As cass.Exec runs cass: no prompts, no refresh, no progress events.
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

// excerpt is the last excerptBytes of a transcript.
func excerpt(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.Size() > excerptBytes {
		if _, err := f.Seek(-excerptBytes, io.SeekEnd); err != nil {
			return "", err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, excerptBytes))
	if err != nil {
		return "", err
	}
	text := string(bytes.ToValidUTF8(data, nil))
	if i := strings.IndexByte(text, '\n'); i >= 0 && fi.Size() > excerptBytes {
		text = text[i+1:]
	}
	return "```\n" + strings.TrimSpace(text) + "\n```", nil
}
