// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/robot.go
// (RenderHelp, robotHelpSections, renderHelpCommandSection,
// robotHelpFlagUsage, robotHelpRequiredParameterHints) and the summaries
// of the commands in internal/robot/capabilities.go (summarizeDescription
// of their Description) at commit 9d5ed977; see LICENSE in this directory
// and PROVENANCE.md. Local changes: the sections list only the robot flags
// that this nhm implements (the inventory), in ntm's order and line
// layout; nhm's text in place of ntm's fixed text (herdr, nhm's commands,
// its modifiers and conditions, no attention feed); the summaries and
// required parameters are ntm's, kept here for the flags nhm answers,
// where ntm reads its registry, with "tmux" and "ntm" read as "herdr" and
// "nhm" (nhm-2p6.2.6).

package cli

import (
	"fmt"
	"strings"

	"github.com/arosl/nhm/compat"
)

// robotHelpEntry is what ntm's help prints for a robot flag: its
// registry summary, and its required parameters (the session one makes
// the usage --robot-x=SESSION).
type robotHelpEntry struct {
	summary  string
	session  bool
	requires []string
}

// robotHelpEntries are ntm's registry facts for the flags of its help
// sections that nhm answers.
var robotHelpEntries = map[string]robotHelpEntry{
	"status":       {summary: "Get herdr sessions, panes, and agent states."},
	"snapshot":     {summary: "Unified state query: sessions + beads + alerts + mail."},
	"capabilities": {summary: "Discover robot commands, optionally filtered or projected into a compact token-efficient catalog."},
	"version":      {summary: "Get nhm version, commit, and build info."},
	"spawn":        {summary: "Create session with agents and optionally wait for readiness or assign initial work.", session: true},
	"agent-names":  {summary: "Get stable agent-name mappings for a session.", session: true},
	"send":         {summary: "Send message to panes atomically.", session: true, requires: []string{"--msg=MSG"}},
	"tail":         {summary: "Capture recent output from panes.", session: true},
	"interrupt":    {summary: "Send Ctrl+C to stop agents, optionally send a new task.", session: true},
	// ntm's names its attention-feed and convergence conditions too,
	// which nhm does not answer.
	"wait":     {summary: "Wait for pane-state conditions and return a machine-usable wake result.", session: true},
	"assign":   {summary: "Get work distribution recommendations for assigning beads to agents.", session: true},
	"health":   {summary: "Get session or project health status."},
	"activity": {summary: "Get agent activity state (idle/busy/error) for all agents in a session.", session: true},
	"errors":   {summary: "Filter pane output to show only errors and recent failure context.", session: true},
	"logs":     {summary: "Get aggregated logs from agent panes.", session: true},
}

// robotHelpSections are ntm's help sections, with their commands in
// ntm's order; a command nhm does not implement is left out.
var robotHelpSections = []struct {
	title    string
	names    []string
	postlude string
}{
	{title: "Core Commands", names: []string{"status", "snapshot", "capabilities", "version"}},
	{title: "Session Operations", names: []string{"spawn", "controller-spawn", "agent-names", "context-inject",
		"ensemble_spawn", "send", "tail", "ensemble", "ensemble-suggest", "ensemble-stop", "interrupt", "overlay",
		"is-working", "restart-pane", "smart-restart", "wait"},
		postlude: `                        Conditions: idle, complete, generating, healthy

Note: Pane-targeting commands leave out the calling pane by default.
`},
	{title: "Work Distribution", names: []string{"assign", "bulk-assign", "beads-list", "bead-claim", "bead-create", "bead-close"}},
	{title: "Analysis & Monitoring", names: []string{"triage", "plan", "graph", "context", "health", "activity",
		"agent-health", "health-oauth", "diagnose", "errors", "logs", "monitor", "support-bundle"}},
	{title: "Tool Bridges", names: []string{"cass-search", "cass-context", "cass-insights", "schema", "mail-check", "env",
		"quota-status", "quota-check", "account-status", "accounts-list", "switch-account", "xf-search", "xf-status",
		"acfs-status", "setup-status", "default-prompts", "profile-list", "profile-show", "giil-fetch", "jfp-search",
		"jfp-export", "jfp-update", "ms-search", "ms-show", "slb-pending", "slb-approve", "slb-deny", "tokens", "history"}},
}

