package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/arosl/nhm/compat"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/integrations/quota"
	"github.com/arosl/nhm/internal/robot"
)

// healthLines is how many visible lines that are not blank --robot-health
// reads of each agent that is not working, as nhm limits does.
const healthLines = 15

// Robot exit codes, ntm's (ExitCodeForResponse): 0 success, 2 only for
// NOT_IMPLEMENTED, 1 for every other error, DEPENDENCY_MISSING too
// (nhm-i3k).
const (
	robotOK             = 0
	robotError          = 1
	robotNotImplemented = 2
)

// runRobot answers ntm's --robot-* read flags with ntm's types (plan-v8
// 2.12). A robot flag nhm does not implement yet answers NOT_IMPLEMENTED
// with the inventory's reason, never a guess.
func runRobot(args []string, stdout, stderr io.Writer) int {
	if robotDispatchFlag(args) != "" {
		return runRobotDispatch(args, stdout, stderr)
	}
	if hasRobotSpawn(args) {
		return runRobotSpawn(args, stdout, stderr)
	}
	if hasRobotFlag(args, "--robot-wait") {
		return runRobotWait(args, stdout)
	}
	if hasRobotFlag(args, "--robot-interrupt") {
		return runRobotInterrupt(args, stdout)
	}
	if hasRobotFlag(args, "--robot-assign") {
		return runRobotAssign(args, stdout)
	}
	if hasRobotFlag(args, "--robot-errors") {
		return runRobotErrors(args, stdout)
	}
	if hasRobotFlag(args, "--robot-logs") {
		return runRobotLogs(args, stdout)
	}
	if hasRobotFlag(args, "--robot-pane-address") {
		return runRobotPaneAddress(args, stdout)
	}
	if hasRobotFlag(args, "--robot-inspect-pane") {
		return runRobotInspectPane(args, stdout)
	}
	if hasRobotFlag(args, "--robot-watch-bead") {
		return runRobotWatchBead(args, stdout)
	}
	if hasRobotFlag(args, "--robot-agent-names") {
		return runRobotAgentNames(args, stdout)
	}
	if hasRobotFlag(args, "--robot-palette") {
		return runRobotPalette(args, stdout)
	}
	now := time.Now()
	if slices.Contains(args, "--robot-health") {
		// ntm's bare form is project health: bv's drift, bottlenecks and
		// recommendations. nhm answers the session form only.
		return robotFail(stdout, robot.ErrCodeNotImplemented, "--robot-health without a session is not in this nhm",
			"use --robot-health=SESSION for each agent's health; nhm health covers the runtime", "auto", robotNotImplemented, now)
	}
	if slices.Contains(args, "--robot-health=") {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-health requires a session name or no value for project health",
			"Use --robot-health for project health or --robot-health=SESSION for session health", "auto", robotError, now)
	}
	fs, root := newRobotFlagSet(args, stdout)
	defer root.undo()
	status := fs.Bool("robot-status", false, "")
	snap := fs.Bool("robot-snapshot", false, "")
	help := fs.Bool("robot-help", false, "")
	caps := fs.Bool("robot-capabilities", false, "")
	version := fs.Bool("robot-version", false, "")
	docs := fs.String("robot-docs", "", "")
	activity := fs.String("robot-activity", "", "")
	tail := fs.String("robot-tail", "", "")
	health := fs.String("robot-health", "", "")
	format := &root.format
	lines := fs.Int("lines", 20, "")
	socket := fs.String("socket", "", "")
	noMail := fs.Bool("no-mail", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if name := unknownRobotFlag(fs, args); name != "" {
			return robotFail(stdout, "NOT_IMPLEMENTED", fmt.Sprintf("%s is not in this nhm", name),
				notImplementedHint(name), "auto", robotNotImplemented, now)
		}
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "auto", robotError, now)
	}
	root.noConfig = *help || *caps || *version
	if code, done := applyRobotRoot(root, now); done {
		return code
	}
	// --robot-docs counts when given, empty too (ntm's Changed): an empty
	// topic is the topic index.
	docsGiven := false
	fs.Visit(func(f *flag.Flag) { docsGiven = docsGiven || f.Name == "robot-docs" })
	chosen := 0
	for _, b := range []bool{*help, *status, *snap, *caps, *version, docsGiven, *activity != "", *tail != "", *health != ""} {
		if b {
			chosen++
		}
	}
	if chosen != 1 {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "give exactly one --robot-* flag", "nhm --robot-capabilities lists them", *format, robotError, now)
	}
	if *help {
		// ntm's help is plain text, whatever the format; only a bad format
		// or verbosity failed it first, as JSON (applyRobotRoot).
		if _, err := io.WriteString(stdout, renderRobotHelp()); err != nil {
			return robotError
		}
		return robotOK
	}
	if *caps {
		return robotWrite(stdout, robotCapabilities(*format, now), robotOK)
	}
	if *version {
		return robotWrite(stdout, robotVersion(*format, now), robotOK)
	}
	if docsGiven {
		out := robot.GetDocs(*docs, Version, robotDocsCommands(), *format, now)
		if !out.Success {
			return robotWrite(stdout, out, robotError)
		}
		return robotWrite(stdout, out, robotOK)
	}
	var page robot.PaginationOptions
	if *status || *snap {
		var err error
		if page, err = root.pagination(fs); err != nil {
			return robotFail(stdout, robot.ErrCodeInvalidFlag, err.Error(), robotPagingHint, *format, robotError, now)
		}
	}
	if *lines <= 0 {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--lines must be positive", "", *format, robotError, now)
	}
	r, err := openReader(*socket, (*status || *snap) && !*noMail)
	if err != nil {
		return robotFail(stdout, robot.ErrCodeDependencyMissing, err.Error(), "run inside herdr or give --socket", *format, robotError, now)
	}
	defer r.close()
	ctx := context.Background()
	m := r.model(ctx, "")
	if !m.HerdrOK() {
		if msg, hint := herdrRefusal(m, *status, *snap); msg != "" {
			return robotFail(stdout, robot.ErrCodeDependencyMissing, msg, hint, *format, robotError, now)
		}
	}
	switch {
	case *status:
		out := snapshot.Status(m, snapshot.Attention(m), buildInfo(), *format, now)
		out.Sessions, out.Pagination, out.AgentHints = robot.PageSessions(out.Sessions, page)
		return robotWrite(stdout, out, robotOK)
	case *snap:
		out := snapshot.Snapshot(m, snapshot.Attention(m), *format, now)
		out.Sessions, out.Pagination, out.AgentHints = robot.PageSessions(out.Sessions, page)
		return robotWrite(stdout, out, robotOK)
	}
	session := *activity + *tail + *health
	one, ws, err := selectWorkspace(m, session)
	if err != nil {
		code := robot.ErrCodeSessionNotFound
		if errors.Is(err, snapshot.ErrAmbiguousWorkspace) {
			code = robot.ErrCodeInvalidFlag
		}
		return robotFail(stdout, code, err.Error(), "list sessions: nhm --robot-status", *format, robotError, now)
	}
	switch {
	case *activity != "":
		return robotWrite(stdout, snapshot.ActivityFor(one, *ws, *format, now), robotOK)
	case *health != "":
		// As ntm, read the screens now: each agent that is not working,
		// its visible lines, for nhm's limit recogniser (nhm-8l6). The
		// whole screen: its last lines can be blank (nhm-1q7).
		reads, errs := map[string]herdr.PaneRead{}, map[string]error{}
		for _, a := range one.Agents {
			if a.WorkspaceID != ws.ID || a.State == herdr.StatusWorking {
				continue
			}
			read, err := r.herdr.AgentRead(ctx, herdr.AgentReadParams{Target: a.PaneID, Source: herdr.ReadVisible})
			if err != nil {
				errs[a.PaneID] = err
				continue
			}
			read.Text = quota.LastLines(stripControl(read.Text), healthLines)
			reads[a.PaneID] = read
		}
		// The quota sources nhm quota reads, live, for a rate limit's wait.
		live := func() []quota.Observation { return quota.Collect(quota.DefaultEnv(), now, nil) }
		return robotWrite(stdout, snapshot.HealthFor(one, *ws, reads, errs, live, *format, now), robotOK)
	}
	reads, errs := map[string]herdr.PaneRead{}, map[string]error{}
	for _, a := range one.Agents {
		n := *lines
		read, err := r.herdr.PaneRead(ctx, herdr.PaneReadParams{PaneID: a.PaneID, Source: herdr.ReadRecent, Lines: &n})
		if err != nil {
			errs[a.PaneID] = err
			continue
		}
		read.Text = stripControl(read.Text)
		reads[a.PaneID] = read
	}
	return robotWrite(stdout, snapshot.TailFor(one, *ws, reads, errs, *format, now), robotOK)
}

