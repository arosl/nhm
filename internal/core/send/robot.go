package send

import (
	"time"

	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/robot"
)

// Admission maps a receipt's submission to ntm's admission state.
func Admission(status string) string {
	switch {
	case status == StatusSubmitted:
		return robot.AdmissionSubmitted
	case Unknown(status):
		return robot.AdmissionUnknown
	}
	return robot.AdmissionRejected
}

// projection is the part of plan-v8 2.12's table that both robot answers
// share: who succeeded, who failed and why, and the operation.
type projection struct {
	targets, successful []string
	failed              []robot.SendError
	admissions          []robot.OperationAdmission
	unknown             bool
}

func project(res Result) projection {
	p := projection{targets: []string{}, successful: []string{}, failed: []robot.SendError{}}
	for _, rc := range res.Targets {
		p.targets = append(p.targets, rc.Key)
		adm := robot.OperationAdmission{Target: rc.Key, State: Admission(rc.Status)}
		switch {
		case rc.Result.Completion != nil && rc.Result.Completion.Observation == ObsReplaced:
			p.failed = append(p.failed, robot.SendError{Pane: rc.Key, Error: "generation_changed"})
		case rc.Status == StatusSubmitted:
			p.successful = append(p.successful, rc.Key)
		case Unknown(rc.Status):
			p.unknown = true
			p.failed = append(p.failed, robot.SendError{Pane: rc.Key, Error: "possibly submitted: " + reason(rc)})
		default:
			adm.Error = reason(rc)
			p.failed = append(p.failed, robot.SendError{Pane: rc.Key, Error: reason(rc)})
		}
		p.admissions = append(p.admissions, adm)
	}
	return p
}

func reason(rc Receipt) string {
	switch {
	case rc.Result.Reason != "" && rc.ErrorCode != "":
		return rc.ErrorCode + ": " + rc.Result.Reason
	case rc.Result.Reason != "":
		return rc.Result.Reason
	case rc.ErrorCode != "":
		return rc.ErrorCode
	}
	return rc.Status
}

func (p projection) success() bool {
	return len(p.targets) > 0 && len(p.successful) == len(p.targets)
}

func operation(res Result, p projection) *robot.OperationInfo {
	if res.OperationID == "" {
		return nil
	}
	status := robot.OperationCompleted
	if res.InProgress {
		status = robot.OperationInProgress
	}
	created := res.CreatedAt
	return &robot.OperationInfo{OperationID: res.OperationID, Kind: Verb, Status: status, Replayed: res.Replayed,
		PayloadSHA256: res.PayloadSHA256, PayloadBytes: res.PayloadBytes, Admissions: p.admissions,
		CreatedAt: created, DispatchStartedAt: &created, CompletedAt: res.CompletedAt}
}

// nhmExtras are nhm's receipt fields under "_nhm": the three separate
// facts of plan-v8 2.12 per target.
func nhmExtras(res Result) map[string]any {
	type target struct {
		Target     string      `json:"target"`
		MailName   string      `json:"mail_name,omitempty"`
		PaneID     string      `json:"pane_id,omitempty"`
		Generation int64       `json:"generation,omitempty"`
		Submission string      `json:"submission"`
		Gate       Gate        `json:"gate,omitempty"`
		Completion *Completion `json:"completion_observation"`
		TaskResult string      `json:"task_result"`
	}
	list := []target{}
	for _, rc := range res.Targets {
		c := rc.Result.Completion
		if c == nil {
			c = &Completion{Observation: ObsNotObserved, Correlation: "none"}
		}
		list = append(list, target{Target: rc.Key, MailName: rc.MailName, PaneID: rc.PaneID, Generation: rc.Generation,
			Submission: rc.Status, Gate: rc.Result.Gate, Completion: c, TaskResult: "unknown"})
	}
	return map[string]any{"operation_id": res.OperationID, "workspace_id": res.WorkspaceID, "targets": list}
}

