package cli

import (
	"context"
	"encoding/json"
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

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
)

const logsUsage = `Usage: nhm logs <session> [--panes N.M,…] [-f|--follow] [--filter RE] [-n|--limit N]
                [-a|--aggregate] [--socket PATH] [--json]
       nhm logs <name|herdr-name|pane-id> [read's flags]

ntm's logs on herdr: the recent output of every agent of the session (a
workspace id or label), each under its own header. -n keeps each pane's
last N lines (default 100); --filter keeps the lines that match a
regular expression; --panes keeps the agents in these slots (or herdr
pane ids), not ntm's pane indexes; -a prints the lines with a
[type:slot] prefix in place of the headers; -f prints each new line as
it comes, until Ctrl+C. With --json: nhm's envelope with ntm's fields;
with -f, one JSON object per line.

nhm's extra: an agent's name or pane id in place of the session runs
nhm read for that agent. A session wins over an agent of the same name.
`

// logsOptions are logs' flags for a session.
type logsOptions struct {
	panes     string
	follow    bool
	filter    string
	limit     int
	aggregate bool
}

// PaneLogs is one agent's lines: ntm's PaneLogs, with the slot for ntm's
// pane index, and the pane id and name beside it.
type PaneLogs struct {
	Pane       string    `json:"pane"`
	PaneID     string    `json:"pane_id"`
	Name       string    `json:"name,omitempty"`
	AgentType  string    `json:"agent_type"`
	Lines      []string  `json:"lines"`
	LineCount  int       `json:"line_count"`
	Truncated  bool      `json:"truncated"`
	CapturedAt time.Time `json:"captured_at"`
}

// LogsSummary is ntm's LogsSummary.
type LogsSummary struct {
	TotalPanes     int `json:"total_panes"`
	TotalLines     int `json:"total_lines"`
	TruncatedPanes int `json:"truncated_panes"`
}

// LogsResult is nhm logs <session>'s result: ntm's LogsOutput fields.
type LogsResult struct {
	Session    string      `json:"session"`
	CapturedAt time.Time   `json:"captured_at"`
	Panes      []PaneLogs  `json:"panes"`
	Summary    LogsSummary `json:"summary"`
}

// LogLine is one line of the -a view, as ntm's AggregatedLogEntry.
type LogLine struct {
	Pane      string    `json:"pane"`
	AgentType string    `json:"agent_type"`
	Line      string    `json:"line"`
	Timestamp time.Time `json:"timestamp"`
}

// FollowedLine is one line of -f --json, as ntm prints it: the time in
// RFC 3339, to the second.
type FollowedLine struct {
	Pane      string `json:"pane"`
	AgentType string `json:"agent_type"`
	Line      string `json:"line"`
	Timestamp string `json:"timestamp"`
}

// AggregatedLogs is nhm logs <session> -a's result: ntm's
// AggregatedLogsOutput fields.
type AggregatedLogs struct {
	Session    string      `json:"session"`
	CapturedAt time.Time   `json:"captured_at"`
	Entries    []LogLine   `json:"entries"`
	Summary    LogsSummary `json:"summary"`
}

// logsFollowInterval is the time between two reads of logs -f (ntm's
// one second); logsFollowRounds stops it after that many reads, for the
// tests (0: until interrupted).
var (
	logsFollowInterval = time.Second
	logsFollowRounds   = 0
)

func runLogs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, logsUsage) }
	var o logsOptions
	fs.StringVar(&o.panes, "panes", "", "the slots (or pane ids) to read, comma-separated")
	fs.BoolVar(&o.follow, "follow", false, "print new lines as they come")
	fs.BoolVar(&o.follow, "f", false, "print new lines as they come")
	fs.StringVar(&o.filter, "filter", "", "keep the lines that match this regular expression")
	fs.IntVar(&o.limit, "limit", 100, "the last lines per pane")
	fs.IntVar(&o.limit, "n", 100, "the last lines per pane")
	fs.BoolVar(&o.aggregate, "aggregate", false, "one list of lines, each with its pane")
	fs.BoolVar(&o.aggregate, "a", false, "one list of lines, each with its pane")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	// read's flags, for the name form.
	fs.Int("lines", 40, "read's lines")
	fs.String("source", "visible", "read's source")
	fs.String("raw-to", "", "read's raw file")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitRefused
	}
	if len(pos) != 1 || pos[0] == "" {
		fmt.Fprint(stderr, logsUsage)
		return exitRefused
	}
	if paneIDPattern.MatchString(pos[0]) {
		return runRead(args, stdout, stderr)
	}
	env := envelope{Command: "logs"}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r, err := openReader(*socket, false)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	m, w, err := selectWorkspace(r.model(ctx, ""), pos[0])
	if errors.Is(err, snapshot.ErrNoWorkspace) {
		// No such session: nhm's extra, one agent by name.
		r.close()
		return runRead(args, stdout, stderr)
	}
	defer r.close()
	if errors.Is(err, snapshot.ErrAmbiguousWorkspace) {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	var readFlags []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "lines" || f.Name == "source" || f.Name == "raw-to" {
			readFlags = append(readFlags, "--"+f.Name)
		}
	})
	if len(readFlags) > 0 {
		err := fmt.Errorf("%s: read's flags take an agent, not a session; -n sets the lines per pane", strings.Join(readFlags, ", "))
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	if o.limit <= 0 {
		fmt.Fprint(stderr, logsUsage)
		return exitRefused
	}
	var filterRe *regexp.Regexp
	if o.filter != "" {
		if filterRe, err = regexp.Compile(o.filter); err != nil {
			return output(stdout, stderr, *asJSON, env, fmt.Errorf("invalid filter regex: %w", err), exitRefused, nil)
		}
	}
	agents, err := logsAgents(m.Agents, o.panes)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitRefused, nil)
	}
	session := firstOf(w.Label, w.ID)
	if o.follow {
		return followLogs(ctx, r.herdr, session, agents, filterRe, *asJSON, stdout)
	}
	res := readLogs(ctx, r.herdr, session, agents, o.limit, filterRe)
	if o.aggregate {
		agg := aggregateLogs(res)
		env.Result = agg
		return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
			for _, e := range agg.Entries {
				fmt.Fprintf(w, "%s %s\n", logPrefix(e.AgentType, e.Pane), e.Line)
			}
		})
	}
	env.Result = res
	return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) { printLogs(w, res) })
}