// herdrRefusal is the error of a read that herdr did not answer, or ""
// when the read answers as ntm's does with tmux down (nhm-fyq):
// --robot-status always succeeds, as buildLiveStatus swallows a failed
// collection, and --robot-snapshot succeeds when no server runs, as
// ListSessions then lists none, but not when herdr is not installed. Such
// an answer has no sessions, herdr in degraded_sources and its error under
// _nhm.sources.
func herdrRefusal(m *snapshot.Model, status, snap bool) (msg, hint string) {
	switch {
	case status:
		return "", ""
	case snap && m.HerdrDown():
		if _, err := exec.LookPath(herdrBin()); err != nil {
			return "herdr is not installed", "install herdr to enable snapshot"
		}
		return "", ""
	}
	return "herdr did not answer: " + m.Sources[snapshot.SourceHerdr].Error, "start the herdr server; nhm doctor checks it"
}

// hasRobotFlag reports whether args hold the robot flag name.
func hasRobotFlag(args []string, name string) bool {
	for _, a := range args {
		if n, _, _ := strings.Cut(a, "="); n == name {
			return true
		}
	}
	return false
}

// robotKinds maps ntm's --type values to nhm's kinds: a kind's name,
// its short flag (cc, cod) or ntm's type name (antigravity). Another
// value stays as given, so it matches no agent, as in ntm.
func robotKinds(types []string) []string {
	out := make([]string, len(types))
	for i, t := range types {
		v := strings.ToLower(t)
		out[i] = v
		if a, ok := kinds.Default().ByFlag(v); ok {
			out[i] = a.Name
			continue
		}
		for _, a := range kinds.All() {
			if a.NTMType == v {
				out[i] = a.Name
				break
			}
		}
	}
	return out
}

