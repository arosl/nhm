// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/docs.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: DocsOutput, its parts, the topics and GetDocs's answers
// are ntm's; the text is nhm's (herdr and nhm's commands, as the tutorial
// is), and the commands topic comes from nhm's inventory, so it names only
// the robot flags nhm answers; GetDocs takes the version, the format and
// those sections, which ntm reads from its globals (nhm-2p6.2.7).

package robot

import "time"

// DocsOutput is --robot-docs. Its Version is the build's, which hides the
// envelope's version, as in ntm.
type DocsOutput struct {
	RobotResponse
	Version       string       `json:"version"`
	SchemaVersion string       `json:"schema_version"`
	Topic         string       `json:"topic"`
	Topics        []DocsTopic  `json:"topics,omitempty"`
	Content       *DocsContent `json:"content,omitempty"`
}

// DocsTopic represents an available documentation topic
type DocsTopic struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DocsContent represents the content for a specific topic
type DocsContent struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Sections    []DocsSection  `json:"sections,omitempty"`
	Examples    []DocsExample  `json:"examples,omitempty"`
	ExitCodes   []DocsExitCode `json:"exit_codes,omitempty"`
}

// DocsSection represents a documentation section
type DocsSection struct {
	Heading string `json:"heading"`
	Body    string `json:"body"`
}

// DocsExample represents a documentation example
type DocsExample struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	Notes       string `json:"notes,omitempty"`
}

