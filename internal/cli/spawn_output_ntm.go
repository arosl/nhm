// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/output/schema.go at commit 9d5ed977: TimestampedResponse,
// NewTimestamped, PaneResponse, AgentCountsResponse, StaggerConfig,
// RecoverySpawnStatus, SpawnResponse and AddResponse;
// internal/spawnidentity/coordinator.go at the same commit:
// AgentMailSpawnStatus; internal/cli/add.go at the same commit:
// agentLifecycleFailureResponse and newAgentLifecycleFailureResponse;
// internal/cli/create.go at the same commit: incrementAgentCounts;
// internal/output/progress.go at the same commit: Steps;
// internal/output/success.go at the same commit: Suggestion,
// PrintSuccessFooter, SpawnSuggestions and AddSuggestions; and
// internal/cli/spawn.go at the same commit: the agent-name line of
// sendInitPromptToReadyAgentsWith, as agentNamePrompt; see LICENSE in
// this directory and PROVENANCE.md. Local changes (nhm-2p6.4.8,
// nhm-2p6.4.9, nhm-2p6.4.11): this header; package and imports;
// unexported names;
// SpawnResponse, AddResponse and the failure response carry nhm's own
// facts under "_nhm"; AddResponse holds the admission that ntm's add
// writes beside it (add.go 1193); no seat skips, persona prompt source,
// context or pane age, which nhm does not have; the counts take ntm's type
// strings; the failure response's codes are the ones nhm's spawn can
// give; the steps and the footer print without colour (as ntm does with
// NO_COLOR); the footer prints only to a terminal file, as ntm's spawn
// (which always writes it to stdout); the suggestions name nhm.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/arosl/nhm/internal/robot"
)

// timestampedResponse is the timestamp every ntm response carries.
type timestampedResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
}

// newTimestamped creates a timestamped response base.
func newTimestamped() timestampedResponse {
	return timestampedResponse{GeneratedAt: time.Now().UTC()}
}