// unknownRobotFlag returns the first --robot-* flag of args that fs, the
// call's flag set, does not define: one this nhm does not answer. The
// global modifiers are in every flag set, so they never count.
func unknownRobotFlag(fs *flag.FlagSet, args []string) string {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if strings.HasPrefix(name, "--robot-") && fs.Lookup(name[2:]) == nil {
			return name
		}
	}
	return ""
}

// notImplementedHint is the disposition of flagName's own robot row: the
// row whose form starts with that whole flag, so --robot-ensemble never
// gets the --robot-ensemble-modes row's hint (nhm-2p6.2.10).
func notImplementedHint(flagName string) string {
	for _, r := range compat.Rows() {
		if robotRowFlag(r.Form) == flagName && r.Disposition != "implemented" {
			return r.Disposition + ": " + r.Note
		}
	}
	return "nhm --robot-capabilities lists what this nhm answers"
}

// robotRowFlag is the --robot-* flag that a row's form starts with, cut
// where its name ends ("=", a space, a backtick), or "" for a row of
// another form.
func robotRowFlag(form string) string {
	form = strings.TrimPrefix(form, "`")
	if !strings.HasPrefix(form, "--robot-") {
		return ""
	}
	end := strings.IndexFunc(form[2:], func(c rune) bool {
		return c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9')
	})
	if end < 0 {
		return form
	}
	return form[:2+end]
}

// robotCapabilities lists the robot flags of the inventory, the missing
// ones marked unavailable with their reason.
func robotCapabilities(format string, now time.Time) robot.CapabilitiesOutput {
	out := robot.CapabilitiesOutput{RobotResponse: robot.NewResponse(true, format, now), Version: Version,
		Commands: []robot.RobotCommandInfo{}, Categories: []string{"state", "dispatch", "lifecycle"},
		NHM: map[string]any{"ntm_commit": NTMCommit, "herdr_protocol": herdr.Protocol}}
	for _, r := range compat.Rows() {
		flagName := robotRowFlag(r.Form)
		if flagName == "" {
			continue
		}
		cmd := robot.RobotCommandInfo{Name: strings.TrimPrefix(flagName, "--"), Flag: flagName, Category: robotCategory(flagName),
			OutputFormats: []string{"json"}, DefaultOutputFormat: "json", SchemaSource: "ntm@" + NTMCommit[:8]}
		switch flagName {
		case "--robot-status":
			cmd.SchemaID = robot.StatusSchemaID
		case "--robot-snapshot":
			cmd.SchemaID = robot.SnapshotSchemaID
		}
		if r.Disposition != "implemented" {
			cmd.Unavailable, cmd.UnavailableReason = true, r.Disposition+": "+r.Note
		}
		out.Commands = append(out.Commands, cmd)
	}
	return out
}