// logsAgents keeps the agents that --panes names (all without it). A
// pane is a slot N.M or a herdr pane id; ntm's pane indexes do not exist
// on herdr.
func logsAgents(agents []snapshot.Agent, panes string) ([]snapshot.Agent, error) {
	if panes == "" {
		return agents, nil
	}
	want := map[string]bool{}
	for _, p := range strings.Split(panes, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case indexPattern.MatchString(p):
			return nil, fmt.Errorf("pane %s: herdr has no tmux pane indexes; give the slot N.M (nhm status shows it)", p)
		case slotPattern.MatchString(p), paneIDPattern.MatchString(p):
			want[p] = true
		default:
			return nil, fmt.Errorf("pane %s: give a slot N.M or a herdr pane id", p)
		}
	}
	var out []snapshot.Agent
	for _, a := range agents {
		if (a.Slot != "" && want[a.Slot]) || want[a.PaneID] {
			out = append(out, a)
		}
	}
	return out, nil
}

// readLogs reads each agent's recent output as ntm's GetLogs captures a
// pane: twice the limit (at least 200 lines) of scrollback, without
// control sequences, the filter's lines only, no blank lines at the end,
// and the last limit lines.
func readLogs(ctx context.Context, h *herdr.Client, session string, agents []snapshot.Agent, limit int, filterRe *regexp.Regexp) LogsResult {
	res := LogsResult{Session: session, CapturedAt: time.Now().UTC(), Panes: []PaneLogs{}}
	for _, a := range agents {
		pl := PaneLogs{Pane: logsPane(a), PaneID: a.PaneID, AgentType: snapshot.NTMType(a.Kind), Lines: []string{}}
		if a.Binding != nil {
			pl.Name = a.Binding.MailName
		}
		n := max(limit*2, 200)
		read, err := h.PaneRead(ctx, herdr.PaneReadParams{PaneID: a.PaneID, Source: herdr.ReadRecent, Lines: &n})
		pl.CapturedAt = time.Now().UTC()
		if err != nil {
			pl.Lines = []string{fmt.Sprintf("[error capturing pane output: %v]", err)}
		} else {
			pl.Lines, pl.Truncated = logLines(read.Text, limit, filterRe)
		}
		pl.LineCount = len(pl.Lines)
		res.Panes = append(res.Panes, pl)
		res.Summary.TotalPanes++
		res.Summary.TotalLines += pl.LineCount
		if pl.Truncated {
			res.Summary.TruncatedPanes++
		}
	}
	return res
}