// paneResponse is the standard format for pane-related output.
type paneResponse struct {
	PaneID        string `json:"pane_id"`
	PaneTarget    string `json:"pane_target"`
	WindowIndex   int    `json:"window_index"`
	Index         int    `json:"index"`
	Title         string `json:"title"`
	Type          string `json:"type"`              // claude, codex, gemini, user
	Variant       string `json:"variant,omitempty"` // model alias or persona name
	Active        bool   `json:"active,omitempty"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	Command       string `json:"command,omitempty"`
	Status        string `json:"status,omitempty"`          // idle, working, error
	PromptDelayMs int64  `json:"prompt_delay_ms,omitempty"` // Stagger delay in milliseconds
}

// agentCountsResponse is the standard format for agent counts. Real agent
// types always emit even at 0 so consumers see a stable schema; only the
// metadata categories (user, other) use omitempty.
type agentCountsResponse struct {
	Claude      int `json:"claude"`
	Codex       int `json:"codex"`
	Gemini      int `json:"gemini"`
	Antigravity int `json:"antigravity"`
	Grok        int `json:"grok"`
	Ollama      int `json:"ollama"`
	Cursor      int `json:"cursor"`
	Windsurf    int `json:"windsurf"`
	Aider       int `json:"aider"`
	Opencode    int `json:"opencode"`
	Omp         int `json:"omp"`
	User        int `json:"user,omitempty"`
	Other       int `json:"other,omitempty"`
	Total       int `json:"total"`
}

// staggerConfig represents stagger settings in spawn response.
type staggerConfig struct {
	Enabled    bool   `json:"enabled"`
	Mode       string `json:"mode,omitempty"` // fixed, smart, or legacy (bare --stagger)
	IntervalMs int64  `json:"interval_ms,omitempty"`
}

// recoverySpawnStatus reports whether configured session recovery produced
// prompt content and which optional sources degraded.
type recoverySpawnStatus struct {
	Enabled   bool     `json:"enabled"`
	Applied   bool     `json:"applied"`
	Partial   bool     `json:"partial"`
	ErrorCode string   `json:"error_code,omitempty"`
	Warnings  []string `json:"warnings"`
}

// agentMailSpawnStatus reports the Agent Mail identities of a spawn.
type agentMailSpawnStatus struct {
	Available         bool              `json:"available"`
	ProjectRegistered bool              `json:"project_registered"`
	AgentsRegistered  int               `json:"agents_registered"`
	AgentsFailed      int               `json:"agents_failed"`
	AgentMap          map[string]string `json:"agent_map,omitempty"` // stable pane id -> agent name
	SessionAgent      string            `json:"session_agent,omitempty"`
}

// spawnResponse is the output format for spawn command (with agents).
type spawnResponse struct {
	timestampedResponse
	Session          string                `json:"session"`
	Created          bool                  `json:"created"`
	WorkingDirectory string                `json:"working_directory,omitempty"`
	Panes            []paneResponse        `json:"panes"`
	AgentCounts      agentCountsResponse   `json:"agent_counts"`
	Stagger          *staggerConfig        `json:"stagger,omitempty"`
	AgentMail        *agentMailSpawnStatus `json:"agent_mail,omitempty"`
	Recovery         *recoverySpawnStatus  `json:"recovery,omitempty"`
	NHM              any                   `json:"_nhm,omitempty"`
}

// addResponse is the output format for add command (adding agents to
// session), with the admission ntm's add writes beside it.
type addResponse struct {
	timestampedResponse
	Session          string                `json:"session"`
	AddedClaude      int                   `json:"added_claude"`
	AddedCodex       int                   `json:"added_codex"`
	AddedGemini      int                   `json:"added_gemini"`
	AddedAntigravity int                   `json:"added_antigravity"`
	AddedGrok        int                   `json:"added_grok"`
	AddedOllama      int                   `json:"added_ollama"`
	AddedCursor      int                   `json:"added_cursor"`
	AddedWindsurf    int                   `json:"added_windsurf"`
	AddedAider       int                   `json:"added_aider"`
	AddedOpencode    int                   `json:"added_opencode"`
	AddedOmp         int                   `json:"added_omp"`
	TotalAdded       int                   `json:"total_added"`
	NewPanes         []paneResponse        `json:"new_panes,omitempty"`
	AgentMail        *agentMailSpawnStatus `json:"agent_mail,omitempty"`
	Admission        *robot.SpawnAdmission `json:"admission,omitempty"`
	NHM              any                   `json:"_nhm,omitempty"`
}

type agentLifecycleFailureResponse struct {
	timestampedResponse
	Success         bool                  `json:"success"`
	Session         string                `json:"session,omitempty"`
	Error           string                `json:"error"`
	ErrorCode       string                `json:"error_code"`
	Code            string                `json:"code"`
	PartialMutation bool                  `json:"partial_mutation"`
	SessionMayExist bool                  `json:"session_may_exist"`
	AffectedPaneIDs []string              `json:"affected_pane_ids"`
	Admission       *robot.SpawnAdmission `json:"admission,omitempty"`
	NHM             any                   `json:"_nhm,omitempty"`
}

func newAgentLifecycleFailureResponse(err error, code, session string, partialMutation, sessionMayExist bool,
	affectedPaneIDs []string, admission *robot.SpawnAdmission) agentLifecycleFailureResponse {
	normalizedPaneIDs := make([]string, 0, len(affectedPaneIDs))
	seenPaneIDs := make(map[string]struct{}, len(affectedPaneIDs))
	for _, paneID := range affectedPaneIDs {
		paneID = strings.TrimSpace(paneID)
		if paneID == "" {
			continue
		}
		if _, seen := seenPaneIDs[paneID]; seen {
			continue
		}
		seenPaneIDs[paneID] = struct{}{}
		normalizedPaneIDs = append(normalizedPaneIDs, paneID)
	}
	return agentLifecycleFailureResponse{
		timestampedResponse: newTimestamped(),
		Success:             false,
		Session:             session,
		Error:               err.Error(),
		ErrorCode:           code,
		Code:                code,
		PartialMutation:     partialMutation,
		SessionMayExist:     sessionMayExist,
		AffectedPaneIDs:     normalizedPaneIDs,
		Admission:           admission,
	}
}

// incrementAgentCounts counts one pane of an ntm agent type.
func incrementAgentCounts(counts *agentCountsResponse, t string) {
	if counts == nil {
		return
	}
	switch t {
	case "claude":
		counts.Claude++
	case "codex":
		counts.Codex++
	case "gemini":
		counts.Gemini++
	case "antigravity":
		counts.Antigravity++
	case "grok":
		counts.Grok++
	case "ollama":
		counts.Ollama++
	case "cursor":
		counts.Cursor++
	case "windsurf":
		counts.Windsurf++
	case "aider":
		counts.Aider++
	case "oc", "opencode":
		counts.Opencode++
	case "omp":
		counts.Omp++
	case "user":
		counts.User++
	default:
		counts.Other++
	}
	counts.Total++
}

// steps manages a sequence of steps for long operations: "  name... ✓".
type steps struct {
	w        io.Writer
	running  bool
	useColor bool
	indent   string
}

func newSteps(w io.Writer) *steps {
	useColor := false
	if f, ok := w.(*os.File); ok {
		useColor = term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
	}
	return &steps{w: w, useColor: useColor, indent: "  "}
}

// start begins a new named step, auto-completing any running step.
func (s *steps) start(name string) *steps {
	if s.running {
		s.done()
	}
	s.running = true
	fmt.Fprintf(s.w, "%s%s... ", s.indent, name)
	return s
}

// done marks the current step as successful: "✓" (or "[OK]" without
// colour).
func (s *steps) done() *steps { return s.finish("✓", "OK") }

// fail marks the current step as failed: "✗" (or "[FAIL]").
func (s *steps) fail() *steps { return s.finish("✗", "FAIL") }

func (s *steps) finish(icon, text string) *steps {
	if !s.running {
		return s
	}
	s.running = false
	if s.useColor {
		fmt.Fprintln(s.w, icon)
	} else {
		fmt.Fprintf(s.w, "[%s]\n", text)
	}
	return s
}

// suggestion is a "what next" command suggestion.
type suggestion struct {
	Command     string
	Description string
}

// printSuccessFooter prints a "What's next?" footer to w, only when w is a
// terminal: ntm's spawn writes it to stdout, which it skips when piped or
// redirected.
func printSuccessFooter(w io.Writer, suggestions ...suggestion) {
	if len(suggestions) == 0 {
		return
	}
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "What's next?")
	for _, s := range suggestions {
		fmt.Fprintf(w, "  %s  # %s\n", s.Command, s.Description)
	}
	fmt.Fprintln(w)
}

// addSuggestions returns suggestions for after adding agents.
func addSuggestions(session string) []suggestion {
	return []suggestion{
		{Command: fmt.Sprintf("nhm status %s", session), Description: "Check session status"},
		{Command: fmt.Sprintf("nhm send %s", session), Description: "Send prompt to agents"},
		{Command: fmt.Sprintf("nhm dashboard %s", session), Description: "Live status overview"},
	}
}

// spawnSuggestions returns suggestions for after spawning a session.
func spawnSuggestions(session string) []suggestion {
	return []suggestion{
		{Command: fmt.Sprintf("nhm attach %s", session), Description: "Connect to session"},
		{Command: fmt.Sprintf("nhm dashboard %s", session), Description: "Live status overview"},
		{Command: "nhm palette", Description: "Quick command launcher"},
	}
}

// agentNamePrompt is the init prompt with --with-agent-name: the agent's
// name ahead of prompt (ntm#138).
func agentNamePrompt(name, prompt string) string {
	return fmt.Sprintf(
		"You are agent `%s`. Use this name when registering with Agent Mail or referring to yourself.\n\n%s",
		name, prompt,
	)
}