// robotDocsCommands is the commands topic of --robot-docs: each robot row
// of the inventory that nhm implements, its form as the row gives it,
// under ntm's headings, so the docs never name a flag that nhm refuses.
func robotDocsCommands() []robot.DocsSection {
	headings := []string{"State Inspection", "Agent Control", "Session Management", "Utilities"}
	bodies := map[string][]string{}
	for _, r := range compat.Rows() {
		flagName := robotRowFlag(r.Form)
		if flagName == "" || r.Disposition != "implemented" {
			continue
		}
		heading, ok := robotDocsHeading[flagName]
		if !ok {
			heading = "State Inspection"
		}
		// The form is the row's first code span; a note after it is the
		// inventory's, not the command's.
		form, _, _ := strings.Cut(strings.TrimPrefix(r.Form, "`"), "`")
		bodies[heading] = append(bodies[heading], strings.ReplaceAll(form, `\|`, "|"))
	}
	var out []robot.DocsSection
	for _, h := range headings {
		if len(bodies[h]) > 0 {
			out = append(out, robot.DocsSection{Heading: h, Body: strings.Join(bodies[h], "\n")})
		}
	}
	return out
}

// robotDocsHeading is the heading that ntm's commands topic lists a
// robot flag under; any other flag is State Inspection.
var robotDocsHeading = map[string]string{
	"--robot-send": "Agent Control", "--robot-send-receipt": "Agent Control", "--robot-ack": "Agent Control",
	"--robot-interrupt": "Agent Control", "--robot-wait": "Agent Control", "--robot-assign": "Agent Control",
	"--robot-spawn": "Session Management", "--robot-capabilities": "Utilities", "--robot-docs": "Utilities",
	"--robot-version": "Utilities", "--robot-help": "Utilities", "--robot-schema": "Utilities",
	"--robot-format": "Utilities", "--robot-output-format": "Utilities", "--robot-verbosity": "Utilities",
	"--robot-limit": "Utilities", "--robot-offset": "Utilities",
}

// robotVersion is ntm's GetVersion: the build and the Go runtime, with
// tmux_available false as in ntm, which never sets it here. nhm's extras,
// the ntm commit it tracks and the herdr protocol, are under _nhm.
func robotVersion(format string, now time.Time) robot.VersionOutput {
	b := buildInfo()
	return robot.VersionOutput{RobotResponse: robot.NewResponse(true, format, now),
		System: robot.SystemInfo{Version: b.Version, Commit: b.Commit, BuildDate: b.BuildDate,
			GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH},
		NHM: map[string]any{"ntm_commit": NTMCommit, "herdr_protocol": herdr.Protocol}}
}

func robotCategory(flagName string) string {
	switch flagName {
	case "--robot-send", "--robot-send-receipt", "--robot-ack", "--robot-wait", "--robot-assign":
		return "dispatch"
	case "--robot-spawn":
		return "lifecycle"
	default:
		return "state"
	}
}

// robotWrite writes a robot answer. A successful one (code robotOK, as
// ntm's exit code is 0 only for success) gets the call's verbosity, as
// ntm's encodeJSON applies it; an error envelope never does. A successful
// answer is indented, as ntm's JSONRenderer writes it, or TOON in the
// toon format, as ntm's TOONRenderer writes it (nhm-2p6.2.11); an error
// envelope is one line of JSON, as ntm's encodeRobotFailureJSON writes it.
func robotWrite(stdout io.Writer, v any, code int) int {
	if code == robotOK && robotOut.verbosity != robot.VerbosityDefault {
		v = robot.ApplyVerbosity(v, robotOut.verbosity, robotOut.format)
	}
	if code == robotOK && robotOut.format == "toon" {
		out, err := robot.RenderTOON(v)
		if err != nil {
			return robotFail(stdout, robot.ErrCodeInternalError, "toon: "+err.Error(), "use --robot-format=json", "toon", robotError, time.Now())
		}
		if _, err := io.WriteString(stdout, out); err != nil {
			return robotError
		}
		return code
	}
	enc := json.NewEncoder(stdout)
	if code == robotOK {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return robotError
	}
	return code
}

func robotFail(stdout io.Writer, code, msg, hint, format string, exit int, now time.Time) int {
	out := robot.NewError(code, msg, hint, format, now)
	out.Meta = &robot.ResponseMeta{ExitCode: exit, Command: robotCommand}
	robotWrite(stdout, out, exit)
	return exit
}
