package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

const readUsage = `Usage: nhm read <name|herdr-name|pane-id> [--lines N] [--source visible|recent] [--raw-to FILE]
                [--socket PATH] [--json]

Print an agent's pane: the visible screen (agent.read), or the scrollback
with --source recent (pane.read). The preview has no control sequences;
--raw-to FILE writes the raw text with them.
`

// ansiPattern matches CSI and OSC sequences and lone escapes.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// stripControl removes terminal control sequences and control bytes
// other than newline and tab.
func stripControl(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return -1
	}, s)
}

// paneOf finds the herdr pane of a name, a herdr name or a pane id.
func paneOf(ctx context.Context, h *herdr.Client, db *store.DB, target string) (string, error) {
	if strings.Contains(target, ":") {
		return target, nil
	}
	if db != nil {
		r, err := bindings.Resolve(ctx, bindings.NewStore(db), nil, bindings.Query{Target: target}, os.Getenv)
		if err == nil && r.Binding.Status == bindings.Active && r.Binding.PaneID != "" {
			return r.Binding.PaneID, nil
		}
		if err != nil && !errors.Is(err, bindings.ErrNotFound) {
			return "", err
		}
	}
	a, err := h.AgentGet(ctx, strings.ToLower(target))
	if err != nil {
		return "", fmt.Errorf("no binding and no herdr agent named %s: %w", target, err)
	}
	return a.PaneID, nil
}

func runRead(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, readUsage) }
	lines := fs.Int("lines", 40, "lines to read")
	source := fs.String("source", "visible", "visible or recent")
	rawTo := fs.String("raw-to", "", "write the raw text to this file")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) != 1 || (*source != "visible" && *source != "recent") || *lines <= 0 {
		fmt.Fprint(stderr, readUsage)
		return exitRefused
	}
	env := envelope{Command: "read"}
	ctx := context.Background()
	r, err := openReader(*socket, false)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer r.close()
	pane, err := paneOf(ctx, r.herdr, r.db, pos[0])
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	format := herdr.FormatText
	if *rawTo != "" {
		format = herdr.FormatANSI
	}
	n := *lines
	var read herdr.PaneRead
	if *source == "visible" {
		read, err = r.herdr.AgentRead(ctx, herdr.AgentReadParams{Target: pane, Source: herdr.ReadVisible, Format: format, Lines: &n})
		if herdr.ErrorCode(err) == herdr.CodeAgentNotFound {
			// A pane without an agent: read the screen of the pane itself.
			read, err = r.herdr.PaneRead(ctx, herdr.PaneReadParams{PaneID: pane, Source: herdr.ReadVisible, Format: format, Lines: &n})
		}
	} else {
		read, err = r.herdr.PaneRead(ctx, herdr.PaneReadParams{PaneID: pane, Source: herdr.ReadRecent, Format: format, Lines: &n})
	}
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	if *rawTo != "" {
		if err := os.WriteFile(*rawTo, []byte(read.Text), 0o600); err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitError, nil)
		}
	}
	read.Text = stripControl(read.Text)
	env.Result = read
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
		fmt.Fprint(w, read.Text)
		if !strings.HasSuffix(read.Text, "\n") && read.Text != "" {
			fmt.Fprintln(w)
		}
	})
}

const watchUsage = `Usage: nhm watch <session> [name|slot ...] [--lines N] [--interval D] [--count N] [--socket PATH]

Follow the output of a session's agents (or the named ones): every
interval, read each pane's last lines and print the lines that are new.
`

func runWatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, watchUsage) }
	lines := fs.Int("lines", 20, "lines to read per pane")
	interval := fs.Duration("interval", 2*time.Second, "time between reads")
	count := fs.Int("count", 0, "stop after N rounds (0: until interrupted)")
	socket := fs.String("socket", "", "herdr API socket")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) < 1 || *lines <= 0 || *interval <= 0 {
		fmt.Fprint(stderr, watchUsage)
		return exitRefused
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r, err := openReader(*socket, false)
	if err != nil {
		fmt.Fprintf(stderr, "nhm watch: %v\n", err)
		return exitError
	}
	defer r.close()
	m, _, err := selectWorkspace(r.model(ctx, ""), pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "nhm watch: %v\n", err)
		return exitError
	}
	agents := selectAgents(m.Agents, pos[1:])
	if len(agents) == 0 {
		fmt.Fprintln(stderr, "nhm watch: no agents match")
		return exitError
	}
	last := map[string][]string{}
	for round := 1; ; round++ {
		for _, a := range agents {
			n := *lines
			read, err := r.herdr.PaneRead(ctx, herdr.PaneReadParams{PaneID: a.PaneID, Source: herdr.ReadRecent, Lines: &n})
			if err != nil {
				fmt.Fprintf(stderr, "nhm watch: %s: %v\n", label(a), err)
				continue
			}
			cur := strings.Split(strings.TrimRight(stripControl(read.Text), "\n"), "\n")
			for _, l := range newLines(last[a.PaneID], cur) {
				fmt.Fprintf(stdout, "%s | %s\n", label(a), l)
			}
			last[a.PaneID] = cur
		}
		if *count > 0 && round >= *count {
			return exitOK
		}
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(*interval):
		}
	}
}

// selectAgents keeps the agents named by mail name, herdr name or slot;
// no selector keeps all.
func selectAgents(agents []snapshot.Agent, sel []string) []snapshot.Agent {
	if len(sel) == 0 {
		return agents
	}
	var out []snapshot.Agent
	for _, a := range agents {
		for _, s := range sel {
			if a.Slot == s || strings.EqualFold(a.HerdrName, s) || (a.Binding != nil && strings.EqualFold(a.Binding.MailName, s)) {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

func label(a snapshot.Agent) string {
	switch {
	case a.Binding != nil:
		return a.Binding.MailName
	case a.HerdrName != "":
		return a.HerdrName
	default:
		return a.PaneID
	}
}

// newLines returns the lines of cur after the longest suffix of prev that
// starts cur; all of cur when they do not overlap.
func newLines(prev, cur []string) []string {
	for k := min(len(prev), len(cur)); k > 0; k-- {
		if equalLines(prev[len(prev)-k:], cur[:k]) {
			return cur[k:]
		}
	}
	return cur
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
