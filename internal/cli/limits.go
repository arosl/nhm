package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/store"
)

const limitsUsage = `Usage: nhm limits [session] [--once | --loop [--interval D]] [--lines N]
                  [--mail-from NAME [--mail-to NAME]] [--dry-run] [--socket PATH] [--json]

Report each agent that a usage or rate limit stopped: one that is not
working and whose screen's last --lines lines that are not blank (default
15) show its kind's limit message. Each message is reported once, until it
leaves the screen. With --mail-from NAME, Agent Mail carries one message
per limit event from NAME to --mail-to (default NAME itself, the
operator's agent), in the agent's project. A match never starts an action:
an agent can print a fake limit line. --once (the default) runs one pass; --loop repeats it.
`

// limitReport is one agent newly stopped at a limit.
type limitReport struct {
	PaneID   string `json:"pane_id"`
	Slot     string `json:"slot,omitempty"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Line     string `json:"line"`
	Mailed   bool   `json:"mailed"`
	MailNote string `json:"mail_note,omitempty"`
}

func runLimits(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("limits", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, limitsUsage) }
	fs.Bool("once", false, "one pass (the default)")
	loop := fs.Bool("loop", false, "repeat every --interval")
	interval := fs.Duration("interval", time.Minute, "time between passes with --loop")
	lines := fs.Int("lines", 15, "screen lines to read, not counting blank ones")
	mailFrom := fs.String("mail-from", "", "Agent Mail sender for the limit messages")
	mailTo := fs.String("mail-to", "", "Agent Mail recipient (default: the sender)")
	dryRun := fs.Bool("dry-run", false, "say what would be reported")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 1 || *lines <= 0 || *interval <= 0 || (*mailTo != "" && *mailFrom == "") {
		fmt.Fprint(stderr, limitsUsage)
		return exitRefused
	}
	session := ""
	if len(pos) == 1 {
		session = pos[0]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := envelope{Command: "limits"}
	r, err := openReader(*socket, false)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer r.close()
	seenPath, err := limitsSeenPath(r.inst)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	var mail *agentmail.Client
	if *mailFrom != "" && !*dryRun {
		if mail, err = agentmail.NewClient(""); err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitError, nil)
		}
	}
	to := *mailTo
	if to == "" {
		to = *mailFrom
	}
	for {
		reports, warnings, err := limitsPass(ctx, r, session, *lines, seenPath, mail, *mailFrom, to, *dryRun)
		env.Result, env.Warnings = map[string]any{"reports": reports, "dry_run": *dryRun}, warnings
		code := exitOK
		if err != nil {
			code = exitError
		}
		if !*loop || err != nil {
			return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) { printLimits(w, reports, *dryRun) })
		}
		printLimits(stdout, reports, *dryRun)
		for _, w := range warnings {
			fmt.Fprintf(stderr, "nhm limits: warning: %s\n", w)
		}
		select {
		case <-ctx.Done():
			return exitOK
		case <-time.After(*interval):
		}
	}
}

// limitsSeenPath is where the reported lines live: in the instance
// directory, or under nhm's state root for a server not enrolled.
func limitsSeenPath(inst *store.Instance) (string, error) {
	if inst != nil {
		return filepath.Join(inst.Dir, "limits-seen.json"), nil
	}
	root, err := store.StateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "limits-seen.json"), nil
}

func printLimits(w io.Writer, reports []limitReport, dryRun bool) {
	verb := "stopped at a limit"
	if dryRun {
		verb = "would be reported"
	}
	for _, rep := range reports {
		fmt.Fprintf(w, "%s %s (%s, %s) %s: %s\n", time.Now().UTC().Format(time.RFC3339), rep.Name, rep.Kind, rep.PaneID, verb, rep.Line)
		if rep.MailNote != "" {
			fmt.Fprintf(w, "  mail: %s\n", rep.MailNote)
		}
	}
}

// limitsPass reads each agent that is not working, finds new limit
// lines, mails about them, and saves what it saw.
func limitsPass(ctx context.Context, r *reader, session string, lines int, seenPath string, mail *agentmail.Client,
	from, to string, dryRun bool) ([]limitReport, []string, error) {
	m, _, err := selectWorkspace(r.model(ctx, ""), session)
	if err != nil {
		return nil, nil, err
	}
	if !m.HerdrOK() {
		return nil, nil, fmt.Errorf("herdr did not answer: %s", m.Sources[snapshot.SourceHerdr].Error)
	}
	seen, err := quota.LoadSeen(seenPath)
	if err != nil {
		return nil, nil, err
	}
	reports := []limitReport{}
	var warnings []string
	for _, a := range m.Agents {
		// A limit stops the agent, so a working one has not hit one.
		if a.State == herdr.StatusWorking {
			continue
		}
		// The whole screen: its last lines can be blank (nhm-1q7).
		read, err := r.herdr.AgentRead(ctx, herdr.AgentReadParams{Target: a.PaneID, Source: herdr.ReadVisible})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not read %s: %v", a.PaneID, err))
			continue
		}
		line := quota.FindLimit(a.Kind, quota.LastLines(stripControl(read.Text), lines))
		if dryRun {
			if line != "" && seen.Lines[a.PaneID] != line {
				reports = append(reports, limitReport{PaneID: a.PaneID, Slot: a.Slot, Name: label(a), Kind: a.Kind, Line: line})
			}
			continue
		}
		if !seen.Update(a.PaneID, line) {
			continue
		}
		rep := limitReport{PaneID: a.PaneID, Slot: a.Slot, Name: label(a), Kind: a.Kind, Line: line}
		if mail != nil {
			rep.Mailed, rep.MailNote = sendLimitMail(ctx, mail, a, from, to, line)
		}
		reports = append(reports, rep)
	}
	if !dryRun {
		if err := seen.Save(); err != nil {
			return reports, warnings, err
		}
	}
	return reports, warnings, nil
}

// sendLimitMail mails one limit event, in the agent's project; an unbound
// agent has no project, and gets no mail.
func sendLimitMail(ctx context.Context, mail *agentmail.Client, a snapshot.Agent, from, to, line string) (bool, string) {
	if a.Binding == nil {
		return false, "not mailed: the agent has no binding, so nhm does not know its project"
	}
	subject := fmt.Sprintf("[limits] %s stopped at a usage or rate limit", a.Binding.MailName)
	body := fmt.Sprintf("herdr showed %s (%s, pane %s) stopped at a limit at %s:\n\n    %s\n\nIts work waits until the limit resets; hand it over if it cannot wait.",
		a.Binding.MailName, a.Kind, a.PaneID, time.Now().UTC().Format(time.RFC3339), line)
	err := mail.Call(ctx, "send_message", map[string]any{"project_key": a.Binding.ProjectKey, "sender_name": from,
		"to": []string{to}, "subject": subject, "body_md": body}, nil)
	if err != nil {
		return false, "not mailed: " + err.Error()
	}
	return true, "mailed " + to
}
