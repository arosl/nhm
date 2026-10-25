// Package cli parses nhm's command line and renders its output. It holds
// no behaviour of its own; later verbs call internal/core.
package cli

import "io"

// Build facts, set at link time by the release build (scripts/package.sh):
//
//	-ldflags "-X github.com/arosl/nhm/internal/cli.Version=0.1.0 ..."
//
// Date is the build's date (ISO 8601, UTC) and BuiltBy what built it, as
// ntm's version prints them.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
	BuiltBy = "unknown"
)

// NTMCommit is the ntm commit whose grammar and robot types nhm tracks.
const NTMCommit = "9d5ed97796e37fa7b7c89bc3ba4087c144f0102c"

const usage = `nhm: the herdr wrapper of the ACFS flywheel (what ntm is to tmux).

Usage:
  nhm version [--short] [--json]
                         print the version
  nhm spawn <session> [--cc=N[:MODEL]] [--cod=N] … [--prompt T] [--dry-run] [--json]
                         start agents: identity, pane, binding, kickoff
  nhm add <session> …    the same, into an existing workspace
  nhm retire <Name> [--keep-identity] [--cleanup-worktrees] [--dry-run]
                         close an agent's pane, keep its transcript, retire its identity
  nhm recycle <Name> [-t KEY | --prompt T] [--no-handoff]
                         a fresh session in the same pane, with a handoff
  nhm reap [session] [--idle MIN] [--all-workspaces] [--dry-run]
                         retire agents idle past the limit whose work is done
  nhm kill <session> [-f] [--tag T] | kill -p P
                         ntm's kill: close a session's workspace, end its processes
  nhm attach [session] [--json]
                         focus a session's workspace (without one: nhm list)
  nhm zoom [session] [pane] [--json]   (also z)
                         ntm's zoom: zoom one pane, then focus its workspace and tab
  nhm view [session] [--json]   (also tile, v)
                         ntm's view: the zoom off in every tab of a session, then focus it
  nhm diff [session] <pane1> <pane2> [-u] [--code-only] [--json]
                         compare two panes' output, as ntm's diff
  nhm copy [session[:pane]] [-l N] [-p RE] [--code] [--all|--cc|--cod|--gmi|--agy] [-o FILE] [--json]
                         a pane's recent output to the clipboard, as ntm's copy (also cp, yank)
  nhm grep <pattern> [session] [-i] [-v] [-C N] [-A N] [-B N] [-n N] [-l] [--all] [--cc|--cod|--gmi|--agy] [--json]
                         search the panes' recent output, as ntm's grep
  nhm errors [session] [--panes N,…] [-C N] [-n N] [--cc|--cod|--gmi|--agy] [--json]
                         the error lines in the agents' recent output, as ntm's errors
  nhm get-all-session-text [--lines N] [--compact]   (also gast, swarm-status)
                         every session's panes as a markdown table, as ntm's
  nhm switch <agent-name> [--session S] [--json]
                         focus an agent's pane, as ntm's switch
  nhm extract <session> [pane] [-l L] [--last] [-n N] [-s N] [-c] [-a] [--json]
                         the code blocks in the panes' output, as ntm's extract
  nhm save [session] [-o DIR] [-l N] [--all|--cc|--cod|--gmi|--agy]   (also dump, export)
                         each pane's recent output to a file, as ntm's save
  nhm summary [session] [--since D] [--format F] [--all] [--recent] [--regenerate]
                         what each agent did, from its output and git, as ntm's summary
  nhm admin restore | admin recover inspect | admin recover resume <binding> (--dry-run|--run)
  nhm admin export --project DIR --to DIR | admin import --from DIR
  nhm admin takeover (--dry-run | --yes)
                         move this server's agents from ACFS's Bash to nhm
  nhm new <session>      create the workspace only
  nhm status [session] [--watch] [--json]
                         one row per agent: slot, name, kind, state, mail
  nhm list [--tag T] [-p NAME] [--limit N] [--offset N] [--json]   (also ls, l)
                         ntm's list: the sessions, their windows, state and agents
  nhm snapshot [session] the machine read model, as JSON
  nhm attention [session] [--watch] [--json|--jsonl]
                         what needs a decision
  nhm read <name> [--lines N] [--source visible|recent] [--raw-to FILE]
                         print an agent's pane
  nhm logs <session> [--panes N.M,…] [-f] [--filter RE] [-n N] [-a] [--json]
                         every agent's recent output, as ntm's logs (a name: nhm read)
  nhm watch [session] [name|slot ...] [--cc] [--pane P] [--bead ID] [--pattern G --command T] [--json]
                         stream the agents' output, a bead or files, as ntm's watch (also w)
  nhm wait [session] [--until C[,…]] [--timeout D] [--pane N] [--type T] [--any [--count N]] [--exit-on-error]
                         ntm's wait: until the agents reach a state
  nhm wait [session] --after-op OP [--until C] [--name N]… [--json]
                         until an operation's turn completes (nhm's result)
  nhm interrupt <session> [--tag T] [selector] [--json]
                         send each agent ntm's interrupt key (Ctrl+C; Escape for omp)
  nhm send <session> [prompt] [--all|--cc[=V]|--cod[=V]|…] [--tag T] [-p N|--panes P] [-t KEY] [--dry-run] [--json]
                         ntm's send: one prompt to the session's agents, a receipt per agent (--project P: every session of P)
  nhm memory serve [--port N] | context <task> | outcome <status> [--rules IDS] | privacy …
                         ntm's memory verbs over CM: run its daemon, ask it, record
  nhm feedback list|mark|outcomes [--json]
                         the rules each session got; tell CM how they did
  nhm modes list|explain <mode> [--json]
                         ntm's reasoning-mode catalog
  nhm template list|ls [-a] | show <name> [-b] [--json]
                         ntm's prompt templates: the ones send -t takes
  nhm ensemble presets [--json]
                         ntm's ensemble presets
  nhm activity <session> [--json]
                         each agent's activity in ntm's terms
  nhm health [session] [--json]
                         runtime health of sources and sessions
  nhm wake [--once] [--recipients NAME,…] [--json]
                         nudge agents with new Agent Mail, once per message
  nhm maintain [--once | --loop [--interval D]] [--json]
                         the maintenance pass: due wakes, inboxes, names
  nhm mail inbox [session] [--agent NAME] [--urgent] [--session-agents] [--json]
                         the project's inbox, read-only, as ntm's mail inbox
  nhm mail inbox (--mark-read | --keep-unread) [--agent NAME] [--json]
                         one agent's unread mail by thread; --mark-read marks it read
  nhm doctor [-v] [--json]
                         ntm's ecosystem check, on herdr and nhm's setup
  nhm doctor contention [--terminate-pid N --confirm [--force]] [--json]
                         the processes that hold beads, cargo or rsync
  nhm deps [-v] [--json] (also check)
                         the tools and agent CLIs installed, as ntm's deps
  nhm tutorial           where ACFS's lessons are (ntm's tutorial is about tmux)
  nhm capabilities [--json]
                         what this nhm answers, and why not
  nhm compatibility status [--json]
                         the ntm forms implemented, deferred, waived
  nhm --robot-status | --robot-snapshot | --robot-activity=S |
      --robot-tail=S [--lines N] | --robot-capabilities | --robot-version |
      --robot-docs=TOPIC | --robot-help
                         ntm's robot read flags, ntm's types
  nhm --robot-status | --robot-snapshot [--robot-limit=N] [--robot-offset=N]
                         a page of the sessions, ntm's pagination
  nhm --robot-send=S --panes=P --type=K (--msg=M | --msg-file=-) --op-id=ID |
      --robot-send-receipt=OP | --robot-ack=S [--op-id=ID] [--timeout=D]
                         ntm's robot dispatch flags, ntm's types
  nhm --robot-wait=S [--condition=idle|generating|healthy] [--timeout=D]
      [--transition] [--exit-on-error] [--panes=P] [--type=K] [--any]
                         wait for agents, ntm's WaitResponse
  nhm --robot-interrupt=S [--msg=M] [--force] [--no-wait] [--timeout=D]
      [--panes=P] [--type=K] [--dry-run] [--op-id=ID]
                         interrupt agents, ntm's InterruptOutput
  nhm --robot-health=S   each agent's health, ntm's SessionHealthOutput
  nhm --robot-assign=S [--beads=a,b] [--strategy=simple|balanced|speed|quality|dependency]
                         which ready bead each idle agent should take,
                         ntm's AssignOutput; it assigns nothing
  nhm --robot-palette [--session=S] [--category=C] [--search=Q]
                         the palette's prompts, pins and favorites, ntm's PaletteOutput
  nhm --robot-spawn=S --spawn-dir=R --spawn-cc=N --spawn-cod=N [--spawn-agy=N]
      --spawn-no-user --spawn-safety --spawn-wait [--ready-timeout=Ns] [--dry-run]
                         ntm's robot spawn, ntm's SpawnOutput
  nhm --robot-agent-names=S [--spawn-names=a,b]
                         a NATO name for each pane, ntm's AgentNamesOutput
  nhm --robot-dialogs=S [--panes=P]
                         each agent pane's dialog and its options, ntm's DialogsOutput
  nhm --robot-answer-dialog=S --panes=P --choice=decline|extra-usage|dismiss|option-K
                         answer one pane's dialog under ntm's policy, ntm's AnswerDialogOutput
  nhm --robot-exit-cli=S [--panes=P] [--relaunch]
                         exit each agent CLI and keep its shell, ntm's ExitCLIOutput
  nhm --robot-logs=S [--logs-panes=1,2] [--logs-limit=N] | --robot-pane-address=S
                         each agent's last lines, ntm's LogsOutput; each pane's addresses
  nhm --robot-watch-bead=S --bead=ID [--panes=P] [--lines=N] [--interval=D]
                         a bead's mentions in the agents' output, ntm's WatchBeadOutput
  nhm --robot-errors=S [--panes=P] [--lines=N] [--type=K]
                         the error lines in the agents' output, ntm's ErrorsOutput
  nhm --robot-inspect-pane=S [--index=N] [--lines=N] [--code]
                         one pane's agent, output, errors and files, ntm's InspectPaneOutput
  nhm --robot-diff=S [--since=D]
                         each pane's activity and git's changed files, ntm's DiffOutput
  nhm --robot-summary=S [--since=D]
                         each pane's output counts, files and actions, ntm's SessionSummaryResponse
  nhm --robot-* … --robot-format=json (or --robot-output-format=json)
      [--robot-verbosity=terse|default|debug]
                         every robot flag answers JSON (auto is JSON)
  nhm quota [--json]     each plan's windows and the live agents per kind
  nhm limits [session] [--once|--loop] [--dry-run] [--json]
                         the agents a usage limit stopped
  nhm palette | board | menu
                         the prompt palette, the agent board, the nhm menu
  nhm dashboard [session] [--no-tui] [--json] | dash | d
                         ntm's dashboard: nhm board, or ntm's plain and JSON forms
  nhm bind [--unbind | --force | --show | --print-client] [--dry-run]
                         herdr keys from [keys] in nhm's config (default prefix+a
                         palette, prefix+d board, prefix+m menu); --unbind opts out
  nhm resolve <name|herdr-name|pane-id|--current> [--json]
                         print a binding and how nhm verified it
  nhm adopt <MailName> [--pane ID] [--handoff-from BINDING] [--json]
                         bind an identity to the agent in a pane, by hand
  nhm move <Name> --workspace W [--dry-run] [--json]
                         move an agent's pane to a new tab of W; its binding follows
  nhm move <Name> --tab T [--per-tab N] [--dry-run] [--json]
                         move an agent's pane into tab T's next grid cell
  nhm arrange [session] [--per-tab N] [--dry-run] [--json]
                         pack a session's agents into full grid tabs, in slot order
  nhm admin instance enroll [--session NAME] [--socket PATH] [--json]
                         enroll a herdr server (ACFS's install runs it)
  nhm admin recover reconcile --import-only [--dry-run] [--json]
                         read Bash-spawned agents into bindings
  nhm help               print this text

Each verb prints its own help with --help. The other verbs come with the
next releases.
`

// Run executes nhm with args (without the program name) and returns the
// exit code. The robot flags have their own grammar (runRobot), after
// the root flags in front of them (runRobotCall); every other call goes
// through ntm's root (root.go), built after --config takes effect
// (configFirst).
func Run(args []string, stdout, stderr io.Writer) int {
	if code, ok := remoteRobotCall(args, stdout, stderr); ok {
		return code
	}
	if lead, rest, ok := robotCall(args); ok {
		return runRobotCall(lead, rest, stdout, stderr)
	}
	restore := configFirst(args)
	defer restore()
	return newRoot(stdout, stderr).execute(args, stdout, stderr)
}
