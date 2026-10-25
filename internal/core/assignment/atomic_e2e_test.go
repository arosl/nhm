// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/assignment/atomic_e2e_test.go at commit 9d5ed977; see LICENSE
// in this directory and PROVENANCE.md. Local changes: this header; the
// assignment import path; occupant identities are nhm binding ids
// ("bnd-" and the old tmux pane number as 16 digits) in place of %N; the
// helper's environment names are NHM_ATOMIC_E2E_* in place of ntm's
// NTM_ATOMIC_E2E_* (nhm-2p6.28.3).

package assignment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/assignment"
)

const atomicHelperEnv = "NHM_ATOMIC_E2E_HELPER"

type atomicHelperResult struct {
	Sent                 bool                        `json:"sent"`
	Replayed             bool                        `json:"replayed"`
	Recovered            bool                        `json:"recovered"`
	Error                string                      `json:"error,omitempty"`
	ErrorKind            string                      `json:"error_kind,omitempty"`
	Status               assignment.AssignmentStatus `json:"status,omitempty"`
	ClaimState           assignment.ClaimState       `json:"claim_state,omitempty"`
	ClaimAttempts        int                         `json:"claim_attempts,omitempty"`
	DispatchState        assignment.DispatchState    `json:"dispatch_state,omitempty"`
	DispatchAttempts     int                         `json:"dispatch_attempts,omitempty"`
	DispatchTarget       string                      `json:"dispatch_target,omitempty"`
	PendingPrompt        string                      `json:"pending_prompt,omitempty"`
	DispatchReceiptID    string                      `json:"dispatch_receipt_id,omitempty"`
	ReservationCompleted bool                        `json:"reservation_completed"`
	ReservationState     assignment.ReservationState `json:"reservation_state,omitempty"`
	ReservationAttempts  int                         `json:"reservation_attempts,omitempty"`
	ReservationError     string                      `json:"reservation_error,omitempty"`
	ReservationRequested []string                    `json:"reservation_requested,omitempty"`
	ReservedPaths        []string                    `json:"reserved_paths,omitempty"`
	ReservationIDs       []int                       `json:"reservation_ids,omitempty"`
}

// TestAtomicAssignmentProcessHelper is invoked through the test binary itself
// so persistence, locking, recovery, and idempotency cross a real process
// boundary without requiring a live Beads or tmux installation.
func TestAtomicAssignmentProcessHelper(t *testing.T) {
	if os.Getenv(atomicHelperEnv) != "1" {
		return
	}

	session := mustAtomicEnv(t, "NHM_ATOMIC_E2E_SESSION")
	store, err := assignment.LoadStoreStrict(session)
	if os.Getenv("NHM_ATOMIC_E2E_EXPECT_LOAD_FAILURE") == "1" {
		if err == nil {
			t.Fatal("strict helper load unexpectedly accepted unsafe recovery state")
		}
		writeAtomicHelperResult(t, atomicHelperResult{Error: err.Error(), ErrorKind: "strict_load"})
		return
	}
	if err != nil {
		t.Fatalf("load strict store: %v", err)
	}
	atomicHelperReady(t)

	switch os.Getenv("NHM_ATOMIC_E2E_OPERATION") {
	case "stale-save":
		beadID := mustAtomicEnv(t, "NHM_ATOMIC_E2E_BEAD")
		if _, err := store.Assign(beadID, "cross-process assignment", 1, "codex", "Agent", "prompt"); err != nil {
			t.Fatalf("save stale-baseline assignment: %v", err)
		}
		writeAtomicHelperResult(t, atomicHelperResult{})
	case "dispatch-start":
		runAtomicDispatchStartHelper(t, store)
	case "stale-mutation":
		runAtomicStaleMutationHelper(t, store)
	case "execute":
		runAtomicExecuteHelper(t, store)
	default:
		t.Fatalf("unknown helper operation %q", os.Getenv("NHM_ATOMIC_E2E_OPERATION"))
	}
}

