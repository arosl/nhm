// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/send.go (the order of newSendCmd's RunE checks and its
// --project mode, runSendProject, runSendInternal's text lines and its
// "sending to pane" error, finishSendResult); see LICENSE in this
// directory and PROVENANCE.md. Local changes: the send is nhm's core send
// (receipts, the delivery gate, the shell-escape and draft checks), so a
// refused target does not stop the others; the panes are nhm's bound
// agents, selected by nhm's core selector with ntm's filters; a dry run
// names a refused target as a warning; nhm's operation, receipts and
// --wait results go under "_nhm"; send --wait and an operation without a
// final outcome keep nhm's exit codes.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/sessionlabel"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/store/history"
)

// runSendNTM is ntm's send (send_ntm.go) on nhm's core send: the flag
// checks, the prompt, one session or --project's sessions, and ntm's
// output. An error is ntm's: the root's "Error: …", or under --json a
// failed SendResult on one line; either way exit 1.
func runSendNTM(args []string, f *sendNTMFlags, asJSON bool, stdout, stderr io.Writer) error {
	name := ""
	if f.project == "" && len(args) > 0 {
		name = args[0]
	}
	fail := func(err error) error { return sendFailure(stdout, asJSON, name, err) }
	selectors, err := f.ntmSendChecks()
	if err != nil {
		return fail(err)
	}
	if err := f.sendLaterFlag(); err != nil {
		return err
	}
	if f.project != "" {
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") && !strings.Contains(args[0], " ") {
			return fail(errors.New("cannot use --project with a specific session name; use just --project or just a session name"))
		}
		// nhm's: each session is an operation of its own, and a pin
		// names one session's binding.
		if f.opID != "" || len(f.pins) > 0 {
			return fail(errors.New("cannot combine --project with --op-id or --if-generation: each session is its own operation"))
		}
		return runSendProject(args, f, selectors, asJSON, stdout, stderr)
	}
	if len(args) == 0 {
		return fail(errors.New("session name required (or use --project)"))
	}
	prompt, source, err := f.promptText(args[1:])
	if err != nil {
		return fail(err)
	}
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return fail(err)
	}
	defer ie.db.Close()
	r, err := resolveSession(ctx, ie, name, asJSON, stdout, stderr)
	if err != nil {
		return fail(err)
	}
	if r.WorkspaceID == "" {
		return nil // a cancelled picker
	}
	run := ie.sendToSession(ctx, f, r.Session, r.WorkspaceID, prompt, source, selectors)
	return run.report(stdout, stderr, asJSON, f)
}

// promptText is the prompt and its source: nhm's --msg or --msg-file, or
// ntm's prompt (--file, a piped stdin, or the arguments). A template
// needs none: --file is its {{file}}.
func (f *sendNTMFlags) promptText(args []string) (string, string, error) {
	given := 0
	for _, s := range []string{strings.Join(args, " "), f.msg, f.msgFile} {
		if s != "" {
			given++
		}
	}
	switch {
	case given > 1:
		return "", "", errors.New("give the prompt once: as an argument, --msg or --msg-file")
	case f.msg != "":
		return f.msg, "msg", nil
	case f.msgFile != "":
		text, err := readInput(f.msgFile)
		if err != nil {
			return "", "", fmt.Errorf("--msg-file: %w", err)
		}
		return text, "file:" + f.msgFile, nil
	case f.tmpl != "":
		// As ntm: the template is the prompt; positional text is ignored.
		return "", "template:" + f.tmpl, nil
	}
	return getPromptContent(args, f.file, f.prefix, f.suffix, f.stdinIsPipe())
}

