// Package cli parses nhm's command line and renders its output. It holds
// no behaviour of its own; later verbs call internal/core.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/arosl/nhm/internal/herdr/schema"
)

// Build facts, set at link time by the release build:
//
//	-ldflags "-X github.com/arosl/nhm/internal/cli.Version=0.1.0 ..."
var (
	Version = "dev"
	Commit  = "unknown"
)

// NTMCommit is the ntm commit whose grammar and robot types nhm tracks.
const NTMCommit = "9d5ed97796e37fa7b7c89bc3ba4087c144f0102c"

const usage = `nhm: the herdr wrapper of the ACFS flywheel (what ntm is to tmux).

Usage:
  nhm version [--json]   print the version
  nhm spawn <session> [--cc=N[:MODEL]] [--cod=N] … [--prompt T] [--dry-run] [--json]
                         start agents: identity, pane, binding, kickoff
  nhm add <session> …    the same, into an existing workspace
  nhm retire <Name> [--keep-identity] [--cleanup-worktrees] [--dry-run]
                         close an agent's pane, keep its transcript, retire its identity
  nhm recycle <Name> [-t KEY | --prompt T] [--no-handoff]
                         a fresh session in the same pane, with a handoff
  nhm reap [session] [--idle MIN] [--all-workspaces] [--dry-run]
                         retire agents idle past the limit whose work is done
  nhm kill <session> [--yes]
                         retire every agent of a workspace, then close it
  nhm attach [session] [--json]
                         focus a session's workspace (without one: nhm list)
  nhm zoom <session> <slot|pane-id|name|kind> [--json]
                         focus a session's workspace and zoom one pane
  nhm admin restore | admin recover inspect | admin recover resume <binding> (--dry-run|--run)
  nhm admin export --project DIR --to DIR | admin import --from DIR
  nhm admin takeover | admin handback (--dry-run | --yes)
                         move this server's agents from ACFS's Bash to nhm, or back
  nhm new <session>      create the workspace only
  nhm status [session] [--watch] [--json]
                         one row per agent: slot, name, kind, state, mail
  nhm list [--project DIR] [--json]
                         every bound agent across workspaces
  nhm snapshot [session] the machine read model, as JSON
  nhm attention [session] [--watch] [--json|--jsonl]
                         what needs a decision
  nhm read <name> [--lines N] [--source visible|recent] [--raw-to FILE]
                         print an agent's pane
  nhm watch <session> [name|slot ...]
                         follow the agents' output
  nhm wait [session] [selector] [--until idle|done|complete|generating|healthy] [--after-op OP] [--timeout D] [--json]
                         wait until agents reach a state, or a turn completes
  nhm interrupt [session] [selector] [--json]
                         send each agent its kind's interrupt key
  nhm send <session> [prompt] [selector] [-t KEY] [--op-id ID] [--wait] [--dry-run] [--json]
                         send one prompt, once, with a receipt per agent
  nhm memory <query> [--project DIR] [--json]
                         show the CM and CASS block a send would carry
  nhm feedback list|mark|outcomes [--json]
                         the rules each session got; tell CM how they did
  nhm modes list|explain <mode> [--json]
                         ntm's reasoning-mode catalog
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
  nhm mail inbox [--agent NAME] [--keep-unread] [--json]
                         list the agent's unread mail by thread; mark it read
  nhm doctor [--json]    check what nhm needs
  nhm capabilities [--json]
                         what this nhm answers, and why not
  nhm compatibility status [--json]
                         the ntm forms implemented, deferred, waived
  nhm --robot-status | --robot-snapshot | --robot-activity=S |
      --robot-tail=S [--lines N] | --robot-capabilities
                         ntm's robot read flags, ntm's types
  nhm --robot-send=S --panes=P --type=K (--msg=M | --msg-file=-) --op-id=ID |
      --robot-send-receipt=OP | --robot-ack=S [--op-id=ID] [--timeout=D]
                         ntm's robot dispatch flags, ntm's types
  nhm --robot-spawn=S --spawn-dir=R --spawn-cc=N --spawn-cod=N [--spawn-agy=N]
      --spawn-no-user --spawn-safety --spawn-wait [--ready-timeout=Ns] [--dry-run]
                         ntm's robot spawn, ntm's SpawnOutput
  nhm --robot-* … --robot-format=json
                         every robot flag answers JSON (auto is JSON)
  nhm quota [--json]     each plan's windows and the live agents per kind
  nhm limits [session] [--once|--loop] [--dry-run] [--json]
                         the agents a usage limit stopped
  nhm palette | board | menu
                         the prompt palette, the agent board, the nhm menu
  nhm bind [--unbind | --force | --show | --print-client] [--dry-run]
                         herdr keys from [keys] in nhm's config (default prefix+a
                         palette, prefix+d board, prefix+m menu); --unbind opts out
  nhm resolve <name|herdr-name|pane-id|--current> [--json]
                         print a binding and how nhm verified it
  nhm adopt <MailName> [--pane ID] [--handoff-from BINDING] [--json]
                         bind an identity to the agent in a pane, by hand
  nhm admin instance enroll [--session NAME] [--socket PATH] [--json]
                         enroll a herdr server (ACFS's install runs it)
  nhm admin recover reconcile --import-only [--dry-run] [--json]
                         read Bash-spawned agents into bindings
  nhm help               print this text

Each verb prints its own help with --help. The other verbs come with the
next releases.
`