func runAtomicDispatchStartHelper(t *testing.T, store *assignment.AssignmentStore) {
	beadID := mustAtomicEnv(t, "NHM_ATOMIC_E2E_BEAD")
	key := mustAtomicEnv(t, "NHM_ATOMIC_E2E_KEY")
	startedAt, err := time.Parse(time.RFC3339Nano, mustAtomicEnv(t, "NHM_ATOMIC_E2E_STARTED_AT"))
	if err != nil {
		t.Fatalf("parse dispatch start time: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(beadID, key, startedAt); err != nil {
		t.Fatalf("record dispatch start: %v", err)
	}
	stored := store.Get(beadID)
	if stored == nil {
		t.Fatalf("dispatch-start assignment %s disappeared", beadID)
	}
	writeAtomicHelperResult(t, atomicHelperResult{
		Status:           stored.Status,
		DispatchState:    stored.DispatchState,
		DispatchAttempts: stored.DispatchAttempts,
	})
}

func runAtomicStaleMutationHelper(t *testing.T, store *assignment.AssignmentStore) {
	beadID := mustAtomicEnv(t, "NHM_ATOMIC_E2E_BEAD")
	operation := mustAtomicEnv(t, "NHM_ATOMIC_E2E_MUTATION")
	var mutationErr error
	switch operation {
	case "remove":
		mutationErr = store.Remove(beadID)
	case "clear":
		mutationErr = store.Clear()
	case "reassign":
		_, mutationErr = store.Reassign(beadID, assignment.ReassignmentTarget{Pane: 9, AgentType: "claude", AgentName: "ClaudeNine"})
	case "complete":
		mutationErr = store.MarkCompleted(beadID)
	default:
		t.Fatalf("unknown stale mutation %q", operation)
	}

	result := atomicHelperResult{}
	if mutationErr != nil {
		result.Error = mutationErr.Error()
		result.ErrorKind = atomicErrorKind(mutationErr)
	}
	writeAtomicHelperResult(t, result)
	if mutationErr != nil {
		t.Errorf("stale %s rejected: %v", operation, mutationErr)
	}
}

func runAtomicExecuteHelper(t *testing.T, store *assignment.AssignmentStore) {
	beadID := mustAtomicEnv(t, "NHM_ATOMIC_E2E_BEAD")
	key := mustAtomicEnv(t, "NHM_ATOMIC_E2E_KEY")
	requestedPaths := splitAtomicList(os.Getenv("NHM_ATOMIC_E2E_PATHS"))
	requireReservation := os.Getenv("NHM_ATOMIC_E2E_REQUIRE_RESERVATION") == "1"
	allowDiscovery := os.Getenv("NHM_ATOMIC_E2E_ALLOW_DISCOVERY") == "1"

	request := assignment.AtomicRequest{
		BeadID:                    beadID,
		BeadTitle:                 "atomic E2E assignment",
		Target:                    atomicEnvOr("NHM_ATOMIC_E2E_TARGET", "bnd-0000000000000042"),
		OccupancyKey:              os.Getenv("NHM_ATOMIC_E2E_OCCUPANCY_KEY"),
		Pane:                      7,
		AgentType:                 "codex",
		AgentName:                 "AtomicAgent",
		Actor:                     "AtomicE2E",
		Prompt:                    "complete the atomic assignment",
		IdempotencyKey:            key,
		RequireReservation:        requireReservation,
		AllowReservationDiscovery: allowDiscovery,
		RequestedPaths:            requestedPaths,
		ReservationTTL:            time.Hour,
	}

	claimer := &atomicFileClaimPort{
		dir:          mustAtomicEnv(t, "NHM_ATOMIC_E2E_CLAIM_DIR"),
		callLog:      mustAtomicEnv(t, "NHM_ATOMIC_E2E_CLAIM_LOG"),
		actuationLog: mustAtomicEnv(t, "NHM_ATOMIC_E2E_CLAIM_ACTUATION_LOG"),
		crashAt:      os.Getenv("NHM_ATOMIC_E2E_CRASH_AT"),
	}
	reserver := atomicReservationPort(
		os.Getenv("NHM_ATOMIC_E2E_RESERVATION"),
		mustAtomicEnv(t, "NHM_ATOMIC_E2E_RESERVATION_LOG"),
		mustAtomicEnv(t, "NHM_ATOMIC_E2E_RESERVATION_ACTUATION_LOG"),
		mustAtomicEnv(t, "NHM_ATOMIC_E2E_RESERVATION_DIR"),
		os.Getenv("NHM_ATOMIC_E2E_CRASH_AT"),
	)
	dispatcher := atomicDispatchPort{
		mode:         atomicEnvOr("NHM_ATOMIC_E2E_DISPATCH", "success"),
		callLog:      mustAtomicEnv(t, "NHM_ATOMIC_E2E_CALL_LOG"),
		actuationLog: mustAtomicEnv(t, "NHM_ATOMIC_E2E_ACTUATION_LOG"),
		crashAt:      os.Getenv("NHM_ATOMIC_E2E_CRASH_AT"),
	}
	coordinator := assignment.NewAtomicCoordinator(store, claimer, reserver, dispatcher)
	result, executeErr := coordinator.Execute(t.Context(), request)
	stored := store.Get(beadID)

	helperResult := atomicHelperResult{
		Sent:      result.Sent,
		Replayed:  result.Replayed,
		Recovered: result.Recovered,
	}
	if executeErr != nil {
		helperResult.Error = executeErr.Error()
		helperResult.ErrorKind = atomicErrorKind(executeErr)
	}
	if stored != nil {
		helperResult.Status = stored.Status
		helperResult.ClaimState = stored.ClaimState
		helperResult.ClaimAttempts = stored.ClaimAttempts
		helperResult.DispatchState = stored.DispatchState
		helperResult.DispatchAttempts = stored.DispatchAttempts
		helperResult.DispatchTarget = stored.DispatchTarget
		helperResult.PendingPrompt = stored.PendingPrompt
		helperResult.DispatchReceiptID = stored.DispatchReceiptID
		helperResult.ReservationCompleted = stored.ReservationCompleted
		helperResult.ReservationState = stored.ReservationState
		helperResult.ReservationAttempts = stored.ReservationAttempts
		helperResult.ReservationError = stored.ReservationError
		helperResult.ReservationRequested = append([]string(nil), stored.ReservationRequested...)
		helperResult.ReservedPaths = append([]string(nil), stored.ReservedPaths...)
		helperResult.ReservationIDs = append([]int(nil), stored.ReservationIDs...)
	}
	writeAtomicHelperResult(t, helperResult)
}

type atomicFileClaimPort struct {
	dir          string
	callLog      string
	actuationLog string
	crashAt      string
}

func (p *atomicFileClaimPort) Claim(_ context.Context, beadID, actor string) (assignment.ClaimReceipt, error) {
	if err := appendAtomicLog(p.callLog, actor+"\n"); err != nil {
		return assignment.ClaimReceipt{}, err
	}
	atomicCrashAt(p.crashAt, "claim-before-actuation")
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return assignment.ClaimReceipt{}, err
	}
	path := filepath.Join(p.dir, beadID+".claim")
	claimFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := claimFile.WriteString(actor); writeErr != nil {
			_ = claimFile.Close()
			return assignment.ClaimReceipt{}, writeErr
		}
		if closeErr := claimFile.Close(); closeErr != nil {
			return assignment.ClaimReceipt{}, closeErr
		}
		if err := appendAtomicLog(p.actuationLog, actor+"\n"); err != nil {
			return assignment.ClaimReceipt{}, err
		}
		atomicCrashAt(p.crashAt, "claim-after-actuation")
		return atomicClaimReceipt(beadID, actor), nil
	}
	if !os.IsExist(err) {
		return assignment.ClaimReceipt{}, err
	}

	owner, readErr := os.ReadFile(path)
	if readErr != nil {
		return assignment.ClaimReceipt{}, readErr
	}
	if strings.TrimSpace(string(owner)) != actor {
		return assignment.ClaimReceipt{}, assignment.ErrClaimConflict
	}
	return atomicClaimReceipt(beadID, actor), nil
}

func (p *atomicFileClaimPort) ReconcileClaim(_ context.Context, beadID, actor string) (assignment.ClaimReconciliation, error) {
	path := filepath.Join(p.dir, beadID+".claim")
	owner, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return assignment.ClaimReconciliation{State: assignment.ClaimReconciliationAbsent}, nil
	}
	if err != nil {
		return assignment.ClaimReconciliation{}, err
	}
	actual := strings.TrimSpace(string(owner))
	if actual != actor {
		return assignment.ClaimReconciliation{
			State:   assignment.ClaimReconciliationConflict,
			Receipt: assignment.ClaimReceipt{BeadID: beadID, Actor: actual},
		}, nil
	}
	return assignment.ClaimReconciliation{
		State: assignment.ClaimReconciliationOwned, Receipt: atomicClaimReceipt(beadID, actor),
	}, nil
}

func atomicClaimReceipt(beadID, actor string) assignment.ClaimReceipt {
	return assignment.ClaimReceipt{
		BeadID:    beadID,
		Actor:     actor,
		Status:    "in_progress",
		ClaimedAt: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
}

type atomicFileReservationPort struct {
	mode         string
	callLog      string
	actuationLog string
	dir          string
	crashAt      string
}

func atomicReservationPort(mode, callLog, actuationLog, dir, crashAt string) assignment.ReservationPort {
	if mode == "" || mode == "none" {
		return nil
	}
	return &atomicFileReservationPort{mode: mode, callLog: callLog, actuationLog: actuationLog, dir: dir, crashAt: crashAt}
}

func (p *atomicFileReservationPort) Reserve(_ context.Context, request assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
	if err := appendAtomicLog(p.callLog, request.BeadID+"\n"); err != nil {
		return assignment.LeaseReceipt{}, err
	}
	atomicCrashAt(p.crashAt, "reservation-before-actuation")
	lease, err := atomicReservationLease(p.mode, request)
	if err != nil {
		return assignment.LeaseReceipt{}, err
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return assignment.LeaseReceipt{}, err
	}
	data, err := json.Marshal(lease)
	if err != nil {
		return assignment.LeaseReceipt{}, err
	}
	if err := os.WriteFile(filepath.Join(p.dir, request.BeadID+".json"), data, 0o600); err != nil {
		return assignment.LeaseReceipt{}, err
	}
	if err := appendAtomicLog(p.actuationLog, request.BeadID+"\n"); err != nil {
		return assignment.LeaseReceipt{}, err
	}
	atomicCrashAt(p.crashAt, "reservation-after-actuation")
	return lease, nil
}

func (p *atomicFileReservationPort) ReconcileReservation(_ context.Context, request assignment.ReservationRequest, _ assignment.LeaseReceipt) (assignment.ReservationReconciliation, error) {
	data, err := os.ReadFile(filepath.Join(p.dir, request.BeadID+".json"))
	if os.IsNotExist(err) {
		return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationAbsent}, nil
	}
	if err != nil {
		return assignment.ReservationReconciliation{}, err
	}
	var lease assignment.LeaseReceipt
	if err := json.Unmarshal(data, &lease); err != nil {
		return assignment.ReservationReconciliation{}, err
	}
	return assignment.ReservationReconciliation{State: assignment.ReservationReconciliationReserved, Lease: lease}, nil
}

