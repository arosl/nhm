// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/spawn.go
// (SpawnOutput, SpawnedAgent), internal/robot/spawn_pacing.go
// (SpawnStaggerPlan, SpawnStaggerSlot) and internal/pressure/pressure.go
// (SpawnAdmission) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: only the fields nhm fills; nhm's admission
// reasons and warnings in SpawnAdmission; nhm's extras under "_nhm".

package robot

// Spawn admission decisions, ntm's.
const (
	SpawnAdmit  = "admit"
	SpawnRefuse = "refuse"
)

// SpawnAdmission is the admission before a spawn.
type SpawnAdmission struct {
	Serialized      bool     `json:"serialized"`
	Decision        string   `json:"decision"`
	Reason          string   `json:"reason"`
	Hint            string   `json:"hint,omitempty"`
	Session         string   `json:"session,omitempty"`
	RequestedAgents int      `json:"requested_agents"`
	Reasons         []string `json:"reasons,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

// SpawnedAgent is one agent of a spawn.
type SpawnedAgent struct {
	Pane          string `json:"pane"`
	PaneID        string `json:"pane_id,omitempty"`
	AgentMailName string `json:"agent_mail_name,omitempty"`
	Name          string `json:"name,omitempty"`
	Type          string `json:"type"`
	Variant       string `json:"variant,omitempty"`
	Title         string `json:"title"`
	Ready         bool   `json:"ready"`
	StartupMs     int64  `json:"startup_ms"`
	Error         string `json:"error,omitempty"`
}

// SpawnStaggerSlot is one agent's place in the start schedule.
type SpawnStaggerSlot struct {
	Pane      string `json:"pane"`
	AgentType string `json:"agent_type"`
	Order     int    `json:"order"`
	DelayMs   int64  `json:"delay_ms"`
}

// SpawnStaggerPlan is the start pacing.
type SpawnStaggerPlan struct {
	Mode       string             `json:"mode"`
	IntervalMs int64              `json:"interval_ms"`
	Schedule   []SpawnStaggerSlot `json:"schedule"`
}

// SpawnOutput is --robot-spawn's answer.
type SpawnOutput struct {
	RobotResponse
	Session             string            `json:"session"`
	CreatedAt           string            `json:"created_at"`
	WorkingDir          string            `json:"working_dir"`
	EffectiveProjectKey string            `json:"effective_project_key,omitempty"`
	Agents              []SpawnedAgent    `json:"agents"`
	Layout              string            `json:"layout"`
	TotalStartupMs      int64             `json:"total_startup_ms"`
	DryRun              bool              `json:"dry_run,omitempty"`
	WouldCreate         []SpawnedAgent    `json:"would_create,omitempty"`
	Assignments         []any             `json:"assignments"`
	Stagger             *SpawnStaggerPlan `json:"stagger,omitempty"`
	Admission           *SpawnAdmission   `json:"admission,omitempty"`
	MonitorStarted      bool              `json:"monitor_started"`
	NHM                 any               `json:"_nhm,omitempty"`
}
