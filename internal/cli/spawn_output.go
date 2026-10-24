package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/robot"
)

// runSpawnNTM is nhm spawn and add without --assign, with ntm's output
// (nhm-2p6.4.8, nhm-2p6.4.9): under --json ntm's SpawnResponse or
// AddResponse, or on an error its agentLifecycleFailureResponse, all with
// nhm's own facts under "_nhm"; else ntm's text, nhm's agent lines on
// stderr and, on a terminal, ntm's "What's next?". A dry run and a replay
// (nhm's --dry-run and --op-id) print nhm's text. Every error exits 1.
func runSpawnNTM(ctx context.Context, verb string, req spawn.Request, project, socket string, asJSON bool,
	stdout, stderr io.Writer) int {
	write := writeSpawnNTM
	if verb == "add" {
		write = writeAddNTM
	}
	ie, err := openInstance(socket)
	if err != nil {
		return write(stdout, stderr, asJSON, spawnOut{res: spawn.Result{Session: req.Session}}, err)
	}
	defer ie.db.Close()
	if verb == "add" {
		// ntm's add looks the session up first (resolveAddSession): a
		// unique prefix in text, the exact name under --json.
		r, err := resolveSession(ctx, ie, req.Session, asJSON, stdout, stderr)
		if err != nil {
			return write(stdout, stderr, asJSON, spawnOut{res: spawn.Result{Session: req.Session}}, err)
		}
		req.Session = r.Session
	}
	res, _, err := spawnUnderOwner(ctx, ie, &req, project)
	out := spawnOut{res: res, warnings: append(append([]string{}, res.Admission.Warnings...), res.Warnings...)}
	if s, ok := req.Scope.(core.Scope); ok {
		out.scope = &s
	}
	if err == nil {
		err = spawnOutcomeError(res)
		var outcome *spawnOutcome
		if req.VerifyBoot && errors.As(err, &outcome) {
			err = verifyBootError{outcome: outcome, within: req.ReadyTimeout}
		}
	}
	// spawn lists the panes under --json; add counts them in its text too.
	if (asJSON || verb == "add") && err == nil && !res.DryRun {
		var perr error
		if out.panes, perr = sessionPanes(ctx, ie, res); perr != nil {
			out.warnings = append(out.warnings, "the session's panes: "+perr.Error())
		}
	}
	return write(stdout, stderr, asJSON, out, err)
}

// spawnOut is what spawn's output shows: the spawn, its scope (nil when
// it was not resolved), the session's panes (nil when they were not
// read) and nhm's warnings.
type spawnOut struct {
	res      spawn.Result
	scope    *core.Scope
	panes    []paneResponse
	warnings []string
}

// spawnOutcomeError is the error of a spawn that ran and did not start
// every agent: ntm's spawn fails when an agent does not come up.
func spawnOutcomeError(res spawn.Result) error {
	if res.DryRun || res.Ready() || (res.NoWait && res.Started()) {
		return nil
	}
	var failed []string
	for _, t := range res.Targets {
		if targetUp(res, t) {
			continue
		}
		reason := strings.ReplaceAll(t.Status, "_", " ")
		if t.Error != "" {
			reason = t.Error
		}
		failed = append(failed, fmt.Sprintf("%s agent %s: %s", t.Kind, cmp.Or(t.PaneID, t.Slot, fmt.Sprint(t.Index)), reason))
	}
	return &spawnOutcome{res: res, msg: strings.Join(failed, "; ")}
}

// spawnOutcome is a spawn that ran and left agents down.
type spawnOutcome struct {
	res spawn.Result
	msg string
}

func (e *spawnOutcome) Error() string {
	up := 0
	for _, t := range e.res.Targets {
		if targetUp(e.res, t) {
			up++
		}
	}
	return fmt.Sprintf("%d/%d agent(s) ready: %s", up, len(e.res.Targets), e.msg)
}

// verifyBootError is ntm's --verify-boot failure (spawn.go 3768-3772).
type verifyBootError struct {
	outcome *spawnOutcome
	within  time.Duration
}

func (e verifyBootError) Error() string {
	up := 0
	for _, t := range e.outcome.res.Targets {
		if targetUp(e.outcome.res, t) {
			up++
		}
	}
	return fmt.Sprintf("spawn --verify-boot: %d/%d agent(s) ready within %s: %s; the session and panes still exist — inspect with --robot-tail",
		up, len(e.outcome.res.Targets), e.within, e.outcome.msg)
}

func (e verifyBootError) Unwrap() error { return e.outcome }

// timedOut reports whether the agents that are down ran out of time
// waiting to be ready.
func (e *spawnOutcome) timedOut() bool {
	for _, t := range e.res.Targets {
		if !targetUp(e.res, t) && t.Status != spawn.StatusUnresolved {
			return false
		}
	}
	return true
}