func atomicReservationLease(mode string, request assignment.ReservationRequest) (assignment.LeaseReceipt, error) {
	expiresAt := time.Date(2030, 7, 11, 13, 0, 0, 0, time.UTC)
	requested := append([]string(nil), request.RequestedPaths...)
	if strings.HasPrefix(mode, "discovery-") {
		requested = []string{"internal/discovered/**"}
		mode = strings.TrimPrefix(mode, "discovery-")
	}
	lease := assignment.LeaseReceipt{
		AgentName: request.AgentName, Target: request.Target, Requested: requested,
		ReservationIDs: []int{101, 102}, ExpiresAt: &expiresAt,
	}
	switch mode {
	case "full":
		lease.Granted = append([]string(nil), requested...)
	case "partial":
		if len(requested) > 0 {
			lease.Granted = []string{requested[0]}
		}
	case "empty":
		lease.Granted = []string{}
	default:
		return assignment.LeaseReceipt{}, fmt.Errorf("unsupported reservation mode %q", mode)
	}
	return lease, nil
}

type atomicDispatchPort struct {
	mode         string
	callLog      string
	actuationLog string
	crashAt      string
}

func (p atomicDispatchPort) Dispatch(_ context.Context, request assignment.DispatchRequest) (assignment.DispatchReceipt, error) {
	atomicCrashAt(p.crashAt, "dispatch-before-actuation")
	if err := appendAtomicLog(p.callLog, request.IdempotencyKey+"\n"); err != nil {
		return assignment.DispatchReceipt{}, assignment.GuaranteeNoActuation(err)
	}
	switch p.mode {
	case "success":
		if err := appendAtomicLog(p.actuationLog, request.IdempotencyKey+"\n"); err != nil {
			return assignment.DispatchReceipt{}, err
		}
		atomicCrashAt(p.crashAt, "dispatch-after-actuation")
		return assignment.DispatchReceipt{DeliveryID: "delivery-" + request.IdempotencyKey, Duration: 5 * time.Millisecond}, nil
	case "guaranteed":
		return assignment.DispatchReceipt{}, assignment.GuaranteeNoActuation(errors.New("preflight rejected before delivery"))
	case "ambiguous":
		if err := appendAtomicLog(p.actuationLog, request.IdempotencyKey+"\n"); err != nil {
			return assignment.DispatchReceipt{}, err
		}
		return assignment.DispatchReceipt{}, errors.New("connection lost after transport write")
	case "forbidden":
		return assignment.DispatchReceipt{}, errors.New("dispatcher was called during replay or reconciliation")
	default:
		return assignment.DispatchReceipt{}, assignment.GuaranteeNoActuation(fmt.Errorf("unsupported dispatch mode %q", p.mode))
	}
}

func appendAtomicLog(path, line string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func atomicCrashAt(configured, boundary string) {
	if configured == boundary {
		os.Exit(86)
	}
}

func atomicErrorKind(err error) string {
	var concurrentMutation *assignment.ConcurrentMutationError
	switch {
	case errors.As(err, &concurrentMutation):
		return "concurrent_mutation"
	case errors.Is(err, assignment.ErrClaimConflict):
		return "claim_conflict"
	case errors.Is(err, assignment.ErrTargetOccupied):
		return "target_occupied"
	case errors.Is(err, assignment.ErrReservationRequired):
		return "reservation_required"
	case errors.Is(err, assignment.ErrReservationPathsRequired):
		return "reservation_paths_required"
	case errors.Is(err, assignment.ErrReservationOutcomeUnknown):
		return "reservation_outcome_unknown"
	case errors.Is(err, assignment.ErrClaimOutcomeUnknown):
		return "claim_outcome_unknown"
	case errors.Is(err, assignment.ErrDispatchOutcomeUnknown):
		return "dispatch_outcome_unknown"
	case assignment.IsGuaranteedNoActuation(err):
		return "guaranteed_no_actuation"
	default:
		return "other"
	}
}

func atomicHelperReady(t *testing.T) {
	t.Helper()
	readyPath := os.Getenv("NHM_ATOMIC_E2E_READY")
	gatePath := os.Getenv("NHM_ATOMIC_E2E_GATE")
	if readyPath == "" && gatePath == "" {
		return
	}
	if readyPath == "" || gatePath == "" {
		t.Fatal("helper synchronization requires both ready and gate paths")
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("signal helper readiness: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(gatePath); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("inspect helper gate: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeAtomicHelperResult(t *testing.T, result atomicHelperResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal helper result: %v", err)
	}
	if err := os.WriteFile(mustAtomicEnv(t, "NHM_ATOMIC_E2E_RESULT"), data, 0o600); err != nil {
		t.Fatalf("write helper result: %v", err)
	}
}

func mustAtomicEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("required helper environment %s is empty", key)
	}
	return value
}

func atomicEnvOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitAtomicList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ";")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, strings.TrimSpace(part))
	}
	return result
}

type atomicHelperProcess struct {
	cmd        *exec.Cmd
	output     bytes.Buffer
	resultPath string
}

func startAtomicHelper(t *testing.T, env map[string]string) *atomicHelperProcess {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "result.json")
	env[atomicHelperEnv] = "1"
	env["NHM_ATOMIC_E2E_RESULT"] = resultPath

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAtomicAssignmentProcessHelper$", "-test.v")
	cmd.Env = atomicHelperEnvironment(os.Environ(), env)
	process := &atomicHelperProcess{cmd: cmd, resultPath: resultPath}
	cmd.Stdout = &process.output
	cmd.Stderr = &process.output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start atomic helper: %v", err)
	}
	return process
}

func runAtomicHelper(t *testing.T, env map[string]string) atomicHelperResult {
	t.Helper()
	return startAtomicHelper(t, env).wait(t)
}

func (p *atomicHelperProcess) wait(t *testing.T) atomicHelperResult {
	t.Helper()
	result, err := p.waitResult(t)
	if err != nil {
		t.Fatalf("atomic helper failed: %v\n%s", err, p.output.String())
	}
	return result
}

func (p *atomicHelperProcess) waitForFailure(t *testing.T) atomicHelperResult {
	t.Helper()
	result, err := p.waitResult(t)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("atomic helper exit = %v, want nonzero\n%s", err, p.output.String())
	}
	return result
}

func (p *atomicHelperProcess) waitResult(t *testing.T) (atomicHelperResult, error) {
	t.Helper()
	processErr := p.cmd.Wait()
	data, err := os.ReadFile(p.resultPath)
	if err != nil {
		t.Fatalf("read atomic helper result: %v\n%s", err, p.output.String())
	}
	var result atomicHelperResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode atomic helper result: %v\nraw=%s\n%s", err, data, p.output.String())
	}
	return result, processErr
}

func (p *atomicHelperProcess) waitForExit(t *testing.T, want int) {
	t.Helper()
	err := p.cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != want {
		t.Fatalf("atomic helper exit = %v, want %d\n%s", err, want, p.output.String())
	}
}