// coreRequest is the core request of the flags, without the scope and the
// pane filters (sendFilters).
func (f *sendNTMFlags) coreRequest(prompt string) (send.Request, error) {
	req := send.Request{Template: f.tmpl, BeadID: f.bead, Force: f.force, OperationID: f.opID, DryRun: f.dryRun,
		Source: history.SourceCLI, Message: prompt}
	switch f.when {
	case "ready":
	case "any":
		req.WhenAny = true
	default:
		return req, fmt.Errorf("--when is ready or any, not %q", f.when)
	}
	if f.tmpl != "" && f.file != "" {
		text, err := readInput(f.file)
		if err != nil {
			return req, fmt.Errorf("--file: %w", err)
		}
		req.FileContent = text
	}
	if len(f.vars) > 0 {
		req.Vars = map[string]string{}
		for _, v := range f.vars {
			k, val, ok := strings.Cut(v, "=")
			if !ok || k == "" {
				return req, sendInvalidInput{fmt.Errorf("invalid --var format '%s' (expected key=value)", v)}
			}
			req.Vars[k] = val
		}
	}
	if f.limit < 0 {
		return req, errors.New("--limit is positive")
	}
	sel := send.Selector{All: f.all, Names: f.names, Limit: f.limit, IncludeSelf: f.includeSelf || (f.all && f.includeUser)}
	for _, k := range f.kinds {
		ks, err := ntmTypeKinds(k)
		if err != nil {
			return req, err
		}
		sel.Kinds = append(sel.Kinds, ks...)
	}
	// --all overrides ntm's type and tag filters.
	if !f.all {
		for _, t := range f.targets {
			if sel.Variants == nil {
				sel.Variants = map[string][]string{}
			}
			sel.Variants[t.kind] = append(sel.Variants[t.kind], t.variant)
		}
		sel.Tags = f.tags
	}
	states, err := parseStates(f.states)
	if err != nil {
		return req, err
	}
	sel.States = states
	expect, err := parsePins(f.pins)
	if err != nil {
		return req, err
	}
	req.Selector, req.Expect = pinSelection(sel, expect), expect
	cfg, err := loadEnrichConfig()
	if err != nil {
		return req, err
	}
	req.Enrich = f.enrichment(cfg)
	return req, nil
}

// enrichment is the send's CM and CASS part: --with-memory, --with-cass
// and --no-cass (which wins), and ntm's --cass-check and --no-cass-check
// over nhm's config.
func (f *sendNTMFlags) enrichment(cfg enrich.Config) send.Enrichment {
	e := send.Enrichment{DefaultCM: cfg.Memory.CM, DefaultCASS: cfg.CASS.Context, Query: f.memoryQuery,
		CheckDays: cfg.CASS.CheckDays, Check: cfg.CheckOn()}
	if f.cmd.Flags().Changed("with-memory") {
		e.CM = enrich.Off
		if f.withMemory {
			e.CM = enrich.BestEffort
		}
	}
	switch {
	case f.noCASS:
		e.CASS = enrich.Off
	case f.withCASS:
		e.CASS = enrich.BestEffort
	}
	if f.cmd.Flags().Changed("cass-check") || f.cmd.Flags().Changed("no-cass-check") {
		e.Check = f.cassCheck && !f.noCassCheck
	}
	return e
}

// sendFilters fills the selector's herdr part: --pane and --panes as
// slots or pane ids, the panes whose label ends in a --tag, and with
// --skip-first the workspace's first pane. It reads herdr only when one
// of them is given.
func (ie *instanceEnv) sendFilters(ctx context.Context, ws string, f *sendNTMFlags, selectors []string, sel *send.Selector) error {
	if f.pane != "" {
		selectors = []string{f.pane}
	}
	if len(selectors) == 0 && len(sel.Tags) == 0 && !f.skipFirst {
		return nil
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return err
	}
	all, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return err
	}
	active := ""
	tabNumber := map[string]uint{}
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == ws {
			active = w.ActiveTabID
		}
	}
	for _, t := range snap.Tabs {
		tabNumber[t.TabID] = t.Number
	}
	var slots []string
	for _, b := range all {
		if b.Slot != "" {
			slots = append(slots, b.Slot)
		}
	}
	for _, s := range selectors {
		t, err := session.ParsePane(s, session.AgentTabs(all), active)
		if err != nil {
			return err
		}
		found := slices.ContainsFunc(all, func(b bindings.Binding) bool {
			return (t.Form == session.PaneSlot && b.Slot == t.Slot) || (t.Form == session.PaneHerdrID && b.PaneID == t.PaneID)
		})
		if !found {
			return fmt.Errorf("pane selector %q not found; available: %s", s, strings.Join(slots, ", "))
		}
		if t.Form == session.PaneSlot {
			sel.Panes = append(sel.Panes, t.Slot)
		} else {
			sel.Panes = append(sel.Panes, t.PaneID)
		}
	}
	var panes []string
	for _, p := range snap.Panes {
		if p.WorkspaceID != ws {
			continue
		}
		panes = append(panes, p.PaneID)
		if len(sel.Tags) > 0 && session.HasAnyTag(session.PaneTags(p.Label), sel.Tags) {
			sel.TaggedPanes = append(sel.TaggedPanes, p.PaneID)
		}
	}
	if f.skipFirst && len(panes) > 0 {
		// ntm's topology order: the tab's number, then the pane's place.
		tabOf := map[string]string{}
		for _, p := range snap.Panes {
			tabOf[p.PaneID] = p.TabID
		}
		sort.SliceStable(panes, func(i, j int) bool { return tabNumber[tabOf[panes[i]]] < tabNumber[tabOf[panes[j]]] })
		sel.SkipFirst, sel.SkipPanes = true, []string{panes[0]}
	}
	return nil
}

