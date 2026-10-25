// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/logs.go (PaneLogs, LogsOutput, LogsSummary,
// DefaultLogsLimit), internal/robot/robot.go (PaneAddressEntry,
// PaneAddressOutput) and internal/robot/watch_bead.go (BeadMention,
// WatchBeadOutput) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: only the output types; the session reads
// that fill them are nhm's, on herdr's panes (internal/cli).

package robot

import "time"

// PaneLogs contains logs for a single pane.
type PaneLogs struct {
	Pane       int       `json:"pane"`
	AgentType  string    `json:"agent_type"`
	Lines      []string  `json:"lines"`
	LineCount  int       `json:"line_count"`
	Truncated  bool      `json:"truncated"`
	CapturedAt time.Time `json:"captured_at"`
}

// LogsOutput is the response format for --robot-logs=SESSION.
type LogsOutput struct {
	RobotResponse
	Session    string      `json:"session"`
	CapturedAt time.Time   `json:"captured_at"`
	Panes      []PaneLogs  `json:"panes"`
	Summary    LogsSummary `json:"summary"`
}

// LogsSummary contains aggregate log statistics.
type LogsSummary struct {
	TotalPanes     int `json:"total_panes"`
	TotalLines     int `json:"total_lines"`
	TruncatedPanes int `json:"truncated_panes"`
	FilteredLines  int `json:"filtered_lines,omitempty"`
}

// DefaultLogsLimit is the default max lines per pane.
const DefaultLogsLimit = 100

// PaneAddressEntry is one pane's complete addressing card: the canonical
// selector send and tail accept, the stable pane id, the physical
// topology address, and a ready-to-paste target.
type PaneAddressEntry struct {
	Selector    string `json:"selector"`
	PaneID      string `json:"pane_id"`
	WindowIndex int    `json:"window_index"`
	PaneIndex   int    `json:"pane_index"`
	NTMIndex    int    `json:"ntm_index,omitempty"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	TmuxTarget  string `json:"tmux_target"`
}

// PaneAddressOutput is the --robot-pane-address response.
type PaneAddressOutput struct {
	RobotResponse
	Session     string             `json:"session"`
	MultiWindow bool               `json:"multi_window"`
	Panes       []PaneAddressEntry `json:"panes"`
}

// BeadMention describes a bead mention found in pane output.
type BeadMention struct {
	Pane int `json:"pane"`
	// PaneRef is the unambiguous address of the pane: "window.pane" on a
	// multi-window session, the bare pane index otherwise.
	PaneRef   string    `json:"pane_ref,omitempty"`
	PaneID    string    `json:"pane_id,omitempty"`
	AgentType string    `json:"agent_type"`
	Line      string    `json:"line"`
	LineNum   int       `json:"line_num"`
	Timestamp time.Time `json:"timestamp"`
}

// WatchBeadOutput is the response for --robot-watch-bead=SESSION.
type WatchBeadOutput struct {
	RobotResponse
	Session      string        `json:"session"`
	BeadID       string        `json:"bead_id"`
	CheckedAt    time.Time     `json:"checked_at"`
	Interval     string        `json:"interval"`
	PanesScanned int           `json:"panes_scanned"`
	Mentions     []BeadMention `json:"mentions"`
	BeadStatus   string        `json:"bead_status"`
	StatusError  string        `json:"status_error,omitempty"`
}