// made reports whether the spawn made anything: its workspace or a pane.
func made(res spawn.Result) bool {
	return madeWorkspace(res) || slices.ContainsFunc(res.Targets, func(t spawn.Target) bool { return t.PaneID != "" })
}

// madeWorkspace reports whether the spawn made its workspace: the plan
// says a new one (WorkspaceNew), and it has an id once it is made.
func madeWorkspace(res spawn.Result) bool {
	return res.WorkspaceNew && res.WorkspaceID != "" && !res.DryRun
}

// targetUp reports whether an agent came up: ready, or started by a spawn
// that does not wait.
func targetUp(res spawn.Result, t spawn.Target) bool {
	return t.Ready || (res.NoWait && t.Status == spawn.StatusStarted)
}

// writeSpawnNTM writes a spawn's outcome, ntm's way, and returns its exit.
func writeSpawnNTM(stdout, stderr io.Writer, asJSON bool, out spawnOut, err error) int {
	if out.warnings == nil {
		out.warnings = []string{}
	}
	res := out.res
	if asJSON {
		var v any = spawnResponseOf(out)
		if err != nil {
			v = spawnFailureOf(out, err)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if e := enc.Encode(v); e != nil {
			fmt.Fprintln(stderr, "Error:", e)
			return exitError
		}
		if err != nil {
			return exitError
		}
		return exitOK
	}
	for _, w := range out.warnings {
		fmt.Fprintf(stderr, "nhm spawn: warning: %s\n", w)
	}
	switch {
	case res.DryRun || res.Replayed:
		printSpawn(stdout, res)
	case err == nil || made(res):
		// An error before anything was made is the error alone, as ntm's.
		printSpawnSteps(stdout, res)
		printSpawn(stderr, res)
	}
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return exitError
	}
	if !res.DryRun {
		printSuccessFooter(stdout, spawnSuggestions(res.Session)...)
	}
	return exitOK
}

// spawnNHM is nhm's own facts of a spawn, under "_nhm": what nhm's
// envelope held, so that a reader of ".result" and ".scope" reads
// "._nhm.result" and "._nhm.scope".
func spawnNHM(res spawn.Result, scope *core.Scope, warnings []string) map[string]any {
	m := map[string]any{"result": res, "warnings": warnings}
	if res.OperationID != "" {
		m["operation_id"] = res.OperationID
	}
	if scope != nil {
		m["scope"] = scope
	}
	return m
}

// spawnResponseOf projects a spawn onto ntm's SpawnResponse: the session's
// panes after the spawn (panes; the spawn's own agents when they could
// not be read), the counts of their types, the stagger of the prompts and
// the agents' Agent Mail names. A dry run created nothing.
func spawnResponseOf(o spawnOut) spawnResponse {
	res := o.res
	out := spawnResponse{timestampedResponse: newTimestamped(), Session: res.Session, Created: !res.DryRun,
		WorkingDirectory: res.ProjectRoot, Panes: []paneResponse{}, NHM: spawnNHM(res, o.scope, o.warnings)}
	if res.DryRun {
		return out
	}
	panes := o.panes
	if panes == nil {
		panes = targetPanes(res)
	}
	out.Panes = panes
	for _, p := range panes {
		incrementAgentCounts(&out.AgentCounts, p.Type)
	}
	if s := res.Stagger; s != nil && s.Mode != "" && s.Mode != "none" {
		out.Stagger = &staggerConfig{Enabled: true, Mode: s.Mode, IntervalMs: s.IntervalMs}
	}
	out.AgentMail = agentMailOf(res)
	return out
}

// agentMailOf is the Agent Mail names of the spawn's agents, by pane.
func agentMailOf(res spawn.Result) *agentMailSpawnStatus {
	mail := &agentMailSpawnStatus{AgentMap: map[string]string{}}
	for _, t := range res.Targets {
		if t.MailName != "" && t.PaneID != "" {
			mail.AgentMap[t.PaneID] = t.MailName
			mail.AgentsRegistered++
		} else {
			mail.AgentsFailed++
		}
	}
	mail.Available = mail.AgentsRegistered > 0
	mail.ProjectRegistered = mail.Available
	return mail
}

