// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/send.go (newSendCmd's use, flags, defaults and texts; the
// flag checks of its RunE; sendTargetValue; getPromptContent,
// readPromptFileOrStdin, stdinHasData, buildPrompt; SendResult,
// SendDryRunEntry, SendDryRunResult, sendProjectSessionResult,
// sendProjectResult, buildSendProjectResult; outputSendCommandError;
// printSendDryRunResult; truncatePrompt, truncateForPreview) and
// internal/output/success.go (SendSuggestions); see LICENSE in this
// directory and PROVENANCE.md. Local changes: the panes are nhm's bound
// agents and the send is nhm's core send (receipts, the delivery gate,
// the shell-escape check); a type flag's variant is the binding's model;
// the flags of the send family's later beads refuse until they are built
// (nhm-2p6.5.5, .5.6, .5.7); nhm's operation and receipts go under
// "_nhm"; a dry run's agent is the agent's name; the texts name nhm; nhm's
// flags beside ntm's.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/templates"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// SendResult is ntm's SendResult, the JSON of `nhm send --json`: one
// line.
type SendResult struct {
	Success              bool     `json:"success"`
	Session              string   `json:"session"`
	PromptPreview        string   `json:"prompt_preview,omitempty"`
	NonInteractiveForced bool     `json:"non_interactive_forced,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
	Blocked              bool     `json:"blocked,omitempty"`
	ErrorCode            string   `json:"error_code,omitempty"`
	Randomized           bool     `json:"randomized,omitempty"`
	SeedUsed             int64    `json:"seed_used,omitempty"`
	Targets              []string `json:"targets"`
	Delivered            int      `json:"delivered"`
	Failed               int      `json:"failed"`
	// CASSInjection is the CASS context block, as --robot-send's, when
	// the send asked for CASS context.
	CASSInjection *send.Injection `json:"cass_injection,omitempty"`
	Error         string          `json:"error,omitempty"`
	NHM           *SendExtras     `json:"_nhm,omitempty"`
}

// SendExtras are nhm's facts beside ntm's: the operation, whether its id
// was claimed before (then nothing was sent), each target's receipt, and
// send --wait's results.
type SendExtras struct {
	OperationID string         `json:"operation_id,omitempty"`
	Replayed    bool           `json:"replayed,omitempty"`
	InProgress  bool           `json:"in_progress,omitempty"`
	Receipts    []send.Receipt `json:"receipts"`
	Wait        []wait.Result  `json:"wait,omitempty"`
}

const (
	sendErrorCodeFailed          = "SEND_FAILED"
	sendErrorCodeNoMatchingPanes = "NO_MATCHING_PANES"
)

// SendDryRunEntry is ntm's: one prompt a dry run would send.
type SendDryRunEntry struct {
	Pane          string `json:"pane"`
	PaneID        string `json:"pane_id"`
	Agent         string `json:"agent,omitempty"`
	Prompt        string `json:"prompt"`
	PromptPreview string `json:"prompt_preview,omitempty"`
	Source        string `json:"source,omitempty"`
}

// SendDryRunResult is ntm's, the JSON of `nhm send --dry-run --json`.
type SendDryRunResult struct {
	Success   bool              `json:"success"`
	DryRun    bool              `json:"dry_run"`
	Session   string            `json:"session"`
	Warnings  []string          `json:"warnings,omitempty"`
	Blocked   bool              `json:"blocked,omitempty"`
	ErrorCode string            `json:"error_code,omitempty"`
	Total     int               `json:"total"`
	WouldSend []SendDryRunEntry `json:"would_send"`
	Message   string            `json:"message,omitempty"`
	Error     string            `json:"error,omitempty"`
	NHM       *SendExtras       `json:"_nhm,omitempty"`
}

// sendProjectSessionResult and sendProjectResult are ntm's, the JSON of
// `nhm send --project P --json`.
type sendProjectSessionResult struct {
	Session   string   `json:"session"`
	Success   bool     `json:"success"`
	Targets   []string `json:"targets"`
	Delivered int      `json:"delivered"`
	Failed    int      `json:"failed"`
	DryRun    bool     `json:"dry_run,omitempty"`
	WouldSend int      `json:"would_send,omitempty"`
	ErrorCode string   `json:"error_code,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type sendProjectResult struct {
	GeneratedAt       time.Time                  `json:"generated_at"`
	Success           bool                       `json:"success"`
	Project           string                     `json:"project"`
	Sessions          []sendProjectSessionResult `json:"sessions"`
	MatchedSessions   int                        `json:"matched_sessions"`
	SucceededSessions int                        `json:"succeeded_sessions"`
	FailedSessions    int                        `json:"failed_sessions"`
	Delivered         int                        `json:"delivered"`
	FailedDeliveries  int                        `json:"failed_deliveries"`
	ErrorCode         string                     `json:"error_code,omitempty"`
	Error             string                     `json:"error,omitempty"`
}