// sendRun is one session's send and what ntm reports of it.
type sendRun struct {
	name, source, prompt string
	res                  send.Result
	err                  error
	keys                 []string
	delivered, failed    int
	failPane, failReason string
	explicit, broad      bool
	waited               []wait.Result
	waitErr              error
	warnings             []string
}

// sendToSession sends to one session through the core.
func (ie *instanceEnv) sendToSession(ctx context.Context, f *sendNTMFlags, name, ws, prompt, source string, selectors []string) sendRun {
	run := sendRun{name: name, source: source, prompt: prompt, explicit: f.pane != "",
		broad: f.pane == "" && len(selectors) == 0 && len(f.names) == 0 && len(f.pins) == 0}
	req, err := f.coreRequest(prompt)
	if err == nil {
		err = ie.sendFilters(ctx, ws, f, selectors, &req.Selector)
	}
	if err != nil {
		run.err = err
		return run
	}
	req.Caller = ie.callerPane(ctx)
	if err := ie.fillScopeIn(ctx, &req, name, ws, f.projectKey); err != nil {
		run.err = err
		return run
	}
	until, err := wait.ParseUntil(f.until)
	if f.wait && (err != nil || until == wait.UntilGenerating || until == wait.UntilHealthy || f.timeout <= 0 || f.poll <= 0) {
		run.err = errors.New("--until is done, idle or complete; --timeout and --poll are positive")
		return run
	}
	run.res, _, run.err = runSendCore(ctx, ie, req)
	if run.err != nil {
		return run
	}
	run.warnings = run.res.Warnings
	all, _ := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}})
	multiTab := len(session.AgentTabs(all)) > 1
	for _, rc := range run.res.Targets {
		key, _ := interruptPane(slotOf(rc.Key), rc.PaneID, multiTab)
		if key == "" {
			// A pin that the selection did not reach has no pane: its
			// key is the binding.
			key = rc.Key
		}
		run.keys = append(run.keys, key)
		switch rc.Status {
		case send.StatusSubmitted, send.StatusPossiblySubmitted, "would_send":
			run.delivered++
		default:
			run.failed++
			if run.failPane == "" {
				run.failPane, run.failReason = key, rc.Result.Reason
				if run.failReason == "" {
					run.failReason = strings.ReplaceAll(rc.Status, "_", " ")
					if rc.Result.Gate != "" {
						run.failReason += " (" + string(rc.Result.Gate) + ")"
					}
				}
			}
		}
	}
	if f.wait && !run.res.DryRun && run.res.OperationID != "" && run.failed == 0 {
		// observe reads the operation again; a replay stays a replay.
		replayed := run.res.Replayed
		run.waited, run.res, run.waitErr = observe(ctx, ie, run.res.OperationID, until, f.timeout, f.poll)
		run.res.Replayed = run.res.Replayed || replayed
	}
	if f.thread != "" && !run.res.DryRun && run.res.OperationID != "" {
		run.warnings = append(run.warnings, ie.wakeTargets(ctx, run.res)...)
	}
	return run
}

// slotOf is a receipt's key as a slot, or "" for a pane id.
func slotOf(key string) string {
	if _, err := bindings.ParseSlot(key); err == nil {
		return key
	}
	return ""
}

// fillScopeIn is the scope of a resolved session: the workspace, and the
// project of --project-key, else the cwd's work tree; only --project-key
// limits the targets to a project.
func (ie *instanceEnv) fillScopeIn(ctx context.Context, req *send.Request, name, ws, project string) error {
	req.Session, req.WorkspaceID = name, ws
	scope := map[string]string{"instance_id": ie.inst.ID, "workspace_id": ws}
	if s, err := projectScope(ctx, ie.inst.ID, project); err == nil {
		req.ProjectRoot = s.ProjectRoot
		scope["project_root"] = s.ProjectRoot
		if project != "" {
			req.ProjectKey = s.MailProjectKey
			scope["mail_project_key"] = s.MailProjectKey
		}
	} else if project != "" {
		return err
	}
	req.Scope = scope
	return nil
}

// extras are nhm's facts of the run, under "_nhm".
func (run sendRun) extras() *SendExtras {
	rc := run.res.Targets
	if rc == nil {
		rc = []send.Receipt{}
	}
	return &SendExtras{OperationID: run.res.OperationID, Replayed: run.res.Replayed, InProgress: run.res.InProgress,
		Receipts: rc, Wait: run.waited}
}

