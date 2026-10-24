// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/robot/spawn.go
// (SpawnOutput, SpawnedAgent, SpawnAssignment), internal/robot/spawn_pacing.go
// (SpawnStaggerPlan, SpawnStaggerSlot) and internal/pressure/pressure.go
// (SpawnAdmission) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: only the fields nhm fills; nhm's admission
// reasons and warnings in SpawnAdmission; nhm's extras under "_nhm";
// assignments is always present ([] when there are none), where ntm omits
// an empty list.

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
	Pane        string `json:"pane"`
	AgentType   string `json:"agent_type"`
	Order       int    `json:"order"`                  // 1-based delivery position
	DelayMs     int64  `json:"delay_ms"`               // offset from the first delivery
	ScheduledAt string `json:"scheduled_at,omitempty"` // planned delivery time (RFC3339Nano); absent until delivery starts
}

// SpawnStaggerPlan reports how --robot-spawn paces prompt delivery
// (--spawn-prompt and --spawn-assign-work prompts) between agents, ntm's.
type SpawnStaggerPlan struct {
	Mode       string             `json:"mode"`               // none, fixed, or smart
	IntervalMs int64              `json:"interval_ms"`        // gap between consecutive agents' deliveries
	Provider   string             `json:"provider,omitempty"` // smart: rate-limit bucket the interval was learned from
	Warning    string             `json:"warning,omitempty"`  // smart: learned history unreadable, built-in delay used
	Schedule   []SpawnStaggerSlot `json:"schedule"`
}

// SpawnAssignment is one agent's --spawn-assign-work outcome, ntm's.
type SpawnAssignment struct {
	Pane              string `json:"pane"`        // Pane reference (e.g., "0.1")
	AgentType         string `json:"agent_type"`  // claude, codex, gemini
	BeadID            string `json:"bead_id"`     // Assigned bead ID
	BeadTitle         string `json:"bead_title"`  // Bead title for context
	Priority          string `json:"priority"`    // Bead priority (P0-P4)
	Claimed           bool   `json:"claimed"`     // Whether bead was successfully claimed (marked in_progress)
	PromptSent        bool   `json:"prompt_sent"` // Whether the work prompt was sent to the agent
	ClaimActor        string `json:"claim_actor,omitempty"`
	IdempotencyKey    string `json:"idempotency_key,omitempty"`
	DispatchReceiptID string `json:"dispatch_receipt_id,omitempty"`
	ReservationIDs    []int  `json:"reservation_ids,omitempty"`
	ClaimError        string `json:"claim_error,omitempty"`   // Error during claim, if any
	PromptError       string `json:"prompt_error,omitempty"`  // Error sending prompt, if any
	AssignReason      string `json:"assign_reason,omitempty"` // Strategy rationale for this pairing (e.g. skill-matched capability score)
	DeliveredAt       string `json:"delivered_at,omitempty"`  // When this run delivered the work prompt (RFC3339Nano); see stagger.schedule
	// CASSInjection and MemoryInjection report the work prompt's enrichment,
	// as on --robot-bulk-assign. Absent when enrichment was not requested or
	// a recorded intent was replayed without re-querying.
	CASSInjection   *CASSInjectionInfo `json:"cass_injection,omitempty"`
	MemoryInjection *CMInjectionInfo   `json:"memory_injection,omitempty"`
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
	Mode                string            `json:"mode,omitempty"`
	Assignments         []SpawnAssignment `json:"assignments"`
	AssignStrategy      string            `json:"assign_strategy,omitempty"`
	Stagger             *SpawnStaggerPlan `json:"stagger,omitempty"`
	Admission           *SpawnAdmission   `json:"admission,omitempty"`
	MonitorStarted      bool              `json:"monitor_started"`
	NHM                 any               `json:"_nhm,omitempty"`
}