func buildSendProjectResult(project string, sessions []sendProjectSessionResult) sendProjectResult {
	if sessions == nil {
		sessions = []sendProjectSessionResult{}
	}
	result := sendProjectResult{GeneratedAt: time.Now().UTC(), Success: true, Project: project, Sessions: sessions,
		MatchedSessions: len(sessions)}
	for i := range result.Sessions {
		s := &result.Sessions[i]
		if s.Targets == nil {
			s.Targets = []string{}
		}
		result.Delivered += s.Delivered
		result.FailedDeliveries += s.Failed
		if s.Success {
			result.SucceededSessions++
			continue
		}
		result.Success = false
		result.FailedSessions++
	}
	if !result.Success {
		result.ErrorCode = sendErrorCodeFailed
		result.Error = fmt.Sprintf("%d of %d session sends failed", result.FailedSessions, result.MatchedSessions)
	}
	return result
}

// sendTarget is ntm's SendTarget: a kind, and a variant or "" for all.
type sendTarget struct{ kind, variant string }

// sendTargetValue is ntm's: --cc for every agent of the kind, --cc=opus
// for those whose model is opus, --cc=false for nothing.
type sendTargetValue struct {
	kind    string
	targets *[]sendTarget
}

func (v *sendTargetValue) String() string { return "" }
func (v *sendTargetValue) Type() string   { return "[variant]" }
func (v *sendTargetValue) IsBoolFlag() bool {
	return true
}

func (v *sendTargetValue) Set(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "true":
		value = ""
	case "false":
		return nil
	}
	*v.targets = append(*v.targets, sendTarget{kind: v.kind, variant: value})
	return nil
}

// ntmSendTypeFlags are ntm's type flags of send, with nhm's kind of each;
// gmi has none, so it selects no agent.
var ntmSendTypeFlags = []struct{ flag, kind, usage string }{
	{"cc", "claude", "send to Claude agents (optional :variant filter)"},
	{"cod", "codex", "send to Codex agents (optional :variant filter)"},
	{"gmi", "gemini", "send to Gemini agents (optional :variant filter)"},
	{"agy", "agy", "send to Antigravity (agy) agents (optional :variant filter)"},
	{"grok", "grok", "send to Grok Build agents (optional :variant filter)"},
	{"oc", "opencode", "send to OpenCode agents (optional :variant filter)"},
	{"omp", "omp", "send to Oh My Pi (omp) agents (optional :variant filter)"},
}