func atomicHelperEnvironment(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; !overridden && !strings.HasPrefix(key, "NHM_ATOMIC_E2E_") {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func waitForAtomicFiles(t *testing.T, paths ...string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		allReady := true
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				if !os.IsNotExist(err) {
					t.Fatalf("inspect helper readiness %s: %v", path, err)
				}
				allReady = false
			}
		}
		if allReady {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for helper readiness: %v", paths)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func atomicBaseEnv(home, session, beadID, key, workDir string) map[string]string {
	return map[string]string{
		"HOME":                                     home,
		"NHM_ATOMIC_E2E_OPERATION":                 "execute",
		"NHM_ATOMIC_E2E_SESSION":                   session,
		"NHM_ATOMIC_E2E_BEAD":                      beadID,
		"NHM_ATOMIC_E2E_KEY":                       key,
		"NHM_ATOMIC_E2E_CLAIM_DIR":                 filepath.Join(workDir, "claims"),
		"NHM_ATOMIC_E2E_CLAIM_LOG":                 filepath.Join(workDir, "claim-calls.log"),
		"NHM_ATOMIC_E2E_CLAIM_ACTUATION_LOG":       filepath.Join(workDir, "claim-actuations.log"),
		"NHM_ATOMIC_E2E_RESERVATION_LOG":           filepath.Join(workDir, "reservation-calls.log"),
		"NHM_ATOMIC_E2E_RESERVATION_ACTUATION_LOG": filepath.Join(workDir, "reservation-actuations.log"),
		"NHM_ATOMIC_E2E_RESERVATION_DIR":           filepath.Join(workDir, "reservations"),
		"NHM_ATOMIC_E2E_CALL_LOG":                  filepath.Join(workDir, "dispatch-calls.log"),
		"NHM_ATOMIC_E2E_ACTUATION_LOG":             filepath.Join(workDir, "dispatch-actuations.log"),
	}
}

func atomicLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read atomic log %s: %v", path, err)
	}
	return strings.Fields(string(data))
}

func TestAtomicAssignmentE2E_ProcessContentionAndStaleBaselineMerge(t *testing.T) {
	t.Run("independent stale stores preserve both updates", func(t *testing.T) {
		home := t.TempDir()
		syncDir := t.TempDir()
		gate := filepath.Join(syncDir, "gate")
		readyA := filepath.Join(syncDir, "ready-a")
		readyB := filepath.Join(syncDir, "ready-b")
		session := "atomic-e2e-stale-merge"

		processA := startAtomicHelper(t, map[string]string{
			"HOME": home, "NHM_ATOMIC_E2E_OPERATION": "stale-save", "NHM_ATOMIC_E2E_SESSION": session,
			"NHM_ATOMIC_E2E_BEAD": "ntm-process-a", "NHM_ATOMIC_E2E_READY": readyA, "NHM_ATOMIC_E2E_GATE": gate,
		})
		processB := startAtomicHelper(t, map[string]string{
			"HOME": home, "NHM_ATOMIC_E2E_OPERATION": "stale-save", "NHM_ATOMIC_E2E_SESSION": session,
			"NHM_ATOMIC_E2E_BEAD": "ntm-process-b", "NHM_ATOMIC_E2E_READY": readyB, "NHM_ATOMIC_E2E_GATE": gate,
		})
		waitForAtomicFiles(t, readyA, readyB)
		if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
			t.Fatalf("open helper gate: %v", err)
		}
		processA.wait(t)
		processB.wait(t)

		t.Setenv("HOME", home)
		store, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("reload merged store: %v", err)
		}
		if store.Get("ntm-process-a") == nil || store.Get("ntm-process-b") == nil || len(store.List()) != 2 {
			t.Fatalf("merged assignments=%+v, want both process updates", store.List())
		}
	})

	t.Run("same bead contenders claim and dispatch exactly once", func(t *testing.T) {
		home := t.TempDir()
		workDir := t.TempDir()
		syncDir := t.TempDir()
		gate := filepath.Join(syncDir, "gate")
		readyA := filepath.Join(syncDir, "ready-a")
		readyB := filepath.Join(syncDir, "ready-b")
		session := "atomic-e2e-contenders"
		beadID := "ntm-contended"

		envA := atomicBaseEnv(home, session, beadID, "contender-a", workDir)
		envA["NHM_ATOMIC_E2E_READY"] = readyA
		envA["NHM_ATOMIC_E2E_GATE"] = gate
		envA["NHM_ATOMIC_E2E_TARGET"] = "bnd-0000000000000011"
		envB := atomicBaseEnv(home, session, beadID, "contender-b", workDir)
		envB["NHM_ATOMIC_E2E_READY"] = readyB
		envB["NHM_ATOMIC_E2E_GATE"] = gate
		envB["NHM_ATOMIC_E2E_TARGET"] = "bnd-0000000000000012"
		processA := startAtomicHelper(t, envA)
		processB := startAtomicHelper(t, envB)
		waitForAtomicFiles(t, readyA, readyB)
		if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
			t.Fatalf("open helper gate: %v", err)
		}
		results := []atomicHelperResult{processA.wait(t), processB.wait(t)}

		var sent, conflicts int
		for _, result := range results {
			switch {
			case result.Sent && result.Error == "":
				sent++
			case result.ErrorKind == "claim_conflict":
				conflicts++
			default:
				t.Fatalf("unexpected contender result: %+v", result)
			}
		}
		if sent != 1 || conflicts != 1 {
			t.Fatalf("sent=%d conflicts=%d, want one each", sent, conflicts)
		}
		if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-calls.log")); len(got) != 1 {
			t.Fatalf("dispatch calls=%v, want exactly one", got)
		}
		if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-actuations.log")); len(got) != 1 {
			t.Fatalf("dispatch actuations=%v, want exactly one", got)
		}
		if effects := atomicSideEffectCounts(t, workDir); effects.claims != 1 || effects.reservations != 0 || effects.dispatches != 1 || effects.actuations != 1 {
			t.Fatalf("contender external boundaries=%+v, want claims=1 reservations=0 dispatches=1 actuations=1", effects)
		}

		t.Setenv("HOME", home)
		store, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("reload contended store: %v", err)
		}
		stored := store.Get(beadID)
		if stored == nil || stored.DispatchState != assignment.DispatchSent || stored.DispatchReceiptID == "" {
			t.Fatalf("durable winner=%+v", stored)
		}
	})

	t.Run("different beads cannot dispatch through aliases of the same target", func(t *testing.T) {
		home := t.TempDir()
		workDir := t.TempDir()
		syncDir := t.TempDir()
		gate := filepath.Join(syncDir, "gate")
		readyA := filepath.Join(syncDir, "ready-a")
		readyB := filepath.Join(syncDir, "ready-b")
		session := "atomic-e2e-same-target"

		envA := atomicBaseEnv(home, session, "ntm-target-a", "target-key-a", workDir)
		envA["NHM_ATOMIC_E2E_READY"] = readyA
		envA["NHM_ATOMIC_E2E_GATE"] = gate
		envA["NHM_ATOMIC_E2E_TARGET"] = "0.1"
		envA["NHM_ATOMIC_E2E_OCCUPANCY_KEY"] = "bnd-0000000000000077"
		envB := atomicBaseEnv(home, session, "ntm-target-b", "target-key-b", workDir)
		envB["NHM_ATOMIC_E2E_READY"] = readyB
		envB["NHM_ATOMIC_E2E_GATE"] = gate
		envB["NHM_ATOMIC_E2E_TARGET"] = "bnd-0000000000000077"
		envB["NHM_ATOMIC_E2E_OCCUPANCY_KEY"] = "bnd-0000000000000077"

		processA := startAtomicHelper(t, envA)
		processB := startAtomicHelper(t, envB)
		waitForAtomicFiles(t, readyA, readyB)
		if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
			t.Fatalf("open helper gate: %v", err)
		}
		results := []atomicHelperResult{processA.wait(t), processB.wait(t)}
		var sent, occupied int
		for _, result := range results {
			switch {
			case result.Sent && result.Error == "":
				sent++
			case result.ErrorKind == "target_occupied":
				occupied++
			default:
				t.Fatalf("unexpected same-target result: %+v", result)
			}
		}
		if sent != 1 || occupied != 1 {
			t.Fatalf("sent=%d occupied=%d, want one each", sent, occupied)
		}
		if effects := atomicSideEffectCounts(t, workDir); effects.claims != 1 || effects.reservations != 0 || effects.dispatches != 1 || effects.actuations != 1 {
			t.Fatalf("same-target external boundaries=%+v, want claims=1 reservations=0 dispatches=1 actuations=1", effects)
		}
	})

	t.Run("same idempotency key serializes before external side effects", func(t *testing.T) {
		home := t.TempDir()
		workDir := t.TempDir()
		syncDir := t.TempDir()
		gate := filepath.Join(syncDir, "gate")
		readyA := filepath.Join(syncDir, "ready-a")
		readyB := filepath.Join(syncDir, "ready-b")
		session := "atomic-e2e-same-key"
		beadID := "ntm-same-key"
		key := "shared-idempotency-key"

		envA := atomicBaseEnv(home, session, beadID, key, workDir)
		envA["NHM_ATOMIC_E2E_READY"] = readyA
		envA["NHM_ATOMIC_E2E_GATE"] = gate
		envB := atomicBaseEnv(home, session, beadID, key, workDir)
		envB["NHM_ATOMIC_E2E_READY"] = readyB
		envB["NHM_ATOMIC_E2E_GATE"] = gate

		processA := startAtomicHelper(t, envA)
		processB := startAtomicHelper(t, envB)
		waitForAtomicFiles(t, readyA, readyB)
		if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
			t.Fatalf("open helper gate: %v", err)
		}
		results := []atomicHelperResult{processA.wait(t), processB.wait(t)}

		var delivered, replayed int
		for _, result := range results {
			if result.Error != "" || !result.Sent {
				t.Fatalf("same-key process result=%+v, want sent success", result)
			}
			if result.Replayed {
				replayed++
			} else {
				delivered++
			}
		}
		if delivered != 1 || replayed != 1 {
			t.Fatalf("same-key delivered=%d replayed=%d, want one each", delivered, replayed)
		}
		if effects := atomicSideEffectCounts(t, workDir); effects.claims != 1 || effects.reservations != 0 || effects.dispatches != 1 || effects.actuations != 1 {
			t.Fatalf("same-key external boundaries=%+v, want claims=1 reservations=0 dispatches=1 actuations=1", effects)
		}
	})
}