// renderRobotHelp is ntm's RenderHelp for this nhm: plain text, not JSON.
func renderRobotHelp() string {
	implemented := map[string]bool{}
	for _, r := range compat.Rows() {
		if flag := robotRowFlag(r.Form); flag != "" && r.Disposition == "implemented" {
			implemented[strings.TrimPrefix(flag, "--robot-")] = true
		}
	}
	var b strings.Builder
	b.WriteString(`nhm AI Agent Interface
======================
Robot mode provides a JSON API for AI agents to orchestrate coding agents in herdr.

API Design Principles:
----------------------
1. Global commands: bool flags (--robot-status, --robot-snapshot)
2. Session-scoped: =SESSION syntax (--robot-send=myproj, --robot-tail=myproj)
3. Modifiers: unprefixed global flags (--limit, --offset, and shared flags like --lines and --type)
4. Output: JSON by default, TOON for token-efficient (--robot-format=toon)
`)
	for _, s := range robotHelpSections {
		b.WriteString(s.title + ":\n" + strings.Repeat("-", len(s.title)+1) + "\n")
		for _, name := range s.names {
			e, ok := robotHelpEntries[name]
			if !ok || !implemented[name] {
				continue
			}
			usage := "--robot-" + name
			if e.session {
				usage += "=SESSION"
			}
			summary := e.summary
			if len(e.requires) > 0 {
				summary += " Requires " + strings.Join(e.requires, ", ") + "."
			}
			fmt.Fprintf(&b, "%-24s %s\n", usage, summary)
		}
		b.WriteString("\n")
		b.WriteString(s.postlude)
	}
	b.WriteString(`
Output Formats:
---------------
--robot-format=json     Full JSON (default; auto is JSON)
--robot-format=toon     TOON format (toon_rust's tru, else a pure Go encoder)
--robot-verbosity=V     terse (short keys), default, or debug (adds _debug)

Common Modifiers:
-----------------
--limit=N        Max sessions for --robot-status and --robot-snapshot
--offset=N       Pagination offset for the same
--robot-limit=N  Explicit pagination alias for robot list outputs
--robot-offset=N Explicit pagination alias for robot list outputs
--lines=N        Lines per pane for --robot-tail
--type=TYPE      Agent type filter for commands that support it (claude, codex, antigravity, grok, omp, pi)
--timeout=VALUE  Shared timeout for wait, ack and interrupt, and spawn --spawn-wait
--msg=TEXT       Message payload for send and interrupt
--panes=X,Y      Pane filter (slot N.M or a herdr pane id)
--dry-run        Preview without executing

Quick Start:
------------
1) Create session:    nhm --robot-spawn=proj --spawn-dir=/path/to/proj --spawn-cc=2 --spawn-wait
2) Check state:       nhm --robot-status
3) Send prompt:       nhm --robot-send=proj --type=claude --msg="implement auth"
4) Wait for idle:     nhm --robot-wait=proj --condition=idle
5) Get output:        nhm --robot-tail=proj --lines=100

Tips for AI Agents:
-------------------
- Start with --robot-status or --robot-snapshot.
- Prefer --robot-capabilities for discovery over parsing help text: it marks each ntm robot flag this nhm does not answer, with the reason.
- Pass --op-id=ID to --robot-send or --robot-interrupt when you may retry after a timeout: an identical retry replays the recorded outcome instead of sending again, and --robot-send-receipt=ID reports the outcome.

For topic documentation:      nhm --robot-docs=quickstart
For machine-readable schema:  nhm --robot-capabilities
`)
	return b.String()
}