// sendNTMFlags are ntm's send flags and nhm's beside them.
type sendNTMFlags struct {
	targets                       []sendTarget
	all, includeUser, skipFirst   bool
	clearInput                    bool
	pane, panes, file             string
	prefix, suffix                string
	contexts, vars                []string
	tmpl, bead                    string
	tags                          []string
	dryRun                        bool
	cassCheck, noCassCheck        bool
	withCASS, noCASS              bool
	loopMode, forceNonInteractive bool
	cassSimilarity                float64
	cassCheckDays                 int
	noHooks                       bool
	smart                         bool
	route                         string
	distribute, distAuto          bool
	distStrategy                  string
	distLimit                     int
	randomize, priority, pace     bool
	seed                          int64
	basePrompt, basePromptFile    string
	batch, delay                  string
	confirmEach, stopOnErr, bcast bool
	agent                         int
	project                       string
	codexGoal                     bool
	names, kinds, states, pins    []string
	limit                         int
	includeSelf, force, wait      bool
	msg, msgFile, when, until     string
	timeout, poll                 time.Duration
	withMemory                    bool
	memoryQuery, opID, thread     string
	projectKey, socket            string
	cmd                           *cobra.Command
	kindFlags                     []string
	stdinIsPipe                   func() bool
}

// sendLater are ntm's send flags whose behaviour the send family's later
// beads bring: given, they refuse, never run as if absent.
var sendLater = []struct{ flag, bead string }{
	{"context", "nhm-2p6.5.5"}, {"base-prompt", "nhm-2p6.5.5"}, {"base-prompt-file", "nhm-2p6.5.5"},
	{"clear-input", "nhm-2p6.5.6"}, {"codex-goal", "nhm-2p6.5.6"}, {"force-non-interactive", "nhm-2p6.5.6"},
	{"cass-similarity", "nhm-2p6.5.6"}, {"cass-check-days", "nhm-2p6.5.6"}, {"loop-mode", "nhm-2p6.5.6"},
	{"smart", "nhm-2p6.5.7"}, {"route", "nhm-2p6.5.7"}, {"distribute", "nhm-2p6.5.7"}, {"dist-strategy", "nhm-2p6.5.7"},
	{"dist-limit", "nhm-2p6.5.7"}, {"dist-auto", "nhm-2p6.5.7"}, {"randomize", "nhm-2p6.5.7"}, {"seed", "nhm-2p6.5.7"},
	{"pace-dispatch", "nhm-2p6.5.7"}, {"priority-order", "nhm-2p6.5.7"}, {"batch", "nhm-2p6.5.7"}, {"delay", "nhm-2p6.5.7"},
	{"confirm-each", "nhm-2p6.5.7"}, {"stop-on-error", "nhm-2p6.5.7"}, {"broadcast", "nhm-2p6.5.7"}, {"agent", "nhm-2p6.5.7"},
}

func newSendCmd() *cobra.Command {
	cmd, _ := newSendCommand()
	return cmd
}

// sendStdinPiped reports a piped stdin; tests replace it.
var sendStdinPiped = stdinHasData

