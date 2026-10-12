package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
)

const feedbackUsage = `Usage: nhm feedback list [--op OP] [--name N] [--bead ID] [--socket PATH] [--json]
       nhm feedback mark <op-id> <rule-id> --helpful|--harmful [--name N] [--socket PATH] [--json]
       nhm feedback outcomes [--socket PATH] [--json]

list shows the CM rules that each agent's session received, per send:
the rules included in the block, and the ones left out because that
context already had them. Only a submitted target received them.

mark tells CM (cm mark) that a rule a send gave was helpful or harmful,
with the agent's transcript as the session. --name picks the agent when
more than one got the rule. A repeat of the same mark is a no-op.

outcomes runs the learning profile closed-assigned of config.toml
([learning] implicit_outcome): for a bead that closed while assigned to
the agent that got rules for it, nhm runs cm outcome success and
cm outcome-apply, once per bead, agent and generation. Run it after beads
close; nothing runs it on its own yet. nhm never runs cm reflect.
`

func runFeedback(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stderr, feedbackUsage)
		return exitRefused
	}
	verb := args[0]
	fs := flag.NewFlagSet("feedback "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, feedbackUsage) }
	op := fs.String("op", "", "an operation id")
	name := fs.String("name", "", "an Agent Mail name")
	bead := fs.String("bead", "", "a bead id")
	helpful := fs.Bool("helpful", false, "the rule helped")
	harmful := fs.Bool("harmful", false, "the rule did harm")
	socket := fs.String("socket", "", "herdr API socket")
	asJSON := fs.Bool("json", false, "nhm's JSON envelope")
	pos, err := parseInterleaved(fs, args[1:])
	if err != nil {
		return exitRefused
	}
	env := envelope{Command: "feedback " + verb}
	refuse := func(err error) int { return output(stdout, stderr, *asJSON, env, err, exitRefused, nil) }
	switch verb {
	case "list", "outcomes":
		if len(pos) > 0 {
			return refuse(fmt.Errorf("unexpected arguments: %s", strings.Join(pos, " ")))
		}
	case "mark":
		if len(pos) != 2 || *helpful == *harmful {
			return refuse(errors.New("give <op-id> <rule-id> and one of --helpful or --harmful"))
		}
	default:
		return refuse(fmt.Errorf("unknown verb %q: list, mark or outcomes", verb))
	}
	cfg, err := loadEnrichConfig()
	if err != nil {
		return refuse(err)
	}
	ie, err := openInstance(*socket)
	if err != nil {
		return output(stdout, stderr, *asJSON, env, err, exitError, nil)
	}
	defer ie.db.Close()
	ctx := context.Background()
	cmc, _, _ := enrichClients(cfg)
	fenv := enrich.FeedbackEnv{DB: ie.db, CM: cmc, Transcripts: transcriptFinder(ie.db)}
	switch verb {
	case "list":
		list, err := enrich.List(ctx, ie.db, enrich.Filter{OperationID: *op, MailName: *name, BeadID: *bead})
		env.Result = list
		if err != nil {
			return output(stdout, stderr, *asJSON, env, err, exitError, nil)
		}
		return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) { printReceived(w, list) })
	case "mark":
		res, err := enrich.Mark(ctx, fenv, pos[0], pos[1], *name, *helpful)
		env.OperationID, env.Result = pos[0], res
		if err != nil {
			code := exitError
			if errors.Is(err, enrich.ErrNotReceived) || errors.Is(err, enrich.ErrAmbiguous) || errors.Is(err, enrich.ErrMarkedOther) {
				code = exitRefused
			}
			return output(stdout, stderr, *asJSON, env, err, code, nil)
		}
		return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
			switch {
			case res.Repeat:
				fmt.Fprintf(w, "%s was marked %s for %s before; nothing changed\n", res.RuleID, res.Outcome, res.MailName)
			default:
				fmt.Fprintf(w, "%s marked %s for %s (session %s)\n", res.RuleID, res.Outcome, res.MailName, dash(res.Session))
			}
		})
	}
	// outcomes
	if cfg.Learning.ImplicitOutcome != enrich.LearningClosedAssigned {
		env.Result = []enrich.OutcomeResult{}
		return output(stdout, stderr, *asJSON, env, nil, exitOK, func(w io.Writer) {
			fmt.Fprintln(w, "the learning profile is off ([learning] implicit_outcome in config.toml)")
		})
	}
	results, err := enrich.ImplicitOutcomes(ctx, fenv, brBead)
	env.Result = results
	code := exitOK
	for _, r := range results {
		if r.State == "failed" {
			code = exitError
		}
	}
	return output(stdout, stderr, *asJSON, env, err, code, func(w io.Writer) {
		if len(results) == 0 {
			fmt.Fprintln(w, "no bead has rules submitted for it")
		}
		for _, r := range results {
			line := fmt.Sprintf("%s %s (generation %d): %s", r.BeadID, r.MailName, r.Generation, strings.ReplaceAll(r.State, "_", " "))
			if r.Detail != "" {
				line += ": " + r.Detail
			}
			fmt.Fprintln(w, line)
		}
	})
}