// result is ntm's SendResult of the run.
func (run sendRun) result() SendResult {
	preview := run.prompt
	if preview == "" {
		preview = run.res.Preview
	}
	r := SendResult{Session: run.name, PromptPreview: truncatePrompt(preview, 50), Warnings: run.warnings,
		Targets: run.keys, Delivered: run.delivered, Failed: run.failed, NHM: run.extras()}
	if r.Targets == nil {
		r.Targets = []string{}
	}
	if inj := run.res.CASSInjection; inj.Mode != "" && inj.Mode != enrich.Off {
		r.CASSInjection = &inj
	}
	switch {
	case run.noMatch():
		r.Targets, r.ErrorCode, r.Error = []string{}, sendErrorCodeNoMatchingPanes, "no matching panes found"
	case run.err != nil:
		r.ErrorCode, r.Error = sendErrorCode(run.err), run.err.Error()
	case run.failed > 0:
		r.ErrorCode, r.Error = sendErrorCodeFailed, run.failReason
	default:
		r.Success = true
	}
	return r
}

// noMatch reports ntm's "No matching panes found": a broad send that
// selects no agent. A name or a pane that matches none keeps nhm's error.
func (run sendRun) noMatch() bool {
	return run.broad && errors.Is(run.err, send.ErrNoTargets)
}

// dryRunResult is ntm's SendDryRunResult of a dry run.
func (run sendRun) dryRunResult() SendDryRunResult {
	r := SendDryRunResult{Success: true, DryRun: true, Session: run.name, Warnings: append([]string(nil), run.warnings...),
		WouldSend: []SendDryRunEntry{}, Message: "use without --dry-run to execute", NHM: run.extras()}
	for i, rc := range run.res.Targets {
		if rc.Status == send.StatusRefused {
			// A target that the send would refuse is no prompt to send;
			// the dry run names it and why (nhm-mowp).
			r.Warnings = append(r.Warnings, fmt.Sprintf("pane %s (%s): refused: %s", run.keys[i], rc.MailName, rc.Result.Reason))
			continue
		}
		prompt := run.prompt
		if i < len(run.res.Rendered) {
			prompt = run.res.Rendered[i]
		}
		r.WouldSend = append(r.WouldSend, SendDryRunEntry{Pane: run.keys[i], PaneID: rc.PaneID, Agent: rc.MailName,
			Prompt: prompt, PromptPreview: truncateForPreview(prompt, 80), Source: run.source})
	}
	r.Total = len(r.WouldSend)
	return r
}