// newSendCommand is ntm's send command and its flags.
func newSendCommand() (*cobra.Command, *sendNTMFlags) {
	f := &sendNTMFlags{stdinIsPipe: func() bool { return sendStdinPiped() }}
	cmd := &cobra.Command{
		Use:   "send <session> [prompt]",
		Short: "Send a prompt to agent panes",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSendNTM(args, f, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f.cmd = cmd
	fs := cmd.Flags()
	for _, t := range ntmSendTypeFlags {
		fs.Var(&sendTargetValue{kind: t.kind, targets: &f.targets}, t.flag, t.usage)
		fs.Lookup(t.flag).NoOptDefVal = "true"
	}
	fs.BoolVar(&f.all, "all", false, "send to all agent panes, overriding type/tag filters (the user pane is excluded unless --include-user)")
	fs.BoolVar(&f.includeUser, "include-user", false, "opt the user/control pane into a --all broadcast (deliberate shell input only)")
	fs.BoolVar(&f.clearInput, "clear-input", false, "clear residual composer text (per-agent Escape ritual + C-u, verified) before typing each prompt; recommended after interrupts on codex panes")
	fs.BoolVarP(&f.skipFirst, "skip-first", "s", false, "skip the first pane in deterministic topology order")
	fs.StringVarP(&f.pane, "pane", "p", "", "send to one pane (N, W.P, or %N)")
	fs.StringVar(&f.panes, "panes", "", "send to panes (comma-separated N, W.P, or %N selectors)")
	fs.StringVarP(&f.file, "file", "f", "", "read prompt from file (also used as {{file}} in templates)")
	fs.StringVar(&f.prefix, "prefix", "", "text to prepend to file/stdin content")
	fs.StringVar(&f.suffix, "suffix", "", "text to append to file/stdin content")
	fs.StringArrayVarP(&f.contexts, "context", "c", nil, "file to include as context (repeatable, supports path:start-end)")
	fs.StringVarP(&f.tmpl, "template", "t", "", "use a named prompt template (see 'nhm template list')")
	fs.StringArrayVar(&f.vars, "var", nil, "template variable in key=value format (repeatable)")
	fs.StringVar(&f.bead, "bead", "", "fill the template's bead_* variables from this bead via 'br show' (requires --template)")
	fs.StringSliceVar(&f.tags, "tag", nil, "filter by tag (OR logic)")
	fs.BoolVar(&f.smart, "smart", false, "Send to the single best agent (smart routing) instead of every matching pane")
	fs.StringVar(&f.route, "route", "", "Smart-routing strategy (implies --smart): least-loaded, first-available, round-robin, round-robin-available, random, sticky, explicit, affinity")
	fs.BoolVar(&f.distribute, "distribute", false, "Auto-distribute prioritized work from bv triage to idle agents")
	fs.StringVar(&f.distStrategy, "dist-strategy", "simple", "Distribution strategy: simple (sequential pairing), balanced, speed, quality, dependency (graph-aware planner)")
	fs.IntVar(&f.distLimit, "dist-limit", 0, "Max tasks to distribute (0 = one per idle agent)")
	fs.BoolVar(&f.distAuto, "dist-auto", false, "Execute distribution without confirmation")
	fs.BoolVar(&f.cassCheck, "cass-check", true, "Check for duplicate work in CASS")
	fs.BoolVar(&f.noCassCheck, "no-cass-check", false, "Skip CASS duplicate check")
	fs.BoolVar(&f.withCASS, "with-cass", false, "Inject relevant CASS session context above the prompt before sending; degrades gracefully when cass is unavailable")
	fs.BoolVar(&f.noCASS, "no-cass", false, "Disable CASS context injection for this send")
	fs.BoolVar(&f.loopMode, "loop-mode", false, "Allow repeated orchestration nudges without a CASS duplicate prompt")
	fs.BoolVar(&f.forceNonInteractive, "force-non-interactive", false, "Bypass safe confirmation gates (currently the CASS duplicate prompt) for recovery/status automation")
	fs.Float64Var(&f.cassSimilarity, "cass-similarity", 0.7, "Similarity threshold for duplicate detection")
	fs.IntVar(&f.cassCheckDays, "cass-check-days", 7, "Look back N days for duplicates")
	// ntm's command hooks ([hooks] pre-send, post-send): nhm runs none.
	fs.BoolVar(&f.noHooks, "no-hooks", false, "Disable command hooks")
	fs.BoolVar(&f.dryRun, "dry-run", false, "Preview what would be sent without sending")
	fs.BoolVar(&f.randomize, "randomize", false, "Randomize send order for individualized prompts (reduces thundering herd)")
	fs.Int64Var(&f.seed, "seed", 0, "Deterministic seed for --randomize (0 = time-based)")
	fs.BoolVar(&f.pace, "pace-dispatch", false, "Include advisory dispatch pacing in JSON and dry-run output without changing send behavior")
	fs.BoolVar(&f.priority, "priority-order", false, "Sort batch prompts by priority (P0 first, annotate with '# priority: N')")
	fs.StringVar(&f.basePrompt, "base-prompt", "", "Text to prepend to all prompts")
	fs.StringVar(&f.basePromptFile, "base-prompt-file", "", "File whose contents are prepended to all prompts")
	fs.StringVar(&f.batch, "batch", "", "Read prompts from file (one per line or --- separated)")
	fs.StringVar(&f.delay, "delay", "", "Delay between prompts (e.g., 5s, 100ms)")
	fs.BoolVar(&f.confirmEach, "confirm-each", false, "Confirm each prompt before sending")
	fs.BoolVar(&f.stopOnErr, "stop-on-error", false, "Stop batch on first send failure")
	fs.BoolVar(&f.bcast, "broadcast", false, "Send same prompt to all agents simultaneously")
	fs.IntVar(&f.agent, "agent", -1, "Send to specific agent index only (-1 = round-robin)")
	fs.StringVar(&f.project, "project", "", "broadcast to all sessions for a base project name")
	fs.BoolVar(&f.codexGoal, "codex-goal", false, "Drive the Codex /goal slash-command flow on --pane")
	addNHMSendFlags(fs, f)
	return cmd, f
}

// addNHMSendFlags adds nhm's own send flags, and a selector flag for each
// kind name and kind flag of the registry that ntm's flags leave free
// (--claude, --codex, --pi …).
func addNHMSendFlags(fs *pflag.FlagSet, f *sendNTMFlags) {
	fs.StringSliceVar(&f.names, "name", nil, "an Agent Mail or herdr name (repeatable)")
	fs.StringSliceVar(&f.kinds, "type", nil, "an agent kind (repeatable)")
	fs.StringSliceVar(&f.states, "state", nil, stateValues+" (repeatable)")
	fs.IntVar(&f.limit, "limit", 0, "the first N targets in slot order")
	fs.BoolVar(&f.includeSelf, "include-self", false, "also the calling pane")
	fs.StringVar(&f.msg, "msg", "", "the prompt")
	fs.StringVar(&f.msgFile, "msg-file", "", "read the prompt from a file (- is stdin)")
	fs.StringVar(&f.when, "when", "any", "any (type into a busy agent, as ntm) or ready (refuse it)")
	fs.BoolVar(&f.force, "force", false, "admit busy and unknown agents even with --when ready, and skip the duplicate check")
	fs.BoolVar(&f.wait, "wait", false, "wait for each target after the send")
	fs.StringVar(&f.until, "until", string(wait.UntilComplete), "with --wait: done, idle or complete")
	fs.DurationVar(&f.timeout, "timeout", defaultWaitTimeout, "with --wait: the longest wait")
	fs.DurationVar(&f.poll, "poll", wait.DefaultPoll, "with --wait: the longest sleep between reads")
	fs.BoolVar(&f.withMemory, "with-memory", false, "put CM's rules before the prompt (best-effort)")
	fs.StringVar(&f.memoryQuery, "memory-query", "", "the CM and CASS query (default: the prompt's start)")
	fs.StringVar(&f.thread, "thread", "", "an Agent Mail thread whose mail goes with the prompt; wakes the targets after the send")
	fs.StringSliceVar(&f.pins, "if-generation", nil, "send to binding ID only at generation G: ID:G (repeatable)")
	fs.StringVar(&f.opID, "op-id", "", "operation id, for an idempotent retry")
	fs.StringVar(&f.projectKey, "project-key", "", "the project (templates, --bead, a name's project); limits the targets to it")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	for _, a := range kinds.All() {
		for _, name := range append([]string{a.Name}, a.Flags...) {
			if fs.Lookup(name) != nil {
				continue
			}
			fs.Var(&sendTargetValue{kind: a.Name, targets: &f.targets}, name, a.Name+" agents (optional :variant filter)")
			fs.Lookup(name).NoOptDefVal = "true"
			f.kindFlags = append(f.kindFlags, name)
		}
	}
}

// sendLaterFlag refuses the first given flag of a later bead.
func (f *sendNTMFlags) sendLaterFlag() error {
	for _, l := range sendLater {
		if f.cmd.Flags().Changed(l.flag) {
			return notBuiltError{flag: l.flag, bead: l.bead}
		}
	}
	return nil
}

// ntmSendChecks are ntm's checks of the flags, in its order, before
// anything is resolved.
func (f *sendNTMFlags) ntmSendChecks() ([]string, error) {
	f.pane = strings.TrimSpace(f.pane)
	panesGiven := strings.TrimSpace(f.panes) != ""
	selectors, err := parseSendPaneSelectors(f.panes)
	if err != nil {
		return nil, err
	}
	if f.pane != "" {
		if err := validateSendPaneSelector(f.pane); err != nil {
			return nil, err
		}
	}
	if f.pane != "" && panesGiven {
		return nil, errors.New("cannot use --pane and --panes together")
	}
	if f.skipFirst && (f.pane != "" || panesGiven) {
		return nil, errors.New("cannot combine --skip-first with explicit --pane/--panes selectors")
	}
	// ntm's validateSendTemplateFlags.
	f.bead = strings.TrimSpace(f.bead)
	if f.cmd.Flags().Changed("bead") && f.bead == "" {
		return nil, sendInvalidInput{errors.New("--bead requires a bead ID")}
	}
	if f.bead != "" && f.tmpl == "" {
		return nil, sendInvalidInput{errors.New("--bead only fills template variables; use it with --template")}
	}
	if f.tmpl != "" {
		for _, m := range []struct {
			set  bool
			flag string
		}{{f.project != "", "--project"}, {f.codexGoal, "--codex-goal"}, {f.distribute, "--distribute"}, {f.batch != "", "--batch"}} {
			if m.set {
				return nil, sendInvalidInput{fmt.Errorf("cannot combine --template with %s", m.flag)}
			}
		}
	}
	return selectors, nil
}

// parseSendPaneSelectors is ntm's parseShellPaneSelectors.
func parseSendPaneSelectors(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			return nil, fmt.Errorf("invalid empty pane selector in %q", raw)
		}
		if err := validateSendPaneSelector(s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// validateSendPaneSelector is ntm's selector grammar, N, W.P or %N, and a
// herdr pane id, nhm's own form.
func validateSendPaneSelector(s string) error {
	if paneIndexText.MatchString(s) || paneSlotText.MatchString(s) || tmuxPaneText.MatchString(s) || herdrPaneText.MatchString(s) {
		return nil
	}
	return fmt.Errorf("invalid pane selector %q: expected N, W.P, or %%N", s)
}

// getPromptContent is ntm's: --file (- is stdin), else stdin when it is a
// pipe and no prompt argument is given, else the arguments. --prefix and
// --suffix wrap file and stdin content. It returns the prompt and its
// source.
func getPromptContent(args []string, promptFile, prefix, suffix string, piped bool) (string, string, error) {
	if promptFile != "" {
		data, err := readPromptFileOrStdin(promptFile)
		if err != nil {
			return "", "", fmt.Errorf("reading prompt file: %w", err)
		}
		content := string(data)
		if strings.TrimSpace(content) == "" {
			return "", "", errors.New("prompt file is empty")
		}
		return buildPrompt(content, prefix, suffix), "file:" + promptFile, nil
	}
	if len(args) == 0 && piped {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", "", fmt.Errorf("reading from stdin: %w", err)
		}
		content := string(data)
		if strings.TrimSpace(content) == "" && prefix == "" {
			return "", "", errors.New("stdin is empty and no prefix provided")
		}
		return buildPrompt(content, prefix, suffix), "stdin", nil
	}
	if len(args) == 0 {
		return "", "", errors.New("no prompt provided (use argument, --file, or pipe to stdin)")
	}
	return strings.Join(args, " "), "args", nil
}

// readPromptFileOrStdin is ntm's: "-" reads stdin.
func readPromptFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		// Read one byte past the limit so oversize input is a loud error
		// instead of a silent mid-prompt truncation.
		const limit = 10 * 1024 * 1024
		data, err := io.ReadAll(io.LimitReader(stdin, limit+1))
		if err != nil {
			return nil, err
		}
		if len(data) > limit {
			return nil, errors.New("stdin prompt too large (max 10MB)")
		}
		return data, nil
	}
	return os.ReadFile(path)
}