func TestAtomicAssignmentE2E_StaleDestructiveProcessCannotEraseDispatchBarrier(t *testing.T) {
	mutations := []string{"remove", "clear", "reassign"}
	for index, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			syncDir := t.TempDir()
			ready := filepath.Join(syncDir, "stale-ready")
			gate := filepath.Join(syncDir, "stale-gate")
			session := fmt.Sprintf("atomic-e2e-stale-destructive-%s", mutation)
			beadID := fmt.Sprintf("ntm-stale-destructive-%s", mutation)
			key := fmt.Sprintf("stale-destructive-key-%s", mutation)
			startedAt := time.Date(2026, 7, 11, 15, index, 0, index+1, time.UTC)

			request := assignment.AtomicRequest{
				BeadID: beadID, BeadTitle: "stale destructive mutation", Target: "bnd-0000000000000017", OccupancyKey: "bnd-0000000000000017", Pane: 2,
				AgentType: "codex", AgentName: "CodexTwo", Actor: "AtomicE2E", Prompt: "continue", IdempotencyKey: key,
			}
			actor := assignment.StableClaimActor(request.Actor, key)
			store := assignment.NewStore(session)
			if _, err := store.RecordAtomicIntent(request, actor, startedAt.Add(-time.Minute)); err != nil {
				t.Fatalf("seed atomic intent: %v", err)
			}
			if _, err := store.RecordAtomicClaim(request, assignment.ClaimReceipt{
				BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: startedAt.Add(-30 * time.Second),
			}); err != nil {
				t.Fatalf("seed atomic claim: %v", err)
			}
			if err := store.UpdateStatus(beadID, assignment.StatusAssigned); err != nil {
				t.Fatalf("seed assigned status: %v", err)
			}
			if err := store.MarkWorking(beadID); err != nil {
				t.Fatalf("seed working status: %v", err)
			}

			staleProcess := startAtomicHelper(t, map[string]string{
				"HOME": home, "NHM_ATOMIC_E2E_OPERATION": "stale-mutation", "NHM_ATOMIC_E2E_SESSION": session,
				"NHM_ATOMIC_E2E_BEAD": beadID, "NHM_ATOMIC_E2E_MUTATION": mutation,
				"NHM_ATOMIC_E2E_READY": ready, "NHM_ATOMIC_E2E_GATE": gate,
			})
			waitForAtomicFiles(t, ready)

			barrier := runAtomicHelper(t, map[string]string{
				"HOME": home, "NHM_ATOMIC_E2E_OPERATION": "dispatch-start", "NHM_ATOMIC_E2E_SESSION": session,
				"NHM_ATOMIC_E2E_BEAD": beadID, "NHM_ATOMIC_E2E_KEY": key,
				"NHM_ATOMIC_E2E_STARTED_AT": startedAt.Format(time.RFC3339Nano),
			})
			if barrier.DispatchState != assignment.DispatchSending || barrier.DispatchAttempts != 1 || barrier.Status != assignment.StatusWorking {
				t.Fatalf("dispatch barrier helper result=%+v, want working/sending attempt 1", barrier)
			}

			if err := os.WriteFile(gate, []byte("mutate"), 0o600); err != nil {
				t.Fatalf("release stale mutation helper: %v", err)
			}
			staleResult := staleProcess.waitForFailure(t)
			if staleResult.ErrorKind != "concurrent_mutation" || !strings.Contains(staleResult.Error, beadID) {
				t.Fatalf("stale %s result=%+v, want typed concurrent mutation conflict", mutation, staleResult)
			}

			finalStore, err := assignment.LoadStoreStrict(session)
			if err != nil {
				t.Fatalf("reload ledger after stale %s: %v", mutation, err)
			}
			final := finalStore.Get(beadID)
			if final == nil || final.IdempotencyKey != key || final.DispatchState != assignment.DispatchSending ||
				final.DispatchAttempts != 1 || final.DispatchStartedAt == nil || !final.DispatchStartedAt.Equal(startedAt) {
				t.Fatalf("ledger after stale %s=%+v, want key %q and original sending barrier", mutation, final, key)
			}
		})
	}
}