// SendOutput projects a send onto ntm's --robot-send answer.
func SendOutput(res Result, format string, now time.Time) robot.SendOutput {
	p := project(res)
	out := robot.SendOutput{RobotResponse: robot.NewResponse(true, format, now), Session: res.Session, SentAt: now.UTC(),
		Redaction: robot.RedactionSummary{Mode: "off"}, Warnings: res.Warnings, Targets: p.targets,
		Successful: p.successful, Failed: p.failed, MessagePreview: res.Preview, DryRun: res.DryRun,
		CASSInjection:   &robot.CASSInjectionInfo{Enabled: res.CASSInjection.Enabled, SkippedReason: res.CASSInjection.SkippedReason},
		MemoryInjection: &robot.CMInjectionInfo{Enabled: res.MemoryInjection.Enabled, SkippedReason: res.MemoryInjection.SkippedReason},
		Operation:       operation(res, p), NHM: nhmExtras(res)}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if res.DryRun {
		out.Successful, out.Failed, out.WouldSendTo = []string{}, []robot.SendError{}, []string{}
		for _, rc := range res.Targets {
			if rc.Status == "would_send" {
				out.WouldSendTo = append(out.WouldSendTo, rc.Key)
			} else {
				out.Failed = append(out.Failed, robot.SendError{Pane: rc.Key, Error: reason(rc)})
			}
		}
		out.Operation = nil
		return out
	}
	switch {
	case res.InProgress:
		out.Success, out.ErrorCode = false, robot.ErrCodeOperationInProgress
		out.Error = "the operation is claimed but has no final outcome"
		out.Hint = "nhm --robot-send-receipt=" + res.OperationID + " shows what is known; never resend with this id"
	case p.unknown:
		out.Success, out.ErrorCode = false, robot.ErrCodeOperationOutcomeUnknown
		out.Error = "a prompt may have been submitted; nhm cannot tell"
		out.Hint = "read the pane, then resend with a new --op-id if needed"
	case !p.success():
		out.Success = false
		out.Error = "not every target was submitted"
	}
	return out
}

// ReceiptOutput projects a stored send onto ntm's --robot-send-receipt
// answer.
func ReceiptOutput(res Result, format string, now time.Time) robot.SendReceiptOutput {
	p := project(res)
	out := robot.SendReceiptOutput{RobotResponse: robot.NewResponse(true, format, now), Session: res.Session,
		Warnings: res.Warnings, Operation: operation(res, p), NHM: nhmExtras(res)}
	if res.InProgress {
		out.Hint = "the operation has no final outcome; never resend with this id"
		return out
	}
	sent := res.CreatedAt
	if res.CompletedAt != nil {
		sent = *res.CompletedAt
	}
	out.Outcome = &robot.SendReceiptOutcome{Success: p.success() && !p.unknown, SentAt: sent.UTC(), Targets: p.targets,
		Successful: p.successful, Failed: p.failed}
	if p.unknown {
		out.Outcome.ErrorCode = robot.ErrCodeOperationOutcomeUnknown
		out.Outcome.Error = "a prompt may have been submitted; nhm cannot tell"
	}
	return out
}

// AckOutput projects a wait after a send onto ntm's --robot-ack answer: a
// target that completed acknowledged with prompt_returned, one whose turn
// started with output_started; a replaced or lost one failed.
func AckOutput(res Result, results []wait.Result, index []int, timeout time.Duration, format string, started, now time.Time) robot.AckOutput {
	out := robot.AckOutput{RobotResponse: robot.NewResponse(true, format, now), Session: res.Session,
		SentAt: res.CreatedAt.UTC(), CompletedAt: now.UTC(), Confirmations: []robot.AckConfirmation{},
		Pending: []string{}, Failed: []robot.AckFailure{}, TimeoutMs: int(timeout.Milliseconds()), NHM: nhmExtras(res)}
	keys := map[int]string{}
	for _, rc := range res.Targets {
		keys[rc.TargetIndex] = rc.Key
	}
	waited := map[int]bool{}
	for i, r := range results {
		key := keys[index[i]]
		waited[index[i]] = true
		at := started.Add(time.Duration(r.WaitedMS) * time.Millisecond).UTC()
		switch {
		case r.Outcome == wait.Complete || r.Outcome == wait.Ambiguous:
			out.Confirmations = append(out.Confirmations, robot.AckConfirmation{Pane: key, AckType: robot.AckPromptReturned,
				AckAt: at.Format(time.RFC3339), LatencyMs: int(r.WaitedMS)})
		case r.Outcome == wait.Replaced || r.Outcome == wait.ContinuityLost || r.Outcome == wait.Failed:
			out.Failed = append(out.Failed, robot.AckFailure{Pane: key, Reason: string(r.Outcome) + ": " + r.Reason})
		case r.StartedSeq != nil:
			out.Confirmations = append(out.Confirmations, robot.AckConfirmation{Pane: key, AckType: robot.AckOutputStarted,
				AckAt: at.Format(time.RFC3339), LatencyMs: int(r.WaitedMS)})
		default:
			out.Pending = append(out.Pending, key)
			if r.Outcome == wait.Timeout {
				out.TimedOut = true
			}
		}
	}
	for _, rc := range res.Targets {
		if !waited[rc.TargetIndex] {
			out.Failed = append(out.Failed, robot.AckFailure{Pane: rc.Key, Reason: "not submitted: " + reason(rc)})
		}
	}
	out.Success = len(out.Pending) == 0 && len(out.Failed) == 0
	return out
}