// stdinHasData is ntm's: stdin is a pipe or a redirect, not a terminal.
func stdinHasData() bool {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice == 0
}

// buildPrompt is ntm's: the prefix, the trimmed content and the suffix,
// a line each.
func buildPrompt(content, prefix, suffix string) string {
	var parts []string
	if prefix != "" {
		parts = append(parts, prefix)
	}
	parts = append(parts, strings.TrimSpace(content))
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, "\n")
}

// truncatePrompt is ntm's: at most maxLen bytes, cut on a rune boundary
// with "...".
func truncatePrompt(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	cut := 0
	for i := range s {
		if i > maxLen-3 {
			break
		}
		cut = i
	}
	return s[:cut] + "..."
}

// truncateForPreview is ntm's: trimmed, newlines as spaces, at most
// maxLen bytes.
func truncateForPreview(s string, maxLen int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// sendFailure is ntm's outputSendCommandError: under --json a failed
// SendResult on one line, exit 1; else the error for the root.
func sendFailure(w io.Writer, asJSON bool, session string, err error) error {
	if !asJSON {
		return err
	}
	code := sendErrorCode(err)
	if code == "" {
		code = robot.ErrCodeInternalError
	}
	return writeSendFailure(w, SendResult{Session: session, Targets: []string{}, ErrorCode: code, Error: err.Error()}, err)
}

// sendInvalidInput is ntm's markCLIInvalidInput: a bad --bead, --template
// or --var, or a template that no source has. Its error code is
// INVALID_FLAG.
type sendInvalidInput struct{ error }

func (e sendInvalidInput) Unwrap() error { return e.error }

// sendErrorCode is the error code that ntm's send gives err: TIMEOUT for a
// cancelled or timed-out send, INVALID_FLAG for invalid input, else none.
// An early failure (outputSendCommandError) has INTERNAL_ERROR then.
func sendErrorCode(err error) string {
	var invalid sendInvalidInput
	var noTemplate *templates.TemplateNotFoundError
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return robot.ErrCodeTimeout
	case errors.As(err, &invalid) || errors.As(err, &noTemplate):
		return robot.ErrCodeInvalidFlag
	}
	return ""
}