func TestAtomicAssignmentE2E_StaleLifecycleCannotResurrectOrCrossGeneration(t *testing.T) {
	t.Run("removed assignment", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		syncDir := t.TempDir()
		ready := filepath.Join(syncDir, "stale-ready")
		gate := filepath.Join(syncDir, "stale-gate")
		const session = "atomic-e2e-stale-update-removed"
		const beadID = "ntm-stale-update-removed"

		seed := assignment.NewStore(session)
		if _, err := seed.Assign(beadID, "Removed generation", 1, "codex", "CodexOne", "work"); err != nil {
			t.Fatalf("Assign: %v", err)
		}
		if err := seed.MarkWorking(beadID); err != nil {
			t.Fatalf("MarkWorking: %v", err)
		}
		stale := startAtomicHelper(t, map[string]string{
			"HOME": home, "NHM_ATOMIC_E2E_OPERATION": "stale-mutation", "NHM_ATOMIC_E2E_SESSION": session,
			"NHM_ATOMIC_E2E_BEAD": beadID, "NHM_ATOMIC_E2E_MUTATION": "complete",
			"NHM_ATOMIC_E2E_READY": ready, "NHM_ATOMIC_E2E_GATE": gate,
		})
		waitForAtomicFiles(t, ready)
		remover, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("load remover: %v", err)
		}
		if err := remover.Remove(beadID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := os.WriteFile(gate, []byte("complete"), 0o600); err != nil {
			t.Fatalf("release stale completion: %v", err)
		}
		result := stale.waitForFailure(t)
		if result.ErrorKind != "concurrent_mutation" || !strings.Contains(result.Error, beadID) {
			t.Fatalf("stale completion result=%+v, want concurrent mutation", result)
		}
		final, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("reload removed ledger: %v", err)
		}
		if got := final.Get(beadID); got != nil {
			t.Fatalf("stale child resurrected removed assignment: %+v", got)
		}
	})

	t.Run("superseded generation", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		syncDir := t.TempDir()
		ready := filepath.Join(syncDir, "stale-ready")
		gate := filepath.Join(syncDir, "stale-gate")
		const session = "atomic-e2e-stale-update-generation"
		const beadID = "ntm-stale-update-generation"
		now := time.Now().UTC()
		oldRequest := assignment.AtomicRequest{
			BeadID: beadID, BeadTitle: "Old generation", Target: "bnd-0000000000000091", OccupancyKey: "bnd-0000000000000091", Pane: 1,
			AgentType: "codex", AgentName: "CodexOne", Actor: "CodexOne", Prompt: "old work", IdempotencyKey: "old-process-generation",
		}
		seed := assignment.NewStore(session)
		oldActor := assignment.StableClaimActor(oldRequest.Actor, oldRequest.IdempotencyKey)
		if _, err := seed.RecordAtomicIntent(oldRequest, oldActor, now); err != nil {
			t.Fatalf("RecordAtomicIntent(old): %v", err)
		}
		if _, err := seed.RecordAtomicClaim(oldRequest, assignment.ClaimReceipt{BeadID: beadID, Actor: oldActor, Status: "in_progress", ClaimedAt: now}); err != nil {
			t.Fatalf("RecordAtomicClaim(old): %v", err)
		}
		if err := seed.UpdateStatus(beadID, assignment.StatusAssigned); err != nil {
			t.Fatalf("mark old assigned: %v", err)
		}
		if err := seed.MarkWorking(beadID); err != nil {
			t.Fatalf("mark old working: %v", err)
		}
		stale := startAtomicHelper(t, map[string]string{
			"HOME": home, "NHM_ATOMIC_E2E_OPERATION": "stale-mutation", "NHM_ATOMIC_E2E_SESSION": session,
			"NHM_ATOMIC_E2E_BEAD": beadID, "NHM_ATOMIC_E2E_MUTATION": "complete",
			"NHM_ATOMIC_E2E_READY": ready, "NHM_ATOMIC_E2E_GATE": gate,
		})
		waitForAtomicFiles(t, ready)
		winner, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("load winner: %v", err)
		}
		if err := winner.MarkCompleted(beadID); err != nil {
			t.Fatalf("complete old generation: %v", err)
		}
		newRequest := oldRequest
		newRequest.BeadTitle = "New generation"
		newRequest.Prompt = "new work"
		newRequest.IdempotencyKey = "new-process-generation"
		newActor := assignment.StableClaimActor(newRequest.Actor, newRequest.IdempotencyKey)
		if _, err := winner.RecordAtomicIntent(newRequest, newActor, now.Add(time.Minute)); err != nil {
			t.Fatalf("RecordAtomicIntent(new): %v", err)
		}
		if err := os.WriteFile(gate, []byte("complete"), 0o600); err != nil {
			t.Fatalf("release stale completion: %v", err)
		}
		result := stale.waitForFailure(t)
		if result.ErrorKind != "concurrent_mutation" || !strings.Contains(result.Error, beadID) {
			t.Fatalf("old-generation child result=%+v, want concurrent mutation", result)
		}
		final, err := assignment.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("reload new generation: %v", err)
		}
		stored := final.Get(beadID)
		if stored == nil || stored.IdempotencyKey != newRequest.IdempotencyKey || stored.Status != assignment.StatusClaiming || stored.PendingPrompt != newRequest.Prompt {
			t.Fatalf("old-generation child corrupted durable winner: %+v", stored)
		}
	})
}

func TestAtomicAssignmentE2E_DurableRetryReplayAndCompletionTarget(t *testing.T) {
	home := t.TempDir()
	workDir := t.TempDir()
	session := "atomic-e2e-retry"
	beadID := "ntm-retry"
	key := "durable-attempt"
	env := atomicBaseEnv(home, session, beadID, key, workDir)
	env["NHM_ATOMIC_E2E_TARGET"] = "bnd-0000000000000042"
	env["NHM_ATOMIC_E2E_REQUIRE_RESERVATION"] = "1"
	env["NHM_ATOMIC_E2E_PATHS"] = "internal/assignment/**;internal/completion/**"
	env["NHM_ATOMIC_E2E_RESERVATION"] = "full"
	env["NHM_ATOMIC_E2E_DISPATCH"] = "guaranteed"

	first := runAtomicHelper(t, env)
	if first.ErrorKind != "guaranteed_no_actuation" || first.DispatchState != assignment.DispatchPending || first.DispatchAttempts != 1 {
		t.Fatalf("first guaranteed failure=%+v", first)
	}
	if !first.ReservationCompleted || len(first.ReservationIDs) != 2 {
		t.Fatalf("first persisted reservation=%+v", first)
	}
	if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-actuations.log")); len(got) != 0 {
		t.Fatalf("guaranteed failure actuated transport: %v", got)
	}

	env["NHM_ATOMIC_E2E_RESERVATION"] = "none"
	env["NHM_ATOMIC_E2E_DISPATCH"] = "success"
	second := runAtomicHelper(t, env)
	if second.Error != "" || !second.Sent || !second.Recovered || second.Replayed {
		t.Fatalf("restart recovery=%+v", second)
	}
	if second.DispatchState != assignment.DispatchSent || second.DispatchAttempts != 2 || second.DispatchReceiptID != "delivery-"+key {
		t.Fatalf("recovered durable dispatch=%+v", second)
	}
	if second.DispatchTarget != "bnd-0000000000000042" || !second.ReservationCompleted || len(second.ReservedPaths) != 2 {
		t.Fatalf("recovered durable intent=%+v", second)
	}

	env["NHM_ATOMIC_E2E_DISPATCH"] = "forbidden"
	third := runAtomicHelper(t, env)
	if third.Error != "" || !third.Sent || !third.Replayed || third.Recovered {
		t.Fatalf("idempotent replay=%+v", third)
	}
	if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-calls.log")); len(got) != 2 {
		t.Fatalf("dispatch calls=%v, want failed attempt plus successful retry only", got)
	}
	if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-actuations.log")); len(got) != 1 {
		t.Fatalf("dispatch actuations=%v, want exactly one delivery", got)
	}
	if effects := atomicSideEffectCounts(t, workDir); effects.claims != 1 || effects.reservations != 1 || effects.dispatches != 2 || effects.actuations != 1 {
		t.Fatalf("retry/replay external boundaries=%+v, want claims=1 reservations=1 dispatches=2 actuations=1", effects)
	}

	t.Setenv("HOME", home)
	store, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("load completion lifecycle store: %v", err)
	}
	if err := store.MarkWorking(beadID); err != nil {
		t.Fatalf("mark working: %v", err)
	}
	if err := store.MarkCompleted(beadID); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	restarted, err := assignment.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload completed assignment: %v", err)
	}
	completed := restarted.Get(beadID)
	if completed == nil || completed.Status != assignment.StatusCompleted || completed.DispatchTarget != "bnd-0000000000000042" || completed.CompletedAt == nil {
		t.Fatalf("completed durable target=%+v", completed)
	}
}