func printReceived(w io.Writer, list []enrich.Received) {
	if len(list) == 0 {
		fmt.Fprintln(w, "no send gave CM rules")
	}
	for _, r := range list {
		fmt.Fprintf(w, "%s %s %s (%s)", r.At.Format(time.RFC3339), r.OperationID, dash(r.MailName), strings.ReplaceAll(r.Submission, "_", " "))
		if r.BeadID != "" {
			fmt.Fprintf(w, " bead %s", r.BeadID)
		}
		fmt.Fprintln(w)
		for _, it := range r.Rules {
			note := ""
			if it.Status == enrich.Suppressed {
				note = " (already in this context)"
			}
			fmt.Fprintf(w, "  %s v%s%s\n", it.ID, it.Version, note)
		}
	}
}

// transcriptFinder finds a binding's transcript under the provider's own
// root: Claude's ~/.claude/projects/<dir>/<session>.jsonl, Codex's
// ~/.codex/sessions/YYYY/MM/DD/rollout-*-<session>.jsonl. Other kinds
// have none yet; the kind registry (nhm.kinds) knows more later.
func transcriptFinder(db *store.DB) enrich.Transcripts {
	return func(ctx context.Context, bindingID string) (string, error) {
		b, err := bindings.NewStore(db).Get(ctx, bindingID)
		if err != nil {
			return "", err
		}
		if b.NativeSession == nil || b.NativeSession.Value == "" {
			return "", nil
		}
		return findTranscript(b.Kind, b.NativeSession.Value)
	}
}

func findTranscript(kind, session string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// The id builds a glob: only a plain session id, as every argv takes it.
	if !kinds.SessionPattern.MatchString(session) {
		return "", fmt.Errorf("session id %q is not a plain id", session)
	}
	var pattern string
	switch kind {
	case "claude":
		pattern = filepath.Join(home, ".claude", "projects", "*", session+".jsonl")
	case "codex":
		root := os.Getenv("CODEX_HOME")
		if root == "" {
			root = filepath.Join(home, ".codex")
		}
		pattern = filepath.Join(root, "sessions", "*", "*", "*", "rollout-*-"+session+".jsonl")
	default:
		return "", nil
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return "", err
	}
	return matches[0], nil
}

// brBead reads a bead's status and assignee with br show --json in the
// project root.
func brBead(ctx context.Context, projectRoot, id string) (enrich.Bead, error) {
	if projectRoot == "" {
		return enrich.Bead{}, fmt.Errorf("bead %s: the send has no project", id)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "br", "show", id, "--json")
	cmd.Dir = projectRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return enrich.Bead{}, fmt.Errorf("br show %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
	}
	type row struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	var list []row
	if err := json.Unmarshal(out, &list); err != nil {
		var one row
		if err := json.Unmarshal(out, &one); err != nil {
			return enrich.Bead{}, fmt.Errorf("br show %s: %w", id, err)
		}
		list = []row{one}
	}
	for _, r := range list {
		if r.ID == id {
			return enrich.Bead{Status: r.Status, Assignee: r.Assignee}, nil
		}
	}
	return enrich.Bead{}, fmt.Errorf("br show %s: the bead is not in the answer", id)
}