// report writes ntm's output of one session's send and returns its error.
func (run sendRun) report(stdout, stderr io.Writer, asJSON bool, f *sendNTMFlags) error {
	warn := func(ws []string) {
		if !asJSON {
			for _, w := range ws {
				fmt.Fprintln(stderr, "Warning:", w)
			}
		}
	}
	if run.res.DryRun && run.err == nil {
		r := run.dryRunResult()
		if asJSON {
			return writeSendJSON(stdout, r)
		}
		warn(r.Warnings)
		printSendDryRunResult(stdout, r)
		return nil
	}
	warn(run.warnings)
	r := run.result()
	if asJSON {
		if run.err == nil && run.res.InProgress {
			// nhm's: the id's operation has no final outcome; exit 4.
			r.Success, r.ErrorCode = false, ""
			r.Error = fmt.Sprintf("operation %s has no final outcome; never resend with this id", run.res.OperationID)
			if err := writeSendJSON(stdout, r); err != nil {
				return err
			}
			return verbExit{code: exitAmbiguous}
		}
		if !r.Success {
			cause := run.err
			if cause == nil {
				cause = errors.New(r.Error)
			}
			return writeSendFailure(stdout, r, cause)
		}
		if err := writeSendJSON(stdout, r); err != nil {
			return err
		}
		return run.waitExit()
	}
	switch {
	case run.noMatch():
		fmt.Fprintln(stdout, "No matching panes found")
		return errors.New("no matching panes found")
	case run.err != nil:
		return run.err
	case run.res.InProgress:
		fmt.Fprintf(stdout, "operation %s has no final outcome; never resend with this id\n", run.res.OperationID)
		return verbExit{code: exitAmbiguous}
	case run.res.Replayed:
		// The stored operation; --wait still reports its turn.
		fmt.Fprintf(stdout, "operation %s was claimed before; nothing sent\n", run.res.OperationID)
	case run.failed > 0:
		return fmt.Errorf("sending to pane %s: %s", run.failPane, run.failReason)
	case run.explicit:
		fmt.Fprintf(stdout, "Sent to pane %s\n", run.keys[0])
	default:
		fmt.Fprintf(stdout, "Sent to %d pane(s)\n", run.delivered)
	}
	for _, w := range run.waited {
		line := fmt.Sprintf("%s (%s): %s", w.MailName, dash(w.Slot), w.Outcome)
		if w.Correlation != "" {
			line += " (" + w.Correlation + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	if err := run.waitExit(); err != nil {
		return err
	}
	if !run.explicit && !run.res.Replayed {
		// ntm's --pane send ends at its line, with no footer.
		printSuccessFooter(stdout, sendSuggestions(run.name)...)
	}
	return nil
}

// waitExit is send --wait's outcome, nhm's own: 1 when a target did not
// complete, 4 when its completion is ambiguous.
func (run sendRun) waitExit() error {
	if run.waitErr != nil {
		return run.waitErr
	}
	if run.waited == nil {
		return nil
	}
	s := wait.Summarize(run.waited, 0)
	switch {
	case s.Ambiguous:
		return verbExit{code: exitAmbiguous}
	case !s.Met:
		return verbExit{code: exitError}
	}
	return nil
}

// writeSendJSON is ntm's send JSON: one line.
func writeSendJSON(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// runSendProject is ntm's runSendProject: the prompt (the arguments) to
// every session whose base name is the project, in name order.
func runSendProject(args []string, f *sendNTMFlags, selectors []string, asJSON bool, stdout, stderr io.Writer) error {
	fail := func(err error) error {
		if !asJSON {
			return err
		}
		r := buildSendProjectResult(f.project, nil)
		r.Success, r.ErrorCode, r.Error = false, sendErrorCodeFailed, err.Error()
		return writeSendFailure(stdout, r, err)
	}
	ctx := context.Background()
	ie, err := openInstance(f.socket)
	if err != nil {
		return fail(err)
	}
	defer ie.db.Close()
	ss, _, err := herdrSessions(ctx, ie, false)
	if err != nil {
		return fail(err)
	}
	var matching []session.Session
	for _, s := range ss {
		if sessionlabel.SessionBase(s.Name) == f.project {
			matching = append(matching, s)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Name < matching[j].Name })
	if len(matching) == 0 {
		return fail(fmt.Errorf("no sessions found for project %q", f.project))
	}
	prompt := strings.Join(args, " ")
	if strings.TrimSpace(prompt) == "" {
		return fail(errors.New("prompt text required"))
	}
	if asJSON {
		var results []sendProjectSessionResult
		var firstErr error
		for _, s := range matching {
			run := ie.sendToSession(ctx, f, s.Name, s.WorkspaceID, prompt, "args", selectors)
			sr := sendProjectSessionResult{Session: s.Name, Targets: run.keys}
			if run.res.DryRun && run.err == nil {
				// As ntm: the prompts it would send; a refused target is
				// none.
				d := run.dryRunResult()
				sr.Targets = []string{}
				for _, e := range d.WouldSend {
					sr.Targets = append(sr.Targets, e.Pane)
				}
				sr.Success, sr.DryRun, sr.WouldSend = true, true, d.Total
			} else {
				r := run.result()
				sr.Success, sr.Delivered, sr.Failed, sr.ErrorCode, sr.Error = r.Success, r.Delivered, r.Failed, r.ErrorCode, r.Error
				if !r.Success && sr.ErrorCode == "" {
					sr.ErrorCode = sendErrorCodeFailed
				}
				if !r.Success && firstErr == nil {
					firstErr = errors.New(r.Error)
				}
			}
			results = append(results, sr)
		}
		r := buildSendProjectResult(f.project, results)
		if r.Success {
			return writeNTMJSON(stdout, r)
		}
		if firstErr == nil {
			firstErr = errors.New(r.Error)
		}
		return writeSendFailure(stdout, r, firstErr)
	}
	names := make([]string, len(matching))
	for i, s := range matching {
		names[i] = s.Name
	}
	fmt.Fprintf(stdout, "Sending to %d session(s): %s\n", len(matching), strings.Join(names, ", "))
	var sendErrors []string
	delivered := 0
	for _, s := range matching {
		run := ie.sendToSession(ctx, f, s.Name, s.WorkspaceID, prompt, "args", selectors)
		if err := run.report(stdout, stderr, false, f); err != nil {
			sendErrors = append(sendErrors, fmt.Sprintf("%s: %v", s.Name, err))
		} else {
			delivered++
		}
	}
	if len(sendErrors) > 0 {
		fmt.Fprintf(stdout, "Delivered to %d/%d sessions. Errors: %s\n", delivered, len(matching), strings.Join(sendErrors, "; "))
		return fmt.Errorf("project send failed for %d of %d sessions", len(sendErrors), len(matching))
	}
	return nil
}