func TestAtomicAssignmentE2E_RequiredReservationMatrix(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		paths         string
		discovery     bool
		wantSent      bool
		wantError     string
		wantRequested []string
		wantGranted   []string
	}{
		{
			name: "full grant dispatches", mode: "full", paths: "internal/a/**;internal/b/**", wantSent: true,
			wantRequested: []string{"internal/a/**", "internal/b/**"}, wantGranted: []string{"internal/a/**", "internal/b/**"},
		},
		{
			name: "partial grant fails closed", mode: "partial", paths: "internal/a/**;internal/b/**", wantError: "internal/b/**",
			wantRequested: []string{"internal/a/**", "internal/b/**"}, wantGranted: []string{"internal/a/**"},
		},
		{
			name: "empty grant fails closed", mode: "empty", paths: "internal/a/**;internal/b/**", wantError: "internal/a/**",
			wantRequested: []string{"internal/a/**", "internal/b/**"}, wantGranted: []string{},
		},
		{
			name: "explicit discovery full grant dispatches", mode: "discovery-full", discovery: true, wantSent: true,
			wantRequested: []string{"internal/discovered/**"}, wantGranted: []string{"internal/discovered/**"},
		},
		{
			name: "explicit discovery empty grant fails closed", mode: "discovery-empty", discovery: true,
			wantError: "internal/discovered/**", wantRequested: []string{"internal/discovered/**"}, wantGranted: []string{},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			env := atomicBaseEnv(home, fmt.Sprintf("atomic-e2e-reservation-%d", index), fmt.Sprintf("ntm-reservation-%d", index), fmt.Sprintf("reservation-key-%d", index), workDir)
			env["NHM_ATOMIC_E2E_REQUIRE_RESERVATION"] = "1"
			env["NHM_ATOMIC_E2E_RESERVATION"] = test.mode
			env["NHM_ATOMIC_E2E_PATHS"] = test.paths
			if test.discovery {
				env["NHM_ATOMIC_E2E_ALLOW_DISCOVERY"] = "1"
			}
			result := runAtomicHelper(t, env)

			if result.Sent != test.wantSent {
				t.Fatalf("sent=%v, want %v; result=%+v", result.Sent, test.wantSent, result)
			}
			if test.wantError == "" && result.Error != "" {
				t.Fatalf("unexpected reservation error: %+v", result)
			}
			if test.wantError != "" && !strings.Contains(result.Error, test.wantError) {
				t.Fatalf("reservation error=%q, want substring %q; result=%+v", result.Error, test.wantError, result)
			}
			if !stringSlicesEqual(result.ReservationRequested, test.wantRequested) || !stringSlicesEqual(result.ReservedPaths, test.wantGranted) {
				t.Fatalf("reservation requested=%v granted=%v, want %v/%v", result.ReservationRequested, result.ReservedPaths, test.wantRequested, test.wantGranted)
			}
			calls := atomicLogLines(t, filepath.Join(workDir, "dispatch-calls.log"))
			if test.wantSent && len(calls) != 1 {
				t.Fatalf("dispatch calls=%v, want one", calls)
			}
			if !test.wantSent && len(calls) != 0 {
				t.Fatalf("failed reservation dispatched: %v", calls)
			}
			if result.ReservationCompleted != test.wantSent {
				t.Fatalf("reservation_completed=%v, want %v; result=%+v", result.ReservationCompleted, test.wantSent, result)
			}
			effects := atomicSideEffectCounts(t, workDir)
			wantDispatches := 0
			if test.wantSent {
				wantDispatches = 1
				if result.Status != assignment.StatusAssigned || result.DispatchState != assignment.DispatchSent {
					t.Fatalf("successful reservation did not reach assigned/sent: %+v", result)
				}
			} else if result.Status != assignment.StatusClaimed || result.DispatchState != assignment.DispatchPending || result.ReservationError == "" {
				t.Fatalf("failed reservation did not remain claimed/pending with durable error: %+v", result)
			}
			if effects.claims != 1 || effects.reservations != 1 || effects.dispatches != wantDispatches || effects.actuations != wantDispatches {
				t.Fatalf("reservation external boundaries=%+v, want claims=1 reservations=1 dispatches=%d actuations=%d", effects, wantDispatches, wantDispatches)
			}
		})
	}
}

func TestAtomicAssignmentE2E_AmbiguousDispatchSurvivesRestart(t *testing.T) {
	home := t.TempDir()
	workDir := t.TempDir()
	session := "atomic-e2e-ambiguous"
	beadID := "ntm-ambiguous"
	key := "ambiguous-attempt"
	env := atomicBaseEnv(home, session, beadID, key, workDir)
	env["NHM_ATOMIC_E2E_DISPATCH"] = "ambiguous"

	first := runAtomicHelper(t, env)
	if first.ErrorKind != "dispatch_outcome_unknown" || first.DispatchState != assignment.DispatchSending || first.DispatchAttempts != 1 {
		t.Fatalf("ambiguous first result=%+v", first)
	}
	if first.PendingPrompt == "" {
		t.Fatalf("ambiguous delivery lost pending prompt: %+v", first)
	}
	if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-actuations.log")); len(got) != 1 {
		t.Fatalf("ambiguous delivery actuations=%v, want one possible delivery", got)
	}

	env["NHM_ATOMIC_E2E_DISPATCH"] = "forbidden"
	second := runAtomicHelper(t, env)
	if second.ErrorKind != "dispatch_outcome_unknown" || second.DispatchState != assignment.DispatchSending || second.DispatchAttempts != 1 {
		t.Fatalf("ambiguous restart result=%+v", second)
	}
	if got := atomicLogLines(t, filepath.Join(workDir, "dispatch-calls.log")); len(got) != 1 {
		t.Fatalf("restart redispatched ambiguous delivery: %v", got)
	}
	if effects := atomicSideEffectCounts(t, workDir); effects.claims != 1 || effects.reservations != 0 || effects.dispatches != 1 || effects.actuations != 1 {
		t.Fatalf("ambiguous restart external boundaries=%+v, want claims=1 reservations=0 dispatches=1 actuations=1", effects)
	}
}