// writeAddNTM writes an add's outcome, ntm's way, and returns its exit:
// "Adding N agent(s) to session 'S'..." once admission let it through,
// then "✓ Added N agent(s) (total M panes now)".
func writeAddNTM(stdout, stderr io.Writer, asJSON bool, out spawnOut, err error) int {
	if out.warnings == nil {
		out.warnings = []string{}
	}
	res := out.res
	if asJSON {
		var v any = addResponseOf(out)
		if err != nil {
			v = spawnFailureOf(out, err)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if e := enc.Encode(v); e != nil {
			fmt.Fprintln(stderr, "Error:", e)
			return exitError
		}
		if err != nil {
			return exitError
		}
		return exitOK
	}
	for _, w := range out.warnings {
		fmt.Fprintf(stderr, "nhm add: warning: %s\n", w)
	}
	switch {
	case res.DryRun || res.Replayed:
		printSpawn(stdout, res)
	case res.Admission.Decision == spawn.Admit:
		fmt.Fprintf(stdout, "Adding %d agent(s) to session '%s'...\n", len(res.Targets), res.Session)
		printSpawn(stderr, res)
	}
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return exitError
	}
	if !res.DryRun && !res.Replayed {
		total := len(out.panes)
		if out.panes == nil {
			total = len(targetPanes(res))
		}
		fmt.Fprintf(stdout, "✓ Added %d agent(s) (total %d panes now)\n", len(res.Targets), total)
		printSuccessFooter(stdout, addSuggestions(res.Session)...)
	}
	return exitOK
}

// addResponseOf projects an add onto ntm's AddResponse: the agents added
// by type, their panes, their Agent Mail names and the admission. A dry
// run added nothing.
func addResponseOf(o spawnOut) addResponse {
	res := o.res
	out := addResponse{timestampedResponse: newTimestamped(), Session: res.Session, NHM: spawnNHM(res, o.scope, o.warnings),
		Admission: &robot.SpawnAdmission{Decision: res.Admission.Decision, Reason: res.Admission.Reason, Session: res.Session,
			RequestedAgents: len(res.Targets), Reasons: res.Admission.Reasons, Warnings: res.Admission.Warnings}}
	if res.DryRun {
		return out
	}
	for _, t := range res.Targets {
		switch kinds.Lookup(t.Kind).NTMType {
		case "claude":
			out.AddedClaude++
		case "codex":
			out.AddedCodex++
		case "gemini":
			out.AddedGemini++
		case "antigravity":
			out.AddedAntigravity++
		case "grok":
			out.AddedGrok++
		case "ollama":
			out.AddedOllama++
		case "cursor":
			out.AddedCursor++
		case "windsurf":
			out.AddedWindsurf++
		case "aider":
			out.AddedAider++
		case "oc", "opencode":
			out.AddedOpencode++
		case "omp":
			out.AddedOmp++
		}
		out.TotalAdded++
	}
	added := map[string]bool{}
	for _, t := range res.Targets {
		added[t.PaneID] = t.PaneID != ""
	}
	panes := o.panes
	if panes == nil {
		panes = targetPanes(res)
	}
	for _, p := range panes {
		if added[p.PaneID] {
			out.NewPanes = append(out.NewPanes, p)
		}
	}
	out.AgentMail = agentMailOf(res)
	return out
}

// spawnFailureOf projects a failed spawn onto ntm's
// agentLifecycleFailureResponse, with ntm's codes: RESOURCE_BUSY for a
// refused admission, TIMEOUT for a ready wait or a context that ran out,
// INTERNAL_ERROR for the rest.
func spawnFailureOf(o spawnOut, err error) agentLifecycleFailureResponse {
	res := o.res
	code := robot.ErrCodeInternalError
	var admission *robot.SpawnAdmission
	var outcome *spawnOutcome
	switch {
	case errors.Is(err, spawn.ErrRefused):
		code = robot.ErrCodeResourceBusy
		admission = &robot.SpawnAdmission{Decision: res.Admission.Decision, Reason: res.Admission.Reason,
			Hint: spawn.AdmissionHint, Session: res.Session,
			RequestedAgents: len(res.Targets), Reasons: res.Admission.Reasons, Warnings: res.Admission.Warnings}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = robot.ErrCodeTimeout
	case errors.As(err, &outcome) && outcome.timedOut():
		code = robot.ErrCodeTimeout
	}
	var paneIDs []string
	for _, t := range res.Targets {
		paneIDs = append(paneIDs, t.PaneID)
	}
	// --safety refused a session that exists, as ntm's does.
	mayExist := res.WorkspaceID != "" || errors.Is(err, spawn.ErrSessionExists)
	out := newAgentLifecycleFailureResponse(err, code, res.Session, made(res), mayExist, paneIDs, admission)
	out.NHM = spawnNHM(res, o.scope, o.warnings)
	return out
}

// targetPanes is the spawn's own agents as ntm's panes.
func targetPanes(res spawn.Result) []paneResponse {
	panes := []paneResponse{}
	for _, t := range res.Targets {
		if t.PaneID == "" {
			continue
		}
		p := paneResponse{PaneID: t.PaneID, PaneTarget: cmp.Or(t.Slot, t.PaneID), Title: cmp.Or(t.MailName, t.HerdrName),
			Type: kinds.Lookup(t.Kind).NTMType, Variant: t.Model, PromptDelayMs: t.DelayMS}
		if s, err := bindings.ParseSlot(t.Slot); err == nil {
			p.WindowIndex, p.Index = s.Tab, s.Pane
		}
		panes = append(panes, p)
	}
	return panes
}