// logLines is ntm's line pipeline for one pane's text.
func logLines(text string, limit int, filterRe *regexp.Regexp) ([]string, bool) {
	lines := strings.Split(stripControl(text), "\n")
	if filterRe != nil {
		kept := []string{}
		for _, l := range lines {
			if filterRe.MatchString(l) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > limit {
		return lines[len(lines)-limit:], true
	}
	return lines, false
}

// aggregateLogs is ntm's -a view: every line that is not blank, pane by
// pane, each with its pane.
func aggregateLogs(res LogsResult) AggregatedLogs {
	agg := AggregatedLogs{Session: res.Session, CapturedAt: res.CapturedAt, Entries: []LogLine{}, Summary: res.Summary}
	for _, p := range res.Panes {
		for _, l := range p.Lines {
			if strings.TrimSpace(l) != "" {
				agg.Entries = append(agg.Entries, LogLine{Pane: p.Pane, AgentType: p.AgentType, Line: l, Timestamp: p.CapturedAt})
			}
		}
	}
	agg.Summary.TotalLines = len(agg.Entries)
	return agg
}

// printLogs prints ntm's default view, without its colours.
func printLogs(w io.Writer, res LogsResult) {
	fmt.Fprintf(w, "Logs from session: %s\n", res.Session)
	fmt.Fprintf(w, "Captured at: %s\n\n", res.CapturedAt.Format(time.RFC3339))
	for _, p := range res.Panes {
		head := fmt.Sprintf("=== Pane %s (%s) ===", p.Pane, p.AgentType)
		if p.Name != "" {
			head = fmt.Sprintf("=== Pane %s (%s) %s ===", p.Pane, p.AgentType, p.Name)
		}
		if p.Truncated {
			head += " [truncated]"
		}
		fmt.Fprintln(w, head)
		if len(p.Lines) == 0 {
			fmt.Fprintln(w, "  (no output)")
		}
		for _, l := range p.Lines {
			fmt.Fprintln(w, "  "+l)
		}
		fmt.Fprintln(w)
	}
	total := fmt.Sprintf("Total: %d panes, %d lines", res.Summary.TotalPanes, res.Summary.TotalLines)
	if res.Summary.TruncatedPanes > 0 {
		total += fmt.Sprintf(" (%d truncated)", res.Summary.TruncatedPanes)
	}
	fmt.Fprintln(w, total)
}

// logsLiveLines is the bottom of a pane that a working agent redraws in
// place: its prompt box, status line and spinner. logs -f prints a line
// once it has scrolled above them, so a redrawn line never prints again
// and the spinner never prints (ScarletOrchid's review, nhm-k9d).
const logsLiveLines = 10

// followLogs is ntm's logs -f: a first read sets the baseline, then each
// interval prints the lines that are new since the last read (the
// filter's only, and none that is blank, as ntm), each with its pane and
// time, or as one JSON object. A line is new once it has left the live
// region at the bottom of the pane (logsLiveLines).
func followLogs(ctx context.Context, h *herdr.Client, session string, agents []snapshot.Agent, filterRe *regexp.Regexp, asJSON bool, stdout io.Writer) int {
	if !asJSON {
		fmt.Fprintf(stdout, "Following logs from session: %s\nPress Ctrl+C to stop\n\n", session)
	}
	last := map[string][]string{}
	read := func(a snapshot.Agent) ([]string, bool) {
		n := 200
		r, err := h.PaneRead(ctx, herdr.PaneReadParams{PaneID: a.PaneID, Source: herdr.ReadRecent, Lines: &n})
		if err != nil {
			// A failed read is skipped, as ntm skips a failed poll.
			return nil, false
		}
		lines := strings.Split(strings.TrimRight(stripControl(r.Text), "\n"), "\n")
		return lines[:max(0, len(lines)-logsLiveLines)], true
	}
	for _, a := range agents {
		if cur, ok := read(a); ok {
			last[a.PaneID] = cur
		}
	}
	enc := json.NewEncoder(stdout)
	for round := 1; logsFollowRounds == 0 || round <= logsFollowRounds; round++ {
		select {
		case <-ctx.Done():
			if !asJSON {
				fmt.Fprint(stdout, "\nStopped.\n")
			}
			return exitOK
		case <-time.After(logsFollowInterval):
		}
		now := time.Now().UTC()
		for _, a := range agents {
			cur, ok := read(a)
			if !ok {
				continue
			}
			for _, l := range newLines(last[a.PaneID], cur) {
				if strings.TrimSpace(l) == "" || (filterRe != nil && !filterRe.MatchString(l)) {
					continue
				}
				pane, typ := logsPane(a), snapshot.NTMType(a.Kind)
				if asJSON {
					if err := enc.Encode(FollowedLine{Pane: pane, AgentType: typ, Line: l, Timestamp: now.Format(time.RFC3339)}); err != nil {
						return exitError
					}
					continue
				}
				fmt.Fprintf(stdout, "%s %s %s\n", logPrefix(typ, pane), now.Format("15:04:05"), l)
			}
			last[a.PaneID] = cur
		}
	}
	return exitOK
}

// logsPane is the agent's place in ntm's pane field: its slot, or its
// pane id when it has no binding.
func logsPane(a snapshot.Agent) string {
	return firstOf(a.Slot, a.PaneID)
}

// logPrefix is ntm's [type:pane] prefix, with ntm's short type names.
func logPrefix(agentType, pane string) string {
	return fmt.Sprintf("[%s:%s]", shortAgentType(agentType), pane)
}

// shortAgentType is ntm's short name of an agent type in logs: cc, cod,
// agy, oc, omp, and the first three letters of any other.
func shortAgentType(agentType string) string {
	switch t := strings.ToLower(strings.TrimSpace(agentType)); t {
	case "claude", "cc":
		return "cc"
	case "codex", "cod":
		return "cod"
	case "gemini", "gmi":
		return "gmi"
	case "antigravity", "agy":
		return "agy"
	case "oc", "opencode":
		return "oc"
	case "":
		return "unk"
	default:
		if len(t) > 3 {
			return t[:3]
		}
		return t
	}
}