func TestAtomicAssignmentE2E_CrashRecoveryBoundaries(t *testing.T) {
	tests := []struct {
		name                   string
		boundary               string
		reservation            bool
		wantSent               bool
		wantErrorKind          string
		wantClaimCalls         int
		wantClaimActuations    int
		wantReservationCalls   int
		wantReservationActions int
		wantDispatchCalls      int
		wantDispatchActuations int
	}{
		{name: "claim before actuation", boundary: "claim-before-actuation", wantSent: true, wantClaimCalls: 2, wantClaimActuations: 1, wantDispatchCalls: 1, wantDispatchActuations: 1},
		{name: "claim after actuation", boundary: "claim-after-actuation", wantSent: true, wantClaimCalls: 1, wantClaimActuations: 1, wantDispatchCalls: 1, wantDispatchActuations: 1},
		{name: "reservation before actuation", boundary: "reservation-before-actuation", reservation: true, wantSent: true, wantClaimCalls: 1, wantClaimActuations: 1, wantReservationCalls: 2, wantReservationActions: 1, wantDispatchCalls: 1, wantDispatchActuations: 1},
		{name: "reservation after actuation", boundary: "reservation-after-actuation", reservation: true, wantSent: true, wantClaimCalls: 1, wantClaimActuations: 1, wantReservationCalls: 1, wantReservationActions: 1, wantDispatchCalls: 1, wantDispatchActuations: 1},
		{name: "dispatch before actuation", boundary: "dispatch-before-actuation", wantErrorKind: "dispatch_outcome_unknown", wantClaimCalls: 1, wantClaimActuations: 1},
		{name: "dispatch after actuation", boundary: "dispatch-after-actuation", wantErrorKind: "dispatch_outcome_unknown", wantClaimCalls: 1, wantClaimActuations: 1, wantDispatchCalls: 1, wantDispatchActuations: 1},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			workDir := t.TempDir()
			env := atomicBaseEnv(home, fmt.Sprintf("atomic-crash-%d", index), fmt.Sprintf("ntm-crash-%d", index), fmt.Sprintf("crash-key-%d", index), workDir)
			env["NHM_ATOMIC_E2E_CRASH_AT"] = test.boundary
			if test.reservation {
				env["NHM_ATOMIC_E2E_REQUIRE_RESERVATION"] = "1"
				env["NHM_ATOMIC_E2E_RESERVATION"] = "full"
				env["NHM_ATOMIC_E2E_PATHS"] = "internal/assignment/**"
			}

			startAtomicHelper(t, env).waitForExit(t, 86)
			delete(env, "NHM_ATOMIC_E2E_CRASH_AT")
			result := runAtomicHelper(t, env)
			if result.Sent != test.wantSent || result.ErrorKind != test.wantErrorKind {
				t.Fatalf("recovery result = %+v, want sent=%v error_kind=%q", result, test.wantSent, test.wantErrorKind)
			}
			effects := atomicSideEffectCounts(t, workDir)
			if effects.claims != test.wantClaimCalls || effects.claimActuations != test.wantClaimActuations ||
				effects.reservations != test.wantReservationCalls || effects.reservationActuations != test.wantReservationActions ||
				effects.dispatches != test.wantDispatchCalls || effects.actuations != test.wantDispatchActuations {
				t.Fatalf("recovery effects = %+v", effects)
			}
		})
	}
}

func TestAtomicAssignmentE2E_StrictMutatingRecoveryFailsClosed(t *testing.T) {
	t.Run("torn primary with valid backup", func(t *testing.T) {
		home := t.TempDir()
		workDir := t.TempDir()
		session := "atomic-e2e-torn-primary"
		beadID := "ntm-torn-primary"
		env := atomicBaseEnv(home, session, beadID, "torn-attempt", workDir)
		env["NHM_ATOMIC_E2E_DISPATCH"] = "ambiguous"
		result := runAtomicHelper(t, env)
		if result.DispatchState != assignment.DispatchSending {
			t.Fatalf("precondition dispatch state=%s, want sending", result.DispatchState)
		}

		t.Setenv("HOME", home)
		ledgerPath := filepath.Join(assignment.StorageDir(), session, "assignments.json")
		backupData, err := os.ReadFile(ledgerPath + ".bak")
		if err != nil {
			t.Fatalf("read diagnostic backup: %v", err)
		}
		var backup struct {
			Assignments map[string]*assignment.Assignment `json:"assignments"`
		}
		if err := json.Unmarshal(backupData, &backup); err != nil {
			t.Fatalf("decode diagnostic backup: %v", err)
		}
		if stored := backup.Assignments[beadID]; stored == nil || stored.DispatchState != assignment.DispatchSending {
			t.Fatalf("backup did not preserve ambiguity barrier: %+v", stored)
		}
		if err := os.WriteFile(ledgerPath, []byte("{torn-primary"), 0o600); err != nil {
			t.Fatalf("simulate torn primary ledger: %v", err)
		}
		if recovered, err := assignment.LoadStoreStrict(session); err == nil {
			t.Fatalf("strict mutating load accepted corrupt primary via backup: %+v", recovered.Get(beadID))
		}

		before := atomicSideEffectCounts(t, workDir)
		env["NHM_ATOMIC_E2E_EXPECT_LOAD_FAILURE"] = "1"
		env["NHM_ATOMIC_E2E_DISPATCH"] = "forbidden"
		restarted := runAtomicHelper(t, env)
		if restarted.ErrorKind != "strict_load" || restarted.Error == "" {
			t.Fatalf("torn-primary helper result=%+v", restarted)
		}
		if after := atomicSideEffectCounts(t, workDir); after != before {
			t.Fatalf("strict load failure crossed an external boundary: before=%+v after=%+v", before, after)
		}
	})

	t.Run("orphaned backup without primary", func(t *testing.T) {
		home := t.TempDir()
		workDir := t.TempDir()
		session := "atomic-e2e-orphan-backup"
		t.Setenv("HOME", home)
		ledgerPath := filepath.Join(assignment.StorageDir(), session, "assignments.json")
		if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
			t.Fatalf("create orphan backup directory: %v", err)
		}
		backup := []byte(`{"session_name":"atomic-e2e-orphan-backup","assignments":{},"updated_at":"2026-07-11T12:00:00Z","version":2}`)
		if err := os.WriteFile(ledgerPath+".bak", backup, 0o600); err != nil {
			t.Fatalf("write orphan backup: %v", err)
		}
		if _, err := assignment.LoadStoreStrict(session); err == nil {
			t.Fatal("strict mutating load accepted orphaned backup")
		}

		env := atomicBaseEnv(home, session, "ntm-orphan-backup", "orphan-attempt", workDir)
		env["NHM_ATOMIC_E2E_EXPECT_LOAD_FAILURE"] = "1"
		result := runAtomicHelper(t, env)
		if result.ErrorKind != "strict_load" || result.Error == "" {
			t.Fatalf("orphan-backup helper result=%+v", result)
		}
		if effects := atomicSideEffectCounts(t, workDir); effects != (atomicEffects{}) {
			t.Fatalf("orphan-backup strict load crossed an external boundary: %+v", effects)
		}
	})
}

type atomicEffects struct {
	claims                int
	claimActuations       int
	reservations          int
	reservationActuations int
	dispatches            int
	actuations            int
}

func atomicSideEffectCounts(t *testing.T, workDir string) atomicEffects {
	t.Helper()
	return atomicEffects{
		claims:                len(atomicLogLines(t, filepath.Join(workDir, "claim-calls.log"))),
		claimActuations:       len(atomicLogLines(t, filepath.Join(workDir, "claim-actuations.log"))),
		reservations:          len(atomicLogLines(t, filepath.Join(workDir, "reservation-calls.log"))),
		reservationActuations: len(atomicLogLines(t, filepath.Join(workDir, "reservation-actuations.log"))),
		dispatches:            len(atomicLogLines(t, filepath.Join(workDir, "dispatch-calls.log"))),
		actuations:            len(atomicLogLines(t, filepath.Join(workDir, "dispatch-actuations.log"))),
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