// Run executes nhm with args (without the program name) and returns the
// exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		return runVersion(args[1:], stdout, stderr)
	case "resolve":
		return runResolve(args[1:], stdout, stderr)
	case "adopt":
		return runAdopt(args[1:], stdout, stderr)
	case "retire":
		return runRetire(args[1:], stdout, stderr)
	case "recycle":
		return runRecycle(args[1:], stdout, stderr)
	case "reap":
		return runReap(args[1:], stdout, stderr)
	case "kill":
		return runKill(args[1:], stdout, stderr)
	case "attach":
		return runAttach(args[1:], stdout, stderr)
	case "zoom":
		return runZoom(args[1:], stdout, stderr)
	case "spawn":
		return runSpawn(args[1:], stdout, stderr)
	case "add":
		return runAdd(args[1:], stdout, stderr)
	case "new":
		return runNew(args[1:], stdout, stderr)
	case "admin":
		return runAdmin(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "snapshot":
		return runSnapshot(args[1:], stdout, stderr)
	case "attention":
		return runAttention(args[1:], stdout, stderr)
	case "read":
		return runRead(args[1:], stdout, stderr)
	case "watch":
		return runWatch(args[1:], stdout, stderr)
	case "activity":
		return runActivity(args[1:], stdout, stderr)
	case "health":
		return runHealth(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "wake":
		return runWake(args[1:], stdout, stderr)
	case "maintain":
		return runMaintain(args[1:], stdout, stderr)
	case "mail":
		return runMail(args[1:], stdout, stderr)
	case "capabilities":
		return runCapabilities(args[1:], stdout, stderr)
	case "compatibility":
		return runCompatibility(args[1:], stdout, stderr)
	case "quota":
		return runQuota(args[1:], stdout, stderr)
	case "limits":
		return runLimits(args[1:], stdout, stderr)
	case "palette":
		return runPalette(args[1:], stdout, stderr)
	case "board":
		return runBoard(args[1:], stdout, stderr)
	case "menu":
		return runMenu(args[1:], stdout, stderr)
	case "bind":
		return runBind(args[1:], stdout, stderr)
	case "ui":
		return runUI(args[1:], stdout, stderr)
	case "wait":
		return runWait(args[1:], stdout, stderr)
	case "interrupt":
		return runInterrupt(args[1:], stdout, stderr)
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "memory":
		return runMemory(args[1:], stdout, stderr)
	case "feedback":
		return runFeedback(args[1:], stdout, stderr)
	case "modes":
		return runModes(args[1:], stdout, stderr)
	case "ensemble":
		return runEnsemble(args[1:], stdout, stderr)
	default:
		if strings.HasPrefix(args[0], "--robot-") {
			return runRobot(args, stdout, stderr)
		}
		fmt.Fprintf(stderr, "nhm: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// VersionInfo is the JSON form of `nhm version --json`.
type VersionInfo struct {
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	NTMCommit     string `json:"ntm_commit"`
	HerdrProtocol int    `json:"herdr_protocol"`
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		default:
			fmt.Fprintf(stderr, "nhm version: unknown flag %q\n", a)
			return 2
		}
	}
	s, err := schema.Load()
	if err != nil {
		fmt.Fprintf(stderr, "nhm version: %v\n", err)
		return 1
	}
	info := VersionInfo{Version: Version, Commit: Commit, NTMCommit: NTMCommit, HerdrProtocol: s.Protocol}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "nhm version: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "nhm %s (commit %s, herdr protocol %d, ntm %.8s)\n", info.Version, info.Commit, info.HerdrProtocol, info.NTMCommit)
	return 0
}
