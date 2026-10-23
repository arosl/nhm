package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/panediff"
	"github.com/arosl/nhm/internal/herdr"
)

const diffUsage = `Usage: nhm diff [session] <pane1> <pane2> [-u|--unified] [--code-only] [--socket PATH] [--json]

ntm's diff on herdr: compare the output of two agents' panes (the last
1000 lines of each): their line counts and how similar they are, as a
percentage. -u adds the diff (diff-match-patch's patch text, as ntm
prints it); --code-only compares only the fenced code blocks. A pane is
a slot N.M, a herdr pane id, an Agent Mail or herdr name, or a kind (its
first agent), as nhm zoom takes it; ntm's tmux pane indexes do not exist
on herdr. Without a session, the calling pane's workspace. With --json:
nhm's envelope with ntm's fields (pane1, pane2, lines1, lines2,
similarity, diff).
`

// diffLines is how much of each pane diff reads, as ntm's capture.
const diffLines = 1000

func runDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, diffUsage) }
	var unified bool
	fs.BoolVar(&unified, "unified", false, "show the diff")
	fs.BoolVar(&unified, "u", false, "show the diff")
	codeOnly := fs.Bool("code-only", false, "compare only the fenced code blocks")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) < 2 || len(pos) > 3 {
		fmt.Fprint(stderr, diffUsage)
		return exitRefused
	}
	session := ""
	if len(pos) == 3 {
		session, pos = pos[0], pos[1:]
	}
	env := envelope{Command: "diff"}
	ctx := context.Background()
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	ws, err := sessionWorkspace(ctx, ie.herdr, session)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, fmt.Errorf("%w (nhm list shows the sessions)", err), exitRefused, nil)
	}
	bs := bindings.NewStore(ie.db)
	var titles, contents [2]string
	for i, pane := range pos {
		t, err := zoomTarget(ctx, ie.herdr, bs, ws, pane)
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
		}
		if t.MailName == "" {
			// A pane id: the name of the agent bound there, if any.
			if b, err := bs.ActiveByPane(ctx, t.PaneID); err == nil {
				t.MailName = b.MailName
			}
		}
		n := diffLines
		read, err := ie.herdr.PaneRead(ctx, herdr.PaneReadParams{PaneID: t.PaneID, Source: herdr.ReadRecent, Lines: &n})
		if err != nil {
			return output(stdout, stderr, *asJSON, env, fmt.Errorf("capturing pane %d: %w", i+1, err), exitError, nil)
		}
		titles[i], contents[i] = firstOf(t.MailName, t.PaneID), stripControl(read.Text)
		if *codeOnly {
			contents[i] = codeBlocks(contents[i])
		}
	}
	res := panediff.ComputeDiff(titles[0], contents[0], titles[1], contents[1])
	env.Result = res
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		fmt.Fprintf(w, "Comparing %s vs %s:\n", res.Pane1, res.Pane2)
		fmt.Fprintf(w, "  Lines: %d vs %d\n", res.LineCount1, res.LineCount2)
		fmt.Fprintf(w, "  Similarity: %.1f%%\n", res.Similarity*100)
		if unified {
			fmt.Fprintf(w, "\nDiff:\n%s\n", res.UnifiedDiff)
		}
	})
}

// codeBlocks is ntm's --code-only text: each fenced block as
// "// <language>", its content and a blank line.
func codeBlocks(text string) string {
	var b strings.Builder
	for _, blk := range panediff.ExtractFromText(text) {
		fmt.Fprintf(&b, "// %s\n%s\n\n", blk.Language, blk.Content)
	}
	return b.String()
}
