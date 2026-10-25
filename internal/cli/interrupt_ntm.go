// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/send.go (newInterruptCmd's use, args and --tag flag,
// runInterrupt's text and error, buildInterruptResponse),
// internal/output/schema.go (InterruptResponse,
// InterruptPaneResult, ErrorResponse), internal/cli/util.go
// (emitJSONFailureEnvelope) and internal/tmux/session.go
// (InterruptKeyLabel); see LICENSE in this directory and PROVENANCE.md.
// Local changes: the panes are nhm's bound agents and their keys come
// from nhm's kind registry; a pane is its slot's M, or the slot N.M when
// the agents span tabs; nhm's selectors beside --tag; nhm's operation and
// per-agent results go under "_nhm"; the response is built here, not
// through ntm's kernel.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/wait"
)

// interruptFlags are ntm's interrupt flag and nhm's selectors beside it.
type interruptFlags struct {
	tags, names, slots, kinds []string
	includeSelf               bool
	project, opID, socket     string
}

// newInterruptCmd is ntm's interrupt on the shared parser: one session,
// --tag, and the root's --json.
func newInterruptCmd() *cobra.Command {
	var f interruptFlags
	cmd := &cobra.Command{
		Use:   "interrupt <session>",
		Short: "Send Ctrl+C to all agent panes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInterruptNTM(args[0], f, wantJSON(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	fs := cmd.Flags()
	fs.StringSliceVar(&f.tags, "tag", nil, "filter panes by tag (OR logic)")
	fs.StringSliceVar(&f.names, "name", nil, "an Agent Mail or herdr name (repeatable)")
	fs.StringSliceVar(&f.slots, "pane", nil, "an agent's slot N.M (repeatable)")
	fs.StringSliceVar(&f.kinds, "type", nil, "an agent kind: claude, codex, pi, omp (repeatable)")
	fs.BoolVar(&f.includeSelf, "include-self", false, "also interrupt the calling pane")
	fs.StringVar(&f.project, "project", "", "only this project's agents")
	fs.StringVar(&f.opID, "op-id", "", "operation id, for an idempotent retry")
	fs.StringVar(&f.socket, "socket", "", "herdr API socket")
	return cmd
}

// InterruptPaneResult is ntm's output.InterruptPaneResult: one pane's
// outcome.
type InterruptPaneResult struct {
	Pane      string `json:"pane"`
	Index     int    `json:"index"`
	PaneID    string `json:"pane_id"`
	AgentType string `json:"agent_type"`
	Status    string `json:"status"` // "interrupted" | "failed"
	Error     string `json:"error,omitempty"`
}

// InterruptResponse is ntm's output.InterruptResponse, the JSON of
// `nhm interrupt --json`.
type InterruptResponse struct {
	GeneratedAt   time.Time             `json:"generated_at"`
	Success       bool                  `json:"success"`
	Session       string                `json:"session"`
	Interrupted   int                   `json:"interrupted"`
	Failed        int                   `json:"failed,omitempty"`
	Skipped       int                   `json:"skipped,omitempty"`
	TargetedPanes []int                 `json:"targeted_panes,omitempty"`
	Panes         []InterruptPaneResult `json:"panes"`
	Error         string                `json:"error,omitempty"`
	ErrorCode     string                `json:"error_code,omitempty"` // PARTIAL_INTERRUPT | INTERRUPT_FAILED
	NHM           InterruptExtras       `json:"_nhm"`
}

// InterruptExtras are nhm's facts beside ntm's: the operation, whether
// its id was claimed before (then nothing was sent), and each agent's
// result.
type InterruptExtras struct {
	OperationID string                 `json:"operation_id"`
	Existing    bool                   `json:"existing,omitempty"`
	Status      string                 `json:"status"`
	Targets     []wait.InterruptTarget `json:"targets"`
}

// interruptPane is ntm's pane address in the response: the slot's M, or
// the slot N.M when the session's agents span tabs (ntm's PaneTargetKey);
// the herdr pane id for an agent without a slot.
func interruptPane(slot, paneID string, multiTab bool) (string, int) {
	if slot == "" {
		return paneID, 0
	}
	_, m := session.SlotIndexes(slot)
	if multiTab {
		return slot, m
	}
	return strconv.Itoa(m), m
}

// buildInterruptResponse is ntm's buildInterruptResponse over the core's
// result: a sent key is interrupted, anything else failed, and skipped
// counts the agents that --tag left out.
func buildInterruptResponse(sessionName string, res wait.InterruptResult, skipped int, multiTab bool) InterruptResponse {
	resp := InterruptResponse{
		GeneratedAt: time.Now().UTC(), Session: sessionName, Skipped: skipped, Panes: []InterruptPaneResult{},
		NHM: InterruptExtras{OperationID: res.OperationID, Existing: res.Existing, Status: res.Status, Targets: res.Targets},
	}
	if resp.NHM.Targets == nil {
		resp.NHM.Targets = []wait.InterruptTarget{}
	}
	for _, t := range res.Targets {
		key, index := interruptPane(t.Slot, t.PaneID, multiTab)
		r := InterruptPaneResult{Pane: key, Index: index, PaneID: t.PaneID, AgentType: watchAgentType(t.Kind)}
		if t.Status == wait.InterruptSent {
			r.Status = "interrupted"
			resp.Interrupted++
		} else {
			r.Status, r.Error = "failed", t.Reason
			resp.Failed++
		}
		resp.TargetedPanes = append(resp.TargetedPanes, index)
		resp.Panes = append(resp.Panes, r)
	}
	resp.Success = resp.Failed == 0
	if resp.Failed > 0 {
		if resp.Interrupted > 0 {
			resp.ErrorCode = "PARTIAL_INTERRUPT"
			resp.Error = fmt.Sprintf("interrupt partially failed: %d pane(s) interrupted, %d failed", resp.Interrupted, resp.Failed)
		} else {
			resp.ErrorCode = "INTERRUPT_FAILED"
			resp.Error = fmt.Sprintf("interrupt failed on all %d targeted pane(s)", resp.Failed)
		}
	}
	return resp
}

// interruptKeyLabel is ntm's InterruptKeyLabel on a kind's keys: Escape
// for a lone esc, else Ctrl+C, the only other key ntm sends.
func interruptKeyLabel(keys []string) string {
	if len(keys) == 1 && (keys[0] == "esc" || keys[0] == "escape") {
		return "Escape"
	}
	return "Ctrl+C"
}

// printInterruptText is runInterrupt's text: the count, then ntm's error
// when a pane failed. A repeated operation id sent nothing: nhm says so.
func printInterruptText(w io.Writer, resp InterruptResponse) error {
	if resp.NHM.Existing {
		fmt.Fprintf(w, "operation %s was claimed before (%s); nothing sent\n", resp.NHM.OperationID, resp.NHM.Status)
		return nil
	}
	escapes := 0
	var failures []string
	for i, t := range resp.NHM.Targets {
		switch {
		case t.Status == wait.InterruptSent && interruptKeyLabel(t.Keys) == "Escape":
			escapes++
		case t.Status != wait.InterruptSent:
			failures = append(failures, fmt.Sprintf("pane %d: %s", resp.Panes[i].Index, t.Reason))
		}
	}
	if escapes == 0 {
		fmt.Fprintf(w, "Sent Ctrl+C to %d agent pane(s)\n", resp.Interrupted)
	} else {
		fmt.Fprintf(w, "Sent interrupt to %d agent pane(s) (Ctrl+C: %d, Escape: %d)\n", resp.Interrupted, resp.Interrupted-escapes, escapes)
	}
	if len(failures) > 0 {
		return fmt.Errorf("interrupt partially failed (%d interrupted, %d failed): %s", resp.Interrupted, len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// ntmErrorResponse is ntm's output.ErrorResponse, the failure envelope of
// a human verb under --json.
type ntmErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// jsonFailure is ntm's emitJSONFailureEnvelope(output.NewError(...)): the
// envelope on one line on stdout, and exit 1 with nothing on stderr.
func jsonFailure(w io.Writer, err error) error {
	if encErr := json.NewEncoder(w).Encode(ntmErrorResponse{Error: err.Error()}); encErr != nil {
		return errors.Join(encErr, err)
	}
	return verbExit{code: exitError}
}

// writeNTMJSON is ntm's output.PrintJSON: indented, on stdout.
func writeNTMJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
