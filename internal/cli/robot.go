package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
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
	now := time.Now()
	if slices.Contains(args, "--robot-health") {
		// ntm's bare form is project health: bv's drift, bottlenecks and
		// recommendations. nhm answers the session form only.
		return robotFail(stdout, robot.ErrCodeNotImplemented, "--robot-health without a session is not in this nhm",
			"use --robot-health=SESSION for each agent's health; nhm health covers the runtime", "json", robotNotImplemented, now)
	}
	if slices.Contains(args, "--robot-health=") {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "--robot-health requires a session name or no value for project health",
			"Use --robot-health for project health or --robot-health=SESSION for session health", "json", robotError, now)
	}
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	status := fs.Bool("robot-status", false, "")
	snap := fs.Bool("robot-snapshot", false, "")
	caps := fs.Bool("robot-capabilities", false, "")
	activity := fs.String("robot-activity", "", "")
	tail := fs.String("robot-tail", "", "")
	health := fs.String("robot-health", "", "")
	format := fs.String("robot-format", "auto", "")
	lines := fs.Int("lines", 20, "")
	socket := fs.String("socket", "", "")
	noMail := fs.Bool("no-mail", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		if name := unknownRobotFlag(args); name != "" {
			return robotFail(stdout, "NOT_IMPLEMENTED", fmt.Sprintf("%s is not in this nhm", name),
				notImplementedHint(name), "json", robotNotImplemented, now)
		}
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
	}
	switch *format {
	case "auto", "json":
		*format = "json"
	default:
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-format=%s: nhm writes json only", *format),
			"use --robot-format=json", "json", robotError, now)
	}
	chosen := 0
	for _, b := range []bool{*status, *snap, *caps, *activity != "", *tail != "", *health != ""} {
		if b {
			chosen++
		}
	}
	if chosen != 1 {
		return robotFail(stdout, robot.ErrCodeInvalidFlag, "give exactly one --robot-* flag", "nhm --robot-capabilities lists them", *format, robotError, now)
	}
	if *caps {
		return robotWrite(stdout, robotCapabilities(*format, now), robotOK)
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
		return robotWrite(stdout, snapshot.Status(m, snapshot.Attention(m), buildInfo(), *format, now), robotOK)
	case *snap:
		return robotWrite(stdout, snapshot.Snapshot(m, snapshot.Attention(m), *format, now), robotOK)
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

// unknownRobotFlag returns the first --robot-* flag this nhm does not
// parse.
func unknownRobotFlag(args []string) string {
	known := map[string]bool{"--robot-status": true, "--robot-snapshot": true, "--robot-capabilities": true,
		"--robot-activity": true, "--robot-tail": true, "--robot-health": true, "--robot-format": true,
		"--robot-send": true, "--robot-send-receipt": true, "--robot-ack": true, "--robot-spawn": true}
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if strings.HasPrefix(name, "--robot-") && !known[name] {
			return name
		}
	}
	return ""
}

func notImplementedHint(flagName string) string {
	for _, r := range compat.Rows() {
		if strings.HasPrefix(strings.Trim(r.Form, "`"), flagName) && r.Disposition != "implemented" {
			return r.Disposition + ": " + r.Note
		}
	}
	return "nhm --robot-capabilities lists what this nhm answers"
}

// robotCapabilities lists the robot flags of the inventory, the missing
// ones marked unavailable with their reason.
func robotCapabilities(format string, now time.Time) robot.CapabilitiesOutput {
	out := robot.CapabilitiesOutput{RobotResponse: robot.NewResponse(true, format, now), Version: Version,
		Commands: []robot.RobotCommandInfo{}, Categories: []string{"state", "dispatch", "lifecycle"},
		NHM: map[string]any{"ntm_commit": NTMCommit, "herdr_protocol": herdr.Protocol}}
	for _, r := range compat.Rows() {
		form := strings.Trim(r.Form, "`")
		if !strings.HasPrefix(form, "--robot-") {
			continue
		}
		flagName, _, _ := strings.Cut(strings.Fields(form)[0], "=")
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

func robotCategory(flagName string) string {
	switch flagName {
	case "--robot-send", "--robot-send-receipt", "--robot-ack", "--robot-wait":
		return "dispatch"
	case "--robot-spawn":
		return "lifecycle"
	default:
		return "state"
	}
}

func robotWrite(stdout io.Writer, v any, code int) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return robotError
	}
	return code
}

func robotFail(stdout io.Writer, code, msg, hint, format string, exit int, now time.Time) int {
	out := robot.NewError(code, msg, hint, format, now)
	out.Meta = &robot.ResponseMeta{ExitCode: exit}
	robotWrite(stdout, out, exit)
	return exit
}