// DocsExitCode represents an exit code documentation entry
type DocsExitCode struct {
	Code        int    `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Recoverable bool   `json:"recoverable"`
}

// CurrentSchemaVersion is the current schema version for robot docs
const CurrentSchemaVersion = "1.0.0"

// availableTopics lists all documentation topics
var availableTopics = []DocsTopic{
	{Name: "quickstart", Description: "Getting started with robot mode for AI agents"},
	{Name: "commands", Description: "Complete command reference with parameters and flags"},
	{Name: "examples", Description: "Common workflow examples and patterns"},
	{Name: "exit-codes", Description: "Exit code conventions and error handling"},
}

// GetDocs is ntm's GetDocs: the topic index for an empty topic, else the
// topic's content; an unknown topic is ntm's INVALID_FLAG answer, the
// docs fields kept. commands are the sections of the commands topic.
func GetDocs(topic, version string, commands []DocsSection, format string, now time.Time) *DocsOutput {
	output := &DocsOutput{
		RobotResponse: NewResponse(true, format, now),
		Version:       version,
		SchemaVersion: CurrentSchemaVersion,
		Topic:         topic,
	}
	if topic == "" {
		output.Topics = availableTopics
		return output
	}
	var content *DocsContent
	switch topic {
	case "quickstart":
		content = quickstartContent()
	case "commands":
		content = &DocsContent{Title: "Robot Mode Commands",
			Description: "Complete reference of all robot mode commands and their parameters", Sections: commands}
	case "examples":
		content = examplesContent()
	case "exit-codes":
		content = exitCodesContent()
	}
	if content == nil {
		// ntm's NewErrorResponse with no error: the code, lowered, is the error.
		output.RobotResponse = NewResponse(false, format, now)
		output.Error, output.ErrorCode = "invalid flag", ErrCodeInvalidFlag
		output.Hint = "unknown topic: " + topic + ". Use --robot-docs without a topic to see available topics."
		return output
	}
	output.Content = content
	return output
}

func quickstartContent() *DocsContent {
	return &DocsContent{
		Title:       "Robot Mode Quickstart",
		Description: "Getting started with nhm robot mode for AI agent integration",
		Sections: []DocsSection{
			{
				Heading: "Overview",
				Body: `Robot mode provides a JSON API for AI agents to orchestrate coding agents in herdr workspaces.
A robot command prints one machine-readable answer on stdout and leaves stderr empty.
Command failures and their hints are inside the answer.`,
			},
			{
				Heading: "API Design Principles",
				Body: `1. Global commands use bool flags: --robot-status, --robot-snapshot
2. Session-scoped commands use =SESSION syntax: --robot-send=myproj
3. Modifiers use unprefixed flags: --lines, --panes, --type, --limit, --offset
4. Output is JSON by default, with TOON format for token efficiency`,
			},
			{
				Heading: "First Steps",
				Body: `1. Check system state: nhm --robot-status
2. Start agents: nhm --robot-spawn=myproject --spawn-dir=/path/to/myproject --spawn-cc=2 --spawn-wait
3. Send a prompt: nhm --robot-send=myproject --type=claude --msg="implement auth"
4. Wait for the agents: nhm --robot-wait=myproject --condition=idle
5. Capture output: nhm --robot-tail=myproject --lines=100`,
			},
			{
				Heading: "Discovery",
				Body: `Use --robot-capabilities to see every ntm robot flag and whether this nhm answers it.
Use --robot-docs=<topic> for topic-scoped JSON documentation.
Start with --robot-status to understand current state.`,
			},
		},
		Examples: []DocsExample{
			{
				Name:        "basic_session",
				Description: "Start a session with Claude agents",
				Command:     "nhm --robot-spawn=myproject --spawn-dir=/path/to/myproject --spawn-cc=2 --spawn-wait",
				Notes:       "--spawn-wait waits until the agents are ready",
			},
			{
				Name:        "send_prompt",
				Description: "Send a prompt to the session's Claude agents",
				Command:     "nhm --robot-send=myproject --type=claude --msg='Fix the authentication bug'",
				Notes:       "The answer names the operation; --robot-send-receipt=OP reads its receipts",
			},
			{
				Name:        "check_state",
				Description: "Get current system state",
				Command:     "nhm --robot-status",
				Notes:       "Returns a cheap summary, session headers, and degraded-source indicators",
			},
		},
	}
}

func examplesContent() *DocsContent {
	return &DocsContent{
		Title:       "Robot Mode Examples",
		Description: "Common workflow examples and usage patterns",
		Examples: []DocsExample{
			{
				Name:        "single_agent",
				Description: "Start a session with a single Claude agent",
				Command:     "nhm --robot-spawn=myproject --spawn-dir=/path/to/myproject --spawn-cc=1 --spawn-wait",
				Notes:       "Best for focused, single-task work",
			},
			{
				Name:        "multi_agent",
				Description: "Start a session with several agent kinds",
				Command:     "nhm --robot-spawn=myproject --spawn-dir=/path/to/myproject --spawn-cc=2 --spawn-cod=1",
				Notes:       "Useful for parallel work distribution",
			},
			{
				Name:        "send_to_type",
				Description: "Send a prompt to one agent kind",
				Command:     "nhm --robot-send=proj --type=codex --msg='Review code'",
				Notes:       "--type takes ntm's names: cc, cod, claude, codex …",
			},
			{
				Name:        "send_to_panes",
				Description: "Send a prompt to chosen panes",
				Command:     "nhm --robot-send=proj --panes=0.1 --msg='Debug issue'",
				Notes:       "--panes takes the slot N.M or a herdr pane id",
			},
			{
				Name:        "capture_output",
				Description: "Capture recent pane output",
				Command:     "nhm --robot-tail=proj --lines=100",
				Notes:       "Useful for checking progress",
			},
			{
				Name:        "wait_for_idle",
				Description: "Wait for all agents to become idle",
				Command:     "nhm --robot-wait=proj --condition=idle --timeout=5m",
				Notes:       "Blocks until condition met or timeout",
			},
			{
				Name:        "page_sessions",
				Description: "Read the sessions a page at a time",
				Command:     "nhm --robot-status --robot-limit=10 --robot-offset=10",
				Notes:       "_agent_hints.next_offset names the next page",
			},
			{
				Name:        "discover",
				Description: "List the robot flags and what this nhm answers",
				Command:     "nhm --robot-capabilities",
				Notes:       "A flag nhm does not answer is marked unavailable, with its reason",
			},
		},
	}
}

func exitCodesContent() *DocsContent {
	return &DocsContent{
		Title:       "Exit Codes",
		Description: "Exit code conventions for robot mode commands",
		Sections: []DocsSection{
			{
				Heading: "Overview",
				Body: `Robot mode uses exactly three process exit codes.
Exit code 0 indicates success, 1 indicates an error, and 2 indicates unavailable functionality.
All errors include a JSON response with error_code and error fields for programmatic handling.`,
			},
			{
				Heading: "Error Handling",
				Body: `When a command fails:
1. Exit code is non-zero
2. JSON output includes success=false
3. error_code provides machine-readable category
4. error provides human-readable message
5. Recoverable errors may include suggestions in _agent_hints`,
			},
		},
		ExitCodes: []DocsExitCode{
			{Code: 0, Name: "SUCCESS", Description: "Command completed successfully", Recoverable: true},
			{Code: 1, Name: "ERROR", Description: "Command failed; inspect error_code, error, and hint", Recoverable: true},
			{Code: 2, Name: "UNAVAILABLE", Description: "Requested functionality is not implemented or unavailable", Recoverable: true},
		},
	}
}
