// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/memory.go
// (newMemoryCmd, newMemoryServeCmd, runMemoryServe, newMemoryContextCmd,
// newMemoryOutcomeCmd, newMemoryPrivacyCmd and its five sub-verbs,
// runCMPrivacyCommand) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: the supervisor, the daemon client and the
// privacy pass-through are internal/integrations/cm's; the pid and log
// files are under the cwd's .nhm/ (the operator's ruling, 2026-10-24: nhm
// keeps its own paths); the texts name nhm; the output goes to cobra's
// writers; tests can replace the cm command and the serve context.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/integrations/cm"
)

// memoryUsage is `nhm memory`'s help: ntm's memory verbs over CM (the
// operator's ruling, 2026-10-24: ntm's behaviour; nhm's old
// `memory <query>` preview goes to cass preview, nhm-2p6.14).
const memoryUsage = `Usage: nhm memory serve [--port N]
       nhm memory context <task>
       nhm memory outcome success|failure|partial [--rules ID,...]
       nhm memory privacy status|enable [agents...]|disable|allow <agent>|deny <agent>

ntm's memory verbs over CASS Memory (CM, the cm CLI).

serve runs cm serve in the foreground under nhm's supervision, on --port
(default 8200; a busy port refuses). It probes the daemon with an MCP
round-trip, restarts it with backoff (at most 5 times), and keeps its pid
file in the cwd's .nhm/pids and its log in .nhm/logs. Ctrl-C stops it.
nhm spawn does not start it.

context prints the daemon's rules and history for a task, as JSON.
outcome records the outcome of a task for the rules it used, and prints
nothing. Both need a memory serve that runs in the cwd.

privacy runs cm privacy in the cwd, with --json, and passes its output
through: status, enable (optionally the agents to allow), disable, allow
and deny one agent.
`

// memoryCMCommand is the cm command that memory serve supervises, and
// memoryServeContext the context it stops on; tests replace them.
var (
	memoryCMCommand    = []string{"cm"}
	memoryServeContext = context.Background
	memoryServePoll    = 500 * time.Millisecond
)

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Interact with CASS Memory (cm) system",
	}
	cmd.AddCommand(newMemoryServeCmd(), newMemoryContextCmd(), newMemoryOutcomeCmd(), newMemoryPrivacyCmd())
	return cmd
}

func newMemoryServeCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the CASS Memory (cm) daemon under nhm supervision (foreground)",
		Long: `Run the CASS Memory (cm) daemon in the foreground under nhm's supervisor.

The supervisor launches 'cm serve', restarts it on crashes with backoff, and
health-checks it with an MCP JSON-RPC round-trip at the daemon root (cm speaks
MCP only; it has no REST /health endpoint). State transitions are printed here
and appended to .nhm/logs/. Stop with Ctrl-C.

Note: 'nhm spawn' does NOT start the memory daemon. This is the one obvious
way to run cm by hand.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryServe(memoryServeContext(), cmd.OutOrStdout(), port)
		},
	}
	cmd.Flags().IntVar(&port, "port", cm.DefaultServePort, "Port for the cm daemon to listen on")
	return cmd
}

// runMemoryServe supervises the cm daemon in the foreground until ctx
// ends (Ctrl-C) or the daemon's recovery fails for good.
func runMemoryServe(ctx context.Context, out io.Writer, port int) error {
	// A missing cm fails at once, never as a silent loop of launches.
	if _, err := exec.LookPath(memoryCMCommand[0]); err != nil {
		return fmt.Errorf("cm binary not found in PATH: %w\ninstall CASS Memory first (https://github.com/Dicklesworthstone/cass_memory), then re-run 'nhm memory serve'", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	sessionID := fmt.Sprintf("memory-serve-%d", os.Getpid())
	srv, err := cm.NewServer(cm.ServeConfig{ProjectDir: dir, SessionID: sessionID, Port: port,
		Command: memoryCMCommand, MaxRestarts: cm.DefaultMaxRestarts})
	if err != nil {
		return fmt.Errorf("initialize supervisor: %w", err)
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("start cm daemon: %w", err)
	}
	defer srv.Shutdown()
	st := srv.Status()
	fmt.Fprintf(out, "Supervising cm daemon (pid=%d port=%d state=%s)\n", st.PID, st.Port, st.State)
	fmt.Fprintf(out, "Logs: %s\n", cm.ServeLogPath(dir, sessionID))
	fmt.Fprintln(out, "Press Ctrl-C to stop.")
	last := st.State
	ticker := time.NewTicker(memoryServePoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "Shutting down cm daemon...")
			if err := srv.Shutdown(); err != nil {
				return fmt.Errorf("shutdown: %w", err)
			}
			fmt.Fprintln(out, "cm daemon stopped.")
			return nil
		case <-ticker.C:
			st := srv.Status()
			if st.State != last {
				fmt.Fprintf(out, "cm daemon state: %s -> %s (pid=%d restarts=%d)\n", last, st.State, st.PID, st.Restarts)
				last = st.State
			}
			if st.State == cm.StateFailed {
				// A fence or record failure stops recovery before the
				// restart budget is spent: never wait in that state.
				if st.LastError != "" {
					return fmt.Errorf("cm daemon recovery stopped: %s; see .nhm/logs/cm-%s.log", st.LastError, sessionID)
				}
				return fmt.Errorf("cm daemon failed permanently after %d restart attempts; see .nhm/logs/cm-%s.log", st.Restarts-1, sessionID)
			}
		}
	}
}

// memoryDaemon is the client of the memory daemon that runs in the cwd.
func memoryDaemon() (*cm.Daemon, string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}
	sessionID, err := cm.FindSessionID(dir)
	if err != nil {
		return nil, "", err
	}
	d, err := cm.NewDaemon(dir, sessionID)
	return d, dir, err
}

func newMemoryContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context <task>",
		Short: "Get relevant context for a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, dir, err := memoryDaemon()
			if err != nil {
				return err
			}
			// The cwd scopes the rules, so projects with the same base
			// name share none.
			result, err := d.GetContext(context.Background(), args[0], dir)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		},
	}
}

func newMemoryOutcomeCmd() *cobra.Command {
	var rules []string
	cmd := &cobra.Command{
		Use:   "outcome <success|failure|partial>",
		Short: "Record task outcome feedback",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case cm.OutcomeSuccess, cm.OutcomeFailure, cm.OutcomePartial:
			default:
				return fmt.Errorf("invalid status: %s", args[0])
			}
			d, _, err := memoryDaemon()
			if err != nil {
				return err
			}
			return d.RecordOutcome(context.Background(), args[0], rules)
		},
	}
	cmd.Flags().StringSliceVar(&rules, "rules", nil, "Comma-separated list of rule IDs applied")
	return cmd
}

func newMemoryPrivacyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "privacy",
		Short: "Manage cross-agent privacy settings",
		Long: `Manage privacy controls for cross-agent enrichment in CASS Memory.

Cross-agent enrichment allows agents to share relevant context and learned rules
with each other. By default, this is disabled for privacy. You can enable it
and control which agents can participate.

Examples:
  nhm memory privacy status           # Show privacy settings
  nhm memory privacy enable           # Enable cross-agent enrichment
  nhm memory privacy disable          # Disable cross-agent enrichment
  nhm memory privacy allow GreenLake  # Allow specific agent
  nhm memory privacy deny BlueCat     # Remove agent from allowlist`,
	}
	privacy := func(use, short string, nargs cobra.PositionalArgs, cmArgs func([]string) []string) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  nargs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runCMPrivacy(cmd, cmArgs(args)...)
			},
		}
	}
	enable := privacy("enable [agents...]", "Enable cross-agent enrichment", nil,
		func(a []string) []string { return append([]string{"enable", "--json"}, a...) })
	enable.Long = `Enable cross-agent enrichment. Optionally specify agents to auto-allow.
This requires explicit consent as it allows sharing data between agents.`
	cmd.AddCommand(
		privacy("status", "Show cross-agent privacy settings", nil,
			func([]string) []string { return []string{"status", "--json"} }),
		enable,
		privacy("disable", "Disable cross-agent enrichment", nil,
			func([]string) []string { return []string{"disable", "--json"} }),
		privacy("allow <agent>", "Allow a specific agent for cross-agent enrichment", cobra.ExactArgs(1),
			func(a []string) []string { return []string{"allow", a[0], "--json"} }),
		privacy("deny <agent>", "Remove an agent from the cross-agent allowlist", cobra.ExactArgs(1),
			func(a []string) []string { return []string{"deny", a[0], "--json"} }),
	)
	return cmd
}

// runCMPrivacy runs `cm privacy <args>` in the cwd; cm's output passes
// through, and cm's failure is "exit status N", as ntm's.
func runCMPrivacy(cmd *cobra.Command, args ...string) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	return cm.Privacy(context.Background(), memoryCMCommand[0], dir, cmd.OutOrStdout(), cmd.ErrOrStderr(), args...)
}