// sessionPanes reads the session's panes after a spawn, as ntm lists them:
// every pane of its workspace, an nhm agent's at its slot N.M (window N,
// pane M), any other pane at index 0 of its tab's N, as "user" or as the
// agent herdr sees in it.
func sessionPanes(ctx context.Context, ie *instanceEnv, res spawn.Result) ([]paneResponse, error) {
	if res.WorkspaceID == "" {
		return nil, errors.New("no workspace")
	}
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	bs, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: res.WorkspaceID,
		Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return nil, err
	}
	return panesOf(snap, bs, res), nil
}

// panesOf projects a workspace's herdr panes onto ntm's panes, in window
// and pane order, with the spawn's own agents; delays are the spawn's
// prompt delays.
func panesOf(snap herdr.SessionSnapshot, bs []bindings.Binding, res spawn.Result) []paneResponse {
	byPane := map[string]bindings.Binding{}
	tabN := map[string]int{}
	for _, b := range bs {
		byPane[b.PaneID] = b
		if s, err := bindings.ParseSlot(b.Slot); err == nil {
			tabN[b.TabID] = s.Tab
		}
	}
	delays := map[string]int64{}
	for _, t := range res.Targets {
		delays[t.PaneID] = t.DelayMS
	}
	panes := []paneResponse{}
	for _, p := range snap.Panes {
		if p.WorkspaceID != res.WorkspaceID {
			continue
		}
		r := paneResponse{PaneID: p.PaneID, PaneTarget: p.PaneID, WindowIndex: tabN[p.TabID], Title: cmp.Or(p.Label, p.Title),
			Type: "user", Active: p.Focused, PromptDelayMs: delays[p.PaneID]}
		if p.Agent != "" {
			// An agent nhm did not start: herdr's name for it is the kind.
			r.Type = kinds.Lookup(p.Agent).NTMType
		}
		if b, ok := byPane[p.PaneID]; ok {
			r.Type, r.Variant = kinds.Lookup(b.Kind).NTMType, b.Model
			if s, err := bindings.ParseSlot(b.Slot); err == nil {
				r.PaneTarget, r.WindowIndex, r.Index = b.Slot, s.Tab, s.Pane
			}
		}
		panes = append(panes, r)
	}
	// An agent of the spawn that herdr's snapshot does not show yet is
	// still the spawn's.
	for _, p := range targetPanes(res) {
		if !slices.ContainsFunc(panes, func(q paneResponse) bool { return q.PaneID == p.PaneID }) {
			panes = append(panes, p)
		}
	}
	slices.SortStableFunc(panes, func(a, b paneResponse) int {
		return cmp.Or(cmp.Compare(a.WindowIndex, b.WindowIndex), cmp.Compare(a.Index, b.Index))
	})
	return panes
}

// printSpawnSteps prints ntm's step lines for what the spawn did: the
// session, the panes and the launch, the prompts' schedule under a
// stagger, and the wait for them. ntm's pane_init_delay_ms is tmux's:
// herdr has none, and nhm prints no step for it.
func printSpawnSteps(w io.Writer, res spawn.Result) {
	st := newSteps(w)
	if madeWorkspace(res) {
		st.start(fmt.Sprintf("Creating session '%s'", res.Session)).done()
	}
	panes, down := 0, false
	for _, t := range res.Targets {
		if t.PaneID != "" {
			panes++
		}
		down = down || !targetUp(res, t)
	}
	if panes > 0 {
		st.start(fmt.Sprintf("Creating %d pane(s)", panes))
		if panes < len(res.Targets) && down {
			st.fail()
			return
		}
		st.done()
	}
	if len(res.Targets) == 0 || panes == 0 {
		return
	}
	st.start(fmt.Sprintf("Launching %d agent(s)", len(res.Targets)))
	var maxDelay time.Duration
	if s := res.Stagger; s != nil && s.Mode != "" && s.Mode != "none" {
		for i, t := range res.Targets {
			if t.KickoffOp == "" {
				continue
			}
			d := time.Duration(t.DelayMS) * time.Millisecond
			maxDelay = max(maxDelay, d)
			fmt.Fprintf(w, "  → Agent %d prompt scheduled in %v\n", i+1, d)
		}
	}
	if down {
		st.fail()
		return
	}
	st.done()
	if maxDelay > 0 {
		fmt.Fprintf(w, "⏳ Waiting for staggered prompts (max %v)...\n", maxDelay)
		fmt.Fprintln(w, "✓ All staggered prompts delivered")
	}
}
