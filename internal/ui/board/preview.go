package board

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// The preview's bounds: it reads the selected agent's pane only, at most
// PreviewLines lines and PreviewBytes bytes (plan-v4 section 7).
const (
	PreviewLines    = 40
	PreviewBytes    = 16 << 10
	PreviewEvery    = 2 * time.Second
	PreviewDeadline = 2 * time.Second
	SourcePreview   = "preview"
)

// PaneReader reads a pane; *herdr.Client has it.
type PaneReader interface {
	PaneRead(ctx context.Context, p herdr.PaneReadParams) (herdr.PaneRead, error)
}

// Preview is one read of the selected agent's pane, with its control
// sequences removed. Key says which row it was read for: the board shows
// it only while that row is selected.
type Preview struct {
	Key       Key
	PaneID    string
	Text      string
	Truncated bool
}

// previewSource is the preview feed and the row it reads.
type previewSource struct {
	feed *Feed[Preview]

	mu   sync.Mutex
	key  Key
	pane string
}

func newPreview(r PaneReader, now func() time.Time, notify func()) *previewSource {
	p := &previewSource{}
	p.feed = NewFeed(FeedConfig[Preview]{Name: SourcePreview, Interval: PreviewEvery, Deadline: PreviewDeadline,
		Now: now, Changed: notify, Read: func(ctx context.Context) (Preview, error) {
			p.mu.Lock()
			key, pane := p.key, p.pane
			p.mu.Unlock()
			if pane == "" {
				return Preview{Key: key}, nil
			}
			lines := PreviewLines
			strip := true
			out, err := r.PaneRead(ctx, herdr.PaneReadParams{PaneID: pane, Source: herdr.ReadRecent, Format: herdr.FormatText,
				Lines: &lines, StripANSI: &strip})
			if err != nil {
				return Preview{}, err
			}
			text, cut := bound(Clean(out.Text), PreviewLines, PreviewBytes)
			return Preview{Key: key, PaneID: pane, Text: text, Truncated: cut || out.Truncated}, nil
		}})
	return p
}

// target points the preview at a row; a new row is read at once.
func (p *previewSource) target(r Row, ok bool) {
	key, pane := Key{}, ""
	if ok && r.Agent != nil {
		key, pane = r.Key, r.Agent.PaneID
	}
	p.mu.Lock()
	same := key == p.key && pane == p.pane
	p.key, p.pane = key, pane
	p.mu.Unlock()
	if !same {
		p.feed.Refresh()
	}
}

// ansiPattern matches CSI and OSC sequences and lone escapes.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// Clean removes terminal control sequences and control bytes other than
// newline and tab, so pane text cannot move the cursor, set a title or
// change colours on the board.
func Clean(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f) {
			return r
		}
		return -1
	}, s)
}

// bound keeps the last lines lines and at most n bytes of s, cut at a
// line start; cut says whether it dropped anything.
func bound(s string, lines, n int) (string, bool) {
	s = strings.TrimRight(s, "\n")
	cut := false
	if parts := strings.Split(s, "\n"); len(parts) > lines {
		s, cut = strings.Join(parts[len(parts)-lines:], "\n"), true
	}
	if len(s) > n {
		s, cut = s[len(s)-n:], true
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.ToValidUTF8(s, "")
	}
	return s, cut
}