// writeSendFailure writes a failed result on one line and exits 1.
func writeSendFailure(w io.Writer, v any, cause error) error {
	if encErr := json.NewEncoder(w).Encode(v); encErr != nil {
		return errors.Join(encErr, cause)
	}
	return verbExit{code: exitError}
}

// printSendDryRunResult is ntm's text of a dry run.
func printSendDryRunResult(w io.Writer, r SendDryRunResult) {
	fmt.Fprintf(w, "Dry Run: nhm send %s\n\n", r.Session)
	fmt.Fprintf(w, "Would send %d prompt(s):\n", r.Total)
	for i, e := range r.WouldSend {
		source := e.Source
		if source == "" {
			source = "unknown"
		}
		fmt.Fprintf(w, "  %d. %s (pane %s): %q (%s)\n", i+1, e.Agent, e.Pane, e.PromptPreview, source)
	}
	fmt.Fprintln(w)
	if r.Message != "" {
		fmt.Fprintln(w, r.Message)
	}
}

// sendSuggestions is ntm's SendSuggestions, for the footer after a send.
func sendSuggestions(session string) []suggestion {
	return []suggestion{
		{Command: fmt.Sprintf("nhm dashboard %s", session), Description: "Monitor progress"},
		{Command: fmt.Sprintf("nhm attach %s", session), Description: "Connect to session"},
	}
}

// The pane forms of --pane and --panes: ntm's N, W.P and %N, and a herdr
// pane id.
var (
	paneIndexText = regexp.MustCompile(`^[0-9]+$`)
	paneSlotText  = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	tmuxPaneText  = regexp.MustCompile(`^%[0-9]+$`)
	herdrPaneText = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*:p[0-9A-Za-z]+$`)
)
