// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/bv.go at commit 9d5ed977, with ErrTimeout from
// internal/bv/client.go; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; imports. Kept whole and unchanged: the bv
// runners (IsNotInitialized to runWithContextTimeout), GetPlan and
// GetPlanContext, GetInsightsContext (nhm-cvw: the human spawn --assign's
// cycle check), the br runner (RunBd to isBeadsDBCorruptionError), the
// claim errors, operator gates and AssignmentEligibilityError,
// BeadClaimResult, ClaimBeadForAssignment, requireBeadsMutationContext, the
// br claim and list helpers (beadClaimArgs to stripFlagWithValue), and the
// status, authorization and details readers (GetBeadStatus to
// extractStatusField). Changed: the ClaimBeadForAssignment doc comment;
// ClaimBeadForAssignmentWithOperatorGatedLabels no longer wraps the claim in
// withBeadsCorruptionRecovery; claimBeadForAssignmentAttempt claims through
// br's engine (br update --claim --if-unchanged) under nhm's per-project
// claim lock, with ntm's validation and mutex check on one br show read,
// a write that never retries, and a new read after a refused or failed
// write (claimBeadUnderLock; claim_nhm.go; ACFS acfs-f2mc.2 closure
// record). Left out: the direct
// SQLite claim, release and stale-adoption transactions and their content
// hash repair, recoverCorruptBeadsDatabase and withBeadsCorruptionRecovery
// (an automatic `br sync --import-only --rebuild` of a shared tracker), the
// generic ClaimBead and ReleaseBeadClaim, HasLocalBeadsDB, and the bv robot
// readers nhm does not call (priority, recipes, drift, graph, health,
// summaries, lists).

// Package bv provides integration with the beads_viewer (bv) tool.
// It executes bv robot mode commands and parses their JSON output.
package bv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotInstalled indicates bv is not available
var ErrNotInstalled = errors.New("bv is not installed")

// ErrTimeout indicates bv command timed out
var ErrTimeout = errors.New("bv command timed out")

// ErrNoBaseline indicates no baseline exists for drift checking
var ErrNoBaseline = errors.New("no baseline found")

// IsNotInitialized reports whether a br or bv call failed only because the
// project has no beads workspace (no .beads directory or database), a state
// of the project rather than a fault. It matches the phrasings br has used
// across versions, including its NOT_INITIALIZED JSON code, and bv's
// "failed to read beads directory" for a directory that does not exist.
func IsNotInitialized(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "error loading beads") {
		// bv appends "initialized with 'br init'" to every load failure,
		// permission errors included; only a missing directory means no
		// workspace.
		return strings.Contains(msg, "failed to read beads directory") && strings.Contains(msg, "no such file or directory")
	}
	for _, fragment := range []string{
		"no .beads",
		"not initialized",
		"not_initialized",
		"no beads database",
		"br init",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
}

// DefaultTimeout is the default timeout for external command execution
const DefaultTimeout = 30 * time.Second

// noDBCache tracks which directories require --no-db flag.
// Key: directory path, Value: bool (true if --no-db is needed)
// We use a sync.Map for thread-safe concurrent access across sessions.
var noDBCache sync.Map
var runBDMutexes sync.Map

type workspaceBDGate struct {
	token chan struct{}
}

func newWorkspaceBDGate() *workspaceBDGate {
	gate := &workspaceBDGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (g *workspaceBDGate) Lock() {
	<-g.token
}

func (g *workspaceBDGate) LockContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workspace Beads lock context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		return nil
	}
}

func (g *workspaceBDGate) Unlock() {
	g.token <- struct{}{}
}

func getNoDBState(dir string) bool {
	v, ok := noDBCache.Load(dir)
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func setNoDBState(dir string, val bool) {
	noDBCache.Store(dir, val)
}

func workspaceBDMutex(dir string) *workspaceBDGate {
	if existing, ok := runBDMutexes.Load(dir); ok {
		if mu, ok := existing.(*workspaceBDGate); ok {
			return mu
		}
	}
	mu := newWorkspaceBDGate()
	actual, _ := runBDMutexes.LoadOrStore(dir, mu)
	if existing, ok := actual.(*workspaceBDGate); ok {
		return existing
	}
	return mu
}

// IsInstalled checks if bv is available in PATH
func IsInstalled() bool {
	_, err := exec.LookPath("bv")
	return err == nil
}

// run executes bv with given args and returns stdout.
// It includes retry logic for transient database locks.
func run(dir string, args ...string) (string, error) {
	return runWithTimeout(dir, CommandTimeout(), args...)
}

func runWithTimeout(dir string, timeout time.Duration, args ...string) (string, error) {
	return runWithContextTimeout(context.Background(), dir, timeout, args...)
}

func runWithContextTimeout(parent context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	if parent == nil {
		return "", errors.New("bv command context is required")
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	if !IsInstalled() {
		return "", ErrNotInstalled
	}
	if timeout <= 0 {
		timeout = CommandTimeout()
	}

	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return "", err
	}

	const maxAttempts = 3
	deadline := time.Now().Add(timeout)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("bv timed out after %v: %w", timeout, ErrTimeout)
		}

		ctx, cancel := context.WithTimeout(parent, remaining)

		cmd := exec.CommandContext(ctx, "bv", args...)
		cmd.Dir = normalizedDir
		cmd.WaitDelay = time.Second // Prevent hanging on open pipes if child processes outlive context
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()
		cancel()

		if err == nil {
			return strings.TrimSpace(stdout.String()), nil
		}

		// Check for specific error conditions
		stderrStr := stderr.String()
		stdoutStr := stdout.String()

		if parent.Err() != nil {
			return "", parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("bv timed out after %v: %w", timeout, ErrTimeout)
		}

		if strings.Contains(stderrStr, "No baseline found") {
			return "", ErrNoBaseline
		}

		// Handle transient workspace contention (SQLite locks, br write lock).
		if isTransientBeadsDBError(stderrStr, stdoutStr) {
			backoff, retry := transientBeadsRetryDelay(attempt, maxAttempts, deadline)
			if !retry {
				if attempt < maxAttempts {
					return "", fmt.Errorf("bv timed out after %v: %w", timeout, ErrTimeout)
				}
				return "", fmt.Errorf("bv %s: workspace still contended after %d attempt(s): %w: %s",
					strings.Join(args, " "), attempt, err, stderrStr)
			}
			if err := waitForBeadsRetry(parent, backoff); err != nil {
				return "", err
			}
			continue
		}

		return "", fmt.Errorf("bv %s: %w: %s", strings.Join(args, " "), err, stderrStr)
	}

	return "", fmt.Errorf("bv %s: exceeded retry budget", strings.Join(args, " "))
}

// GetPlan returns a parallel execution plan
func GetPlan(dir string) (*PlanResponse, error) {
	return GetPlanContext(context.Background(), dir)
}

// GetInsightsContext returns graph insights with caller cancellation.
func GetInsightsContext(ctx context.Context, dir string) (*InsightsResponse, error) {
	output, err := runWithContextTimeout(ctx, dir, CommandTimeout(), "--robot-insights")
	if err != nil {
		return nil, err
	}

	var resp InsightsResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing insights: %w", err)
	}

	return &resp, nil
}

// GetPlanContext returns the parallel execution plan with caller cancellation.
func GetPlanContext(ctx context.Context, dir string) (*PlanResponse, error) {
	output, err := runWithContextTimeout(ctx, dir, CommandTimeout(), "--robot-plan")
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	if len(envelope.Plan) == 0 || string(envelope.Plan) == "null" {
		return nil, errors.New("parsing plan: missing plan object")
	}
	var planEnvelope struct {
		Tracks json.RawMessage `json:"tracks"`
	}
	if err := json.Unmarshal(envelope.Plan, &planEnvelope); err != nil {
		return nil, fmt.Errorf("parsing plan object: %w", err)
	}
	if len(planEnvelope.Tracks) == 0 || string(planEnvelope.Tracks) == "null" {
		return nil, errors.New("parsing plan: missing plan.tracks array")
	}
	var trackEnvelopes []struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(planEnvelope.Tracks, &trackEnvelopes); err != nil {
		return nil, fmt.Errorf("parsing plan.tracks: %w", err)
	}
	for i, track := range trackEnvelopes {
		if len(track.Items) == 0 || string(track.Items) == "null" {
			return nil, fmt.Errorf("parsing plan: missing plan.tracks[%d].items array", i)
		}
		var items []struct {
			Status   *string `json:"status"`
			Priority *int    `json:"priority"`
		}
		if err := json.Unmarshal(track.Items, &items); err != nil {
			return nil, fmt.Errorf("parsing plan.tracks[%d].items: %w", i, err)
		}
		for itemIndex, item := range items {
			if item.Status == nil || strings.TrimSpace(*item.Status) == "" {
				return nil, fmt.Errorf("parsing plan: missing or blank plan.tracks[%d].items[%d].status", i, itemIndex)
			}
			if item.Priority == nil {
				return nil, fmt.Errorf("parsing plan: missing plan.tracks[%d].items[%d].priority", i, itemIndex)
			}
			if *item.Priority < 0 || *item.Priority > 4 {
				return nil, fmt.Errorf("parsing plan: plan.tracks[%d].items[%d].priority %d is outside 0..4", i, itemIndex, *item.Priority)
			}
		}
	}

	var resp PlanResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	for trackIndex := range resp.Plan.Tracks {
		for itemIndex := range resp.Plan.Tracks[trackIndex].Items {
			item := &resp.Plan.Tracks[trackIndex].Items[itemIndex]
			item.Status = strings.TrimSpace(item.Status)
			if item.Status == "" {
				return nil, fmt.Errorf("parsing plan: missing or blank plan.tracks[%d].items[%d].status", trackIndex, itemIndex)
			}
		}
	}

	return &resp, nil
}

// RunBd executes br (beads_rust) with given args and returns stdout.
// If br reports a missing database and suggests `--no-db`, it retries once with `--no-db`
// and caches that preference for the remainder of the process.
func RunBd(dir string, args ...string) (string, error) {
	return runBdContext(context.Background(), dir, true, args...)
}

// RunBdContext executes br with caller-controlled cancellation while retaining
// the workspace serialization and no-database retry policy used by RunBd.
func RunBdContext(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		return "", errors.New("br command context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return runBdContext(ctx, dir, true, args...)
}

// runBdContext executes br with the workspace serialization and retry policy
// used by RunBd. allowNoDB must be false for compare-and-set mutations such as
// claims because JSONL-only fallback cannot provide the SQLite transaction.
func runBdContext(parent context.Context, dir string, allowNoDB bool, args ...string) (string, error) {
	if parent == nil {
		return "", errors.New("br command context is required")
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	// Normalize dir to ensure consistent cache keys.
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return "", err
	}
	dir = normalizedDir
	args = append([]string(nil), args...)

	// br's SQLite-backed workspace can self-contend when a single ntm process
	// launches multiple br subprocesses against the same directory in parallel.
	mu := workspaceBDMutex(dir)
	if err := mu.LockContext(parent); err != nil {
		return "", err
	}
	defer mu.Unlock()

	// Check cache for this specific directory
	if allowNoDB && getNoDBState(dir) && !containsString(args, "--no-db") {
		args = append([]string{"--no-db"}, args...)
	}
	if !containsString(args, "--no-db") && !containsString(args, "--lock-timeout") {
		args = append([]string{"--lock-timeout", "5000"}, args...)
	}

	const maxAttempts = 6 // Canonical default: config.RetryConfig.DB.MaxAttempts
	retryDeadline := time.Now().Add(transientBeadsRetryBudget)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(parent, DefaultTimeout)

		cmd := exec.CommandContext(ctx, "br", args...)
		cmd.WaitDelay = time.Second // Prevent hanging on open pipes
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()
		cancel()
		if err == nil {
			return strings.TrimSpace(stdout.String()), nil
		}
		if parent.Err() != nil {
			return "", parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("br timed out after %v", DefaultTimeout)
		}

		stdoutStr := stdout.String()
		stderrStr := stderr.String()
		diagnostics := stderrStr
		if strings.TrimSpace(diagnostics) == "" {
			diagnostics = stdoutStr
		}
		// If we haven't already forced no-db, check if we should
		if allowNoDB && !getNoDBState(dir) && !containsString(args, "--no-db") && isNoBeadsDBError(stderrStr, stdoutStr) {
			setNoDBState(dir, true)
			args = append([]string{"--no-db"}, stripFlagWithValue(args, "--lock-timeout")...)
			attempt = 0
			continue
		}
		if isTransientBeadsDBError(stderrStr, stdoutStr) {
			delay, retry := transientBeadsRetryDelay(attempt, maxAttempts, retryDeadline)
			if !retry {
				return "", fmt.Errorf(
					"br %s: workspace still contended after %d attempt(s): %w: %s",
					strings.Join(args, " "), attempt, err, diagnostics,
				)
			}
			if err := waitForBeadsRetry(parent, delay); err != nil {
				return "", err
			}
			continue
		}
		return "", fmt.Errorf("br %s: %w: %s", strings.Join(args, " "), err, diagnostics)
	}

	return "", fmt.Errorf("br %s: exceeded retry budget", strings.Join(args, " "))
}

// beadsRetryWaitTestObserver lets in-package tests synchronize at the exact
// backoff boundary without exposing a public or process-global hook.
type beadsRetryWaitTestObserver interface {
	observeBeadsRetryWaitForTest(time.Duration)
}

func waitForBeadsRetry(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return errors.New("beads retry context is required")
	}
	if delay <= 0 {
		return ctx.Err()
	}
	if observer, ok := ctx.(beadsRetryWaitTestObserver); ok {
		observer.observeBeadsRetryWaitForTest(delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// beadsDBCorruptionSignatures are the SQLite corruption signatures that br
// itself recognizes and auto-repairs by rebuilding the database from the
// authoritative JSONL (beads_rust routes them through
// retry_mutation_with_jsonl_recovery). NTM's guarded CAS-claim paths bypass
// br's CLI wrapper with direct SQL, so they must trigger the same recovery
// themselves instead of stalling the self-feeder on every scan (#227).
var beadsDBCorruptionSignatures = []string{
	"database disk image is malformed",
	"malformed database schema",
	"file is not a database",
	"database corruption",
}

// isBeadsDBCorruptionError reports whether err carries a recognized SQLite
// corruption signature anywhere in its chain's message.
func isBeadsDBCorruptionError(err error) bool {
	if err == nil {
		return false
	}
	// Typed authorization/ownership refusals can contain task IDs or mutex
	// names that happen to match a SQLite diagnostic. They are not evidence
	// of database corruption and must never trigger a destructive rebuild.
	if errors.Is(err, ErrBeadAssignmentIneligible) || errors.Is(err, ErrBeadAlreadyClaimed) || errors.Is(err, ErrBeadTerminal) {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, signature := range beadsDBCorruptionSignatures {
		if strings.Contains(message, signature) {
			return true
		}
	}
	return false
}

var ErrBeadAlreadyClaimed = errors.New("bead is already claimed")

// ErrBeadTerminal means a guarded terminal-generation claim observed a closed
// or tombstoned issue and refused to let br's generic --claim path reopen it.
var ErrBeadTerminal = errors.New("bead is terminal")

// ErrBeadAssignmentIneligible means the final assignment claim transaction
// observed a work item that was not open and free of automation gates.
var ErrBeadAssignmentIneligible = errors.New("bead is not eligible for automated assignment")

// defaultOperatorGatedLabels is the built-in vocabulary of labels that mark a
// bead as requiring a human or operator decision before automated assignment.
// Projects with their own gating vocabulary extend (never replace) this set via
// `[assign] operator_gated_labels` in config.toml (#223); the merged set is
// installed process-wide through ConfigureOperatorGatedLabels.
var defaultOperatorGatedLabels = []string{
	"blocked-on-ivan",
	"operator-gated",
	"operator-action",
	"needs-operator",
	"human-gated",
	"human-input",
	"business-input",
	"blocked-on-operator",
}

var (
	operatorGatedMu     sync.RWMutex
	operatorGatedLabels = buildOperatorGatedSet(nil)
	// projectOperatorGatedLabels keeps assignment policy isolated by the
	// authoritative Beads workspace. Automated assignment commands register
	// the strictly loaded merged policy before planning or claiming, so two
	// projects in the same process cannot overwrite one another's gates.
	projectOperatorGatedLabels sync.Map // map[string]map[string]struct{}
)

// buildOperatorGatedSet merges the built-in defaults with normalized
// (lowercased, trimmed) extra labels. Empty entries are dropped.
func buildOperatorGatedSet(extra []string) map[string]struct{} {
	set := make(map[string]struct{}, len(defaultOperatorGatedLabels)+len(extra))
	for _, label := range defaultOperatorGatedLabels {
		set[label] = struct{}{}
	}
	for _, label := range extra {
		normalized := strings.ToLower(strings.TrimSpace(label))
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}
	return set
}

func canonicalOperatorGatedLabels(extra []string) []string {
	set := buildOperatorGatedSet(extra)
	labels := make([]string, 0, len(set))
	for label := range set {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// ConfigureOperatorGatedLabels installs the process-wide operator-gate
// vocabulary: the built-in defaults merged with the configured extra labels
// (`[assign] operator_gated_labels`). Extras extend the defaults — they can
// never un-gate a built-in label. Matching is case-insensitive.
func ConfigureOperatorGatedLabels(extra []string) {
	set := buildOperatorGatedSet(extra)
	operatorGatedMu.Lock()
	operatorGatedLabels = set
	operatorGatedMu.Unlock()
}

// ConfigureProjectOperatorGatedLabels installs the operator-gate vocabulary
// for one authoritative project. The stored set is immutable after publication
// and may be replaced atomically when that same project's configuration is
// reloaded.
func ConfigureProjectOperatorGatedLabels(projectDir string, extra []string) error {
	key, err := operatorGateProjectKey(projectDir)
	if err != nil {
		return err
	}
	projectOperatorGatedLabels.Store(key, buildOperatorGatedSet(extra))
	return nil
}

func operatorGateProjectKey(projectDir string) (string, error) {
	resolved, err := normalizeTriageDir(strings.TrimSpace(projectDir))
	if err != nil {
		return "", fmt.Errorf("resolve operator-gate project directory %q: %w", projectDir, err)
	}
	return filepath.Clean(resolved), nil
}

func operatorGatedLabelsForProject(projectDir string) map[string]struct{} {
	if key, err := operatorGateProjectKey(projectDir); err == nil {
		if stored, ok := projectOperatorGatedLabels.Load(key); ok {
			if labels, valid := stored.(map[string]struct{}); valid {
				return labels
			}
		}
	}
	operatorGatedMu.RLock()
	labels := make(map[string]struct{}, len(operatorGatedLabels))
	for label := range operatorGatedLabels {
		labels[label] = struct{}{}
	}
	operatorGatedMu.RUnlock()
	return labels
}

// OperatorGatedLabelsForProject returns the canonical gate vocabulary for one
// authoritative project. It falls back to the compatibility process policy
// only when the project has not yet registered a strict policy snapshot.
func OperatorGatedLabelsForProject(projectDir string) []string {
	set := operatorGatedLabelsForProject(projectDir)
	labels := make([]string, 0, len(set))
	for label := range set {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// IsOperatorGatedLabelForProject reports whether label blocks automated
// assignment under the exact authoritative project's configured policy.
func IsOperatorGatedLabelForProject(projectDir, label string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	_, gated := operatorGatedLabelsForProject(projectDir)[normalized]
	return gated
}

// IsContainerBeadType reports whether an issue type is a container/grouping
// node rather than a unit of implementation work. Containers are never
// auto-dispatched into worker panes (GH#242 / ntm-e165).
//
// This lives in bv, next to the operator-label gate, because it is the same
// kind of rule — part of the assignment authorization boundary — and it has to
// mean the same thing on every path that can dispatch work. It previously
// existed only in the CLI classifier, so `ntm coordinator run` happily claimed
// and mailed open epics that `ntm assign` refuses. Unlike the label gate, this
// exclusion is intrinsic to the issue type and no label can lift it.
func IsContainerBeadType(issueType string) bool {
	return strings.EqualFold(strings.TrimSpace(issueType), "epic")
}

// IsOperatorGatedLabelInPolicy evaluates a label against the built-in gates
// plus one immutable configured extras snapshot.
func IsOperatorGatedLabelInPolicy(label string, extra []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(label))
	_, gated := buildOperatorGatedSet(extra)[normalized]
	return gated
}

// OperatorGatedLabels returns the canonical normalized labels that require a
// human or operator decision before automated assignment.
func OperatorGatedLabels() []string {
	operatorGatedMu.RLock()
	labels := make([]string, 0, len(operatorGatedLabels))
	for label := range operatorGatedLabels {
		labels = append(labels, label)
	}
	operatorGatedMu.RUnlock()
	sort.Strings(labels)
	return labels
}

// AssignmentEligibilityError reports the exact tracker preconditions that
// rejected an automated assignment at the claim transaction boundary.
type AssignmentEligibilityError struct {
	BeadID             string
	Status             string
	UnresolvedBlockers []string
	OperatorLabels     []string
	Deferred           bool
	Pinned             bool
	Ephemeral          bool
	Template           bool
	Wisp               bool
}

func (e *AssignmentEligibilityError) Error() string {
	if e == nil {
		return ErrBeadAssignmentIneligible.Error()
	}
	reasons := make([]string, 0, 3)
	if status := strings.TrimSpace(e.Status); !strings.EqualFold(status, "open") {
		reasons = append(reasons, fmt.Sprintf("status is %q", status))
	}
	if len(e.UnresolvedBlockers) > 0 {
		reasons = append(reasons, "unresolved blockers: "+strings.Join(e.UnresolvedBlockers, ", "))
	}
	if len(e.OperatorLabels) > 0 {
		reasons = append(reasons, "operator-gated labels: "+strings.Join(e.OperatorLabels, ", "))
	}
	if e.Deferred {
		reasons = append(reasons, "work is deferred")
	}
	if e.Pinned {
		reasons = append(reasons, "work is pinned")
	}
	if e.Ephemeral {
		reasons = append(reasons, "work is ephemeral")
	}
	if e.Template {
		reasons = append(reasons, "work is a template")
	}
	if e.Wisp {
		reasons = append(reasons, "work is a wisp")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "assignment eligibility changed")
	}
	return fmt.Sprintf("%s: %s: %s", ErrBeadAssignmentIneligible, strings.TrimSpace(e.BeadID), strings.Join(reasons, "; "))
}

func (e *AssignmentEligibilityError) Unwrap() error {
	return ErrBeadAssignmentIneligible
}

// BeadClaimResult is the validated output of br's atomic claim operation.
type BeadClaimResult struct {
	ID        string
	Title     string
	Actor     string
	Status    string
	ClaimedAt time.Time
}

// ClaimBeadForAssignment checks every authoritative automation gate and claims
// the bead with br's compare-and-set (nhm: see claimBeadForAssignmentAttempt).
// It is intentionally limited to assignment paths.
func ClaimBeadForAssignment(ctx context.Context, dir, beadID, actor string) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	return ClaimBeadForAssignmentWithOperatorGatedLabels(
		ctx, dir, beadID, actor, OperatorGatedLabelsForProject(dir),
	)
}

// ClaimBeadForAssignmentWithOperatorGatedLabels performs the final guarded
// claim using an immutable policy snapshot captured by the authoritative
// command. Passing the vocabulary explicitly prevents a concurrent project
// from changing the gates between read-only authorization and this CAS.
func ClaimBeadForAssignmentWithOperatorGatedLabels(ctx context.Context, dir, beadID, actor string, gatedLabels []string) (BeadClaimResult, error) {
	if err := requireBeadsMutationContext(ctx); err != nil {
		return BeadClaimResult{}, err
	}
	beadID = strings.TrimSpace(beadID)
	actor = strings.TrimSpace(actor)
	if beadID == "" {
		return BeadClaimResult{}, errors.New("bead ID is required")
	}
	if actor == "" {
		return BeadClaimResult{}, errors.New("claim actor is required")
	}
	normalizedDir, err := normalizeTriageDir(dir)
	if err != nil {
		return BeadClaimResult{}, err
	}
	// nhm: no automatic JSONL rebuild on a corruption error (claimWriteError
	// names br's own recovery instead).
	return claimBeadForAssignmentAttempt(ctx, normalizedDir, beadID, actor, gatedLabels)
}

// claimBeadForAssignmentAttempt is a single guarded assignment-claim attempt.
//
// nhm: ntm commits this claim as one BEGIN IMMEDIATE transaction written
// straight into beads.db, then flushes and repairs the content hash with a
// second SQLite write. On br 0.7.4 (frankensqlite with WAL certificates) a
// foreign commit followed by any plain-SQLite reader, such as bv on every
// triage, leaves a WAL index that br refuses until `br doctor migrate-schema
// recover` (ACFS acfs-f2mc.2 closure record). So nhm keeps ntm's checks and
// lets br's own engine write. Under the project's claim lock it reads the bead
// once (br show --json), applies ntm's eligibility validation and mutex check
// to that read, and claims with `br update --claim --if-unchanged <updated_at>`.
// A label, dependency, status, defer or assignee change moves updated_at, so
// br writes nothing when the read went stale (exit 6), and br refuses a
// blocked issue itself. The lock serializes nhm claimers, so a later claimer
// sees an earlier mutex holder, as ntm's transaction did. Direct tracker edits
// are not fenced, as in ntm, with one narrower window: ntm's IMMEDIATE
// transaction also kept a direct `br update --claim` of a mutex peer out of
// the time between its check and its write; here such a claim can land
// between the holder read (br list) and the write. The states anyone can
// reach are ntm's (a direct claim can always add a second holder later).
//
// A write br refused without writing because the bead moved since the read
// (exit 6) or names an assignee is decided again from a new read under the
// same lock (claimTries in all); one that still moves leaves the outcome
// unknown. The write itself never retries (runBrClaimWrite).
func claimBeadForAssignmentAttempt(ctx context.Context, normalizedDir, beadID, actor string, gatedLabels []string) (BeadClaimResult, error) {
	unlock, err := lockProjectClaims(ctx, normalizedDir)
	if err != nil {
		return BeadClaimResult{}, err
	}
	defer unlock()

	var moved error
	for try := 0; try < claimTries; try++ {
		result, err := claimBeadUnderLock(ctx, normalizedDir, beadID, actor, gatedLabels)
		if !errors.Is(err, errClaimRecordMoved) {
			return result, err
		}
		moved = err
	}
	return BeadClaimResult{}, fmt.Errorf("claim %s: the bead changed between each read and write (%d tries); the claim's outcome is left unknown: %w",
		beadID, claimTries, moved)
}

// claimBeadUnderLock is one read, check and write of the claim, under the
// project's claim lock.
func claimBeadUnderLock(ctx context.Context, normalizedDir, beadID, actor string, gatedLabels []string) (BeadClaimResult, error) {
	output, err := runBdContext(ctx, normalizedDir, false, "show", beadID, "--json")
	if err != nil {
		return BeadClaimResult{}, claimReadError(beadID, err)
	}
	details, err := parseBeadAssignmentDetailsOutput(output)
	if err != nil {
		return BeadClaimResult{}, err
	}
	updatedAt, err := parseBeadShowUpdatedAt(output)
	if err != nil {
		return BeadClaimResult{}, err
	}
	if liveID := strings.TrimSpace(details.ID); liveID != beadID {
		return BeadClaimResult{}, fmt.Errorf("br show returned bead %q, want %q", liveID, beadID)
	}

	status := strings.ToLower(strings.TrimSpace(details.Status))
	currentAssignee := strings.TrimSpace(details.Assignee)
	if currentAssignee != "" && currentAssignee != actor {
		return BeadClaimResult{}, fmt.Errorf("%w: issue %s already assigned to %s", ErrBeadAlreadyClaimed, beadID, currentAssignee)
	}
	if status == "in_progress" && currentAssignee != actor {
		return BeadClaimResult{}, fmt.Errorf("%w: issue %s is already in progress", ErrBeadAlreadyClaimed, beadID)
	}
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, BeadAssignmentAuthorization{
		BeadID: beadID, ExpectedAssignee: actor,
		AllowUnassignedOpen: true, AllowOwnedOpen: true, AllowOwnedInProgress: true,
	}, canonicalOperatorGatedLabels(gatedLabels)); err != nil {
		var eligibility *AssignmentEligibilityError
		if errors.As(err, &eligibility) {
			return BeadClaimResult{}, err
		}
		// A status the claim cannot start from (closed, deferred, blocked):
		// ntm's transaction reports it as an eligibility error naming it.
		return BeadClaimResult{}, &AssignmentEligibilityError{BeadID: beadID, Status: details.Status}
	}
	if err := requireAssignmentMutexes(ctx, normalizedDir, beadID, details.Labels); err != nil {
		return BeadClaimResult{}, err
	}
	if status == "in_progress" && currentAssignee == actor {
		// The same actor's retry: the claim already holds, nothing to write.
		return BeadClaimResult{ID: beadID, Title: details.Title, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
	}

	claimArgs := append(beadClaimArgs(beadID, actor), "--if-unchanged", updatedAt)
	claimOutput, err := runBrClaimWrite(ctx, normalizedDir, claimArgs...)
	if err != nil {
		return claimAfterWriteError(ctx, normalizedDir, beadID, actor, err)
	}
	result, err := parseBeadClaimOutput(claimOutput)
	if err != nil {
		return BeadClaimResult{}, err
	}
	if result.ID != beadID {
		return BeadClaimResult{}, fmt.Errorf("atomic claim returned bead %q, want %q", result.ID, beadID)
	}
	result.Actor = actor
	result.ClaimedAt = time.Now().UTC()
	return result, nil
}

func requireBeadsMutationContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("beads mutation context is required")
	}
	return ctx.Err()
}

func beadClaimArgs(beadID, actor string) []string {
	return []string{"update", beadID, "--claim", "--actor", actor, "--json"}
}

func parseBeadClaimOutput(output string) (BeadClaimResult, error) {
	type claimRow struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	var rows []claimRow
	trimmed := strings.TrimSpace(output)
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		// Under a workflow capacity soft limit br wraps the rows as
		// {"updated":[...],"warnings":[...]}; the claim itself succeeded.
		var envelope struct {
			Updated []claimRow `json:"updated"`
		}
		if envErr := json.Unmarshal([]byte(trimmed), &envelope); envErr != nil || envelope.Updated == nil {
			return BeadClaimResult{}, fmt.Errorf("parse atomic claim output: %w", err)
		}
		rows = envelope.Updated
	}
	if len(rows) != 1 {
		return BeadClaimResult{}, fmt.Errorf("atomic claim returned %d rows, want 1", len(rows))
	}
	if strings.TrimSpace(rows[0].ID) == "" {
		return BeadClaimResult{}, errors.New("atomic claim response is missing id")
	}
	if !strings.EqualFold(strings.TrimSpace(rows[0].Status), "in_progress") {
		return BeadClaimResult{}, fmt.Errorf("atomic claim status is %q, want in_progress", rows[0].Status)
	}
	return BeadClaimResult{ID: rows[0].ID, Title: rows[0].Title, Status: "in_progress"}, nil
}

type brListEnvelope[T any] struct {
	Issues []T `json:"issues"`
	Beads  []T `json:"beads"`
	Items  []T `json:"items"`
}

// UnmarshalBdList parses list-style br JSON that may be either a raw array or
// an envelope object such as {"issues":[...]}.
func UnmarshalBdList[T any](output string) ([]T, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" || trimmed == "null" {
		return []T{}, nil
	}

	var items []T
	if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
		if items == nil {
			return []T{}, nil
		}
		return items, nil
	}

	var wrapped brListEnvelope[T]
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil {
		switch {
		case len(wrapped.Issues) > 0:
			return wrapped.Issues, nil
		case len(wrapped.Beads) > 0:
			return wrapped.Beads, nil
		case len(wrapped.Items) > 0:
			return wrapped.Items, nil
		}
		// An envelope whose list key is present but EMPTY means "no results",
		// not "not an envelope". Falling through to the single-object branch
		// below unmarshalled the envelope itself into T — unknown fields are
		// ignored, so it always succeeded — and returned one phantom
		// zero-valued item. `br list --status=X --json` answers
		// {"issues":[],"total":0,...} for an empty result, so every caller
		// with no matching beads got one blank bead: the alert generator
		// emitted a stale-bead alert for ID "" (zero UpdatedAt is always
		// older than any threshold) and the REST beads endpoint returned the
		// envelope as a bead.
		if isEmptyBrListEnvelope(trimmed) {
			return []T{}, nil
		}
	}

	var single T
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil {
		// Prove the payload actually populated T before treating it as one
		// item. json.Unmarshal ignores unknown fields, so decoding ANY JSON
		// object into T succeeds and yields a zero value — which is how an
		// unrecognized envelope became a single phantom bead with ID "".
		// isEmptyBrListEnvelope above closes that for the three known list
		// keys; this closes the whole class, including a future or renamed
		// envelope key and an error object emitted on stdout with exit 0.
		var zero T
		if !reflect.DeepEqual(single, zero) {
			return []T{single}, nil
		}
		return []T{}, nil
	}

	return nil, fmt.Errorf("parse br list output: %s", trimmed)
}

// isEmptyBrListEnvelope reports whether the payload is a br list envelope
// whose list key is present but empty (or null) — i.e. a genuine "no results"
// answer rather than some other JSON object.
func isEmptyBrListEnvelope(payload string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return false
	}
	for _, key := range []string{"issues", "beads", "items"} {
		raw, ok := probe[key]
		if !ok {
			continue
		}
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			continue
		}
		return len(list) == 0
	}
	return false
}

func isNoBeadsDBError(streams ...string) bool {
	s := strings.ToLower(strings.Join(streams, "\n"))
	return strings.Contains(s, "no beads database found") || strings.Contains(s, "use 'br --no-db'")
}

// isTransientBeadsDBError reports whether a failed br/bv invocation died on
// workspace contention that a later attempt can reasonably expect to clear:
//
//   - SQLite "database is busy" / "database is locked" (another writer holds
//     the SQLite lock);
//   - br's cross-process workspace write lock timing out ("Timed out after
//     Nms waiting for write lock (...) at .beads/.write.lock"). br reports
//     this as a CONFIG_ERROR, but it is contention, not configuration: the
//     command never ran, so retrying it is safe for reads and for
//     compare-and-set mutations alike.
//
// Every other diagnostic (including other CONFIG_ERRORs such as a genuinely
// unwritable lock file) is terminal and must not be retried.
func isTransientBeadsDBError(streams ...string) bool {
	s := strings.ToLower(strings.Join(streams, "\n"))
	if strings.Contains(s, "database is busy") || strings.Contains(s, "database is locked") {
		return true
	}
	return strings.Contains(s, "timed out after") && strings.Contains(s, "waiting for write lock")
}

const (
	// transientBeadsDBBackoffBase is the first retry delay after workspace
	// contention; each later attempt doubles it up to transientBeadsDBBackoffCap.
	transientBeadsDBBackoffBase = 50 * time.Millisecond
	transientBeadsDBBackoffCap  = 800 * time.Millisecond
)

// transientBeadsRetryBudget bounds the total wall-clock time one br/bv
// invocation may spend retrying workspace contention, measured from the first
// attempt. It is a var so tests can shrink it; production code never writes it.
var transientBeadsRetryBudget = 20 * time.Second

// transientBeadsDBBackoffBounds returns the inclusive [min, max] delay window
// for the given 1-based attempt: exponential growth capped at
// transientBeadsDBBackoffCap, with the lower bound at half the ceiling so
// jitter spreads contending processes without collapsing to zero.
func transientBeadsDBBackoffBounds(attempt int) (time.Duration, time.Duration) {
	if attempt < 1 {
		attempt = 1
	}
	ceiling := transientBeadsDBBackoffBase
	for i := 1; i < attempt && ceiling < transientBeadsDBBackoffCap; i++ {
		ceiling *= 2
	}
	if ceiling > transientBeadsDBBackoffCap {
		ceiling = transientBeadsDBBackoffCap
	}
	return ceiling / 2, ceiling
}

// transientBeadsDBBackoff returns the jittered delay before retrying the
// given 1-based attempt, uniformly drawn from transientBeadsDBBackoffBounds.
func transientBeadsDBBackoff(attempt int) time.Duration {
	low, high := transientBeadsDBBackoffBounds(attempt)
	if high <= low {
		return low
	}
	return low + rand.N(high-low+1)
}

// transientBeadsRetryDelay decides whether a transient failure on the given
// attempt should be retried and, if so, how long to wait first. It returns
// false when the attempt budget is spent or when the next delay would push the
// invocation past deadline; the caller then surfaces the last br error.
func transientBeadsRetryDelay(attempt, maxAttempts int, deadline time.Time) (time.Duration, bool) {
	if attempt >= maxAttempts {
		return 0, false
	}
	delay := transientBeadsDBBackoff(attempt)
	if time.Until(deadline) <= delay {
		return 0, false
	}
	return delay, true
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func stripFlagWithValue(args []string, flag string) []string {
	if len(args) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			filtered = append(filtered, args[i])
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
		}
	}
	return filtered
}

// GetBeadStatus returns the current status for a bead ID using br show --json.
func GetBeadStatus(dir, beadID string) (string, error) {
	return GetBeadStatusContext(context.Background(), dir, beadID)
}

// GetBeadStatusContext returns exact bead status with caller cancellation.
func GetBeadStatusContext(ctx context.Context, dir, beadID string) (string, error) {
	if strings.TrimSpace(beadID) == "" {
		return "", errors.New("bead ID is required")
	}

	output, err := RunBdContext(ctx, dir, "show", beadID, "--json")
	if err != nil {
		return "", err
	}
	return parseBeadStatusOutput(output)
}

// BeadAssignmentDetails is the exact work-item state assignment gates need.
// Unlike triage output, br show is keyed by the requested ID and is not capped
// or ranking-dependent.
type BeadAssignmentDetails struct {
	ID    string
	Title string
	// Description is the bead body. Assignment reads the paths it must
	// reserve from it as well as from the title (ntm#336).
	Description          string
	IssueType            string
	Status               string
	Priority             int
	Assignee             string
	DeferUntil           *time.Time
	Pinned               bool
	Ephemeral            bool
	Template             bool
	Wisp                 bool
	Labels               []string
	BlockedBy            []string
	BlockingDependencies []BeadDependencyState
}

// BeadAssignmentAuthorization defines the tracker ownership states an atomic
// assignment generation may accept. Automation gates are never relaxed by
// these state allowances.
type BeadAssignmentAuthorization struct {
	BeadID                    string
	ExpectedAssignee          string
	AllowUnassignedOpen       bool
	AllowOwnedOpen            bool
	AllowUnassignedInProgress bool
	AllowOwnedInProgress      bool
}

// ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels validates against
// the immutable vocabulary captured by the caller at strict policy admission.
// The same slice can then be passed to the guarded claim, keeping both sides of
// the atomic boundary on an identical policy snapshot.
func ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details *BeadAssignmentDetails, authorization BeadAssignmentAuthorization, gatedLabels []string) error {
	set := buildOperatorGatedSet(gatedLabels)
	return validateBeadAssignmentAuthorization(details, authorization, func(label string) bool {
		_, gated := set[strings.ToLower(strings.TrimSpace(label))]
		return gated
	})
}

func validateBeadAssignmentAuthorization(details *BeadAssignmentDetails, authorization BeadAssignmentAuthorization, operatorGated func(string) bool) error {
	beadID := strings.TrimSpace(authorization.BeadID)
	if beadID == "" {
		return fmt.Errorf("%w: requested bead ID is empty", ErrBeadAssignmentIneligible)
	}
	if details == nil {
		return fmt.Errorf("%w: live details for %s are missing", ErrBeadAssignmentIneligible, beadID)
	}
	liveID := strings.TrimSpace(details.ID)
	if liveID != beadID {
		return fmt.Errorf("%w: live details returned bead %q, want %q", ErrBeadAssignmentIneligible, liveID, beadID)
	}

	blockerSet := make(map[string]struct{}, len(details.BlockedBy)+len(details.BlockingDependencies))
	for _, blockerID := range details.BlockedBy {
		if blockerID = strings.TrimSpace(blockerID); blockerID != "" {
			blockerSet[blockerID] = struct{}{}
		}
	}
	for _, dependency := range details.BlockingDependencies {
		status := strings.ToLower(strings.TrimSpace(dependency.Status))
		if status == "closed" || status == "tombstone" {
			continue
		}
		if blockerID := strings.TrimSpace(dependency.ID); blockerID != "" {
			blockerSet[blockerID] = struct{}{}
		}
	}
	blockers := make([]string, 0, len(blockerSet))
	for blockerID := range blockerSet {
		blockers = append(blockers, blockerID)
	}
	sort.Strings(blockers)

	operatorLabels := make([]string, 0, len(details.Labels))
	for _, label := range details.Labels {
		if operatorGated(label) {
			operatorLabels = append(operatorLabels, strings.TrimSpace(label))
		}
	}
	sort.Strings(operatorLabels)
	deferred := details.DeferUntil != nil && details.DeferUntil.After(time.Now().UTC())
	wisp := details.Wisp || strings.Contains(strings.ToLower(liveID), "-wisp-")
	if len(blockers) > 0 || len(operatorLabels) > 0 || deferred || details.Pinned || details.Ephemeral || details.Template || wisp {
		return &AssignmentEligibilityError{
			BeadID: liveID, Status: details.Status,
			UnresolvedBlockers: blockers, OperatorLabels: operatorLabels,
			Deferred: deferred, Pinned: details.Pinned, Ephemeral: details.Ephemeral,
			Template: details.Template, Wisp: wisp,
		}
	}

	status := strings.ToLower(strings.TrimSpace(details.Status))
	status = strings.ReplaceAll(status, "-", "_")
	status = strings.ReplaceAll(status, " ", "_")
	assignee := strings.TrimSpace(details.Assignee)
	expectedAssignee := strings.TrimSpace(authorization.ExpectedAssignee)
	switch status {
	case "open":
		if assignee == "" && authorization.AllowUnassignedOpen {
			return nil
		}
		if expectedAssignee != "" && assignee == expectedAssignee && authorization.AllowOwnedOpen {
			return nil
		}
	case "in_progress":
		if assignee == "" && authorization.AllowUnassignedInProgress {
			return nil
		}
		if expectedAssignee != "" && assignee == expectedAssignee && authorization.AllowOwnedInProgress {
			return nil
		}
	}
	return fmt.Errorf("%w: bead %s live status %q and assignee %q do not match the authorized generation state for %q", ErrBeadAssignmentIneligible, liveID, details.Status, details.Assignee, expectedAssignee)
}

type beadShowDependency struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	DependencyType string `json:"dependency_type"`
}

type beadShowAssignmentRow struct {
	ID           string               `json:"id"`
	Title        string               `json:"title"`
	Description  string               `json:"description"`
	IssueType    string               `json:"issue_type"`
	Status       string               `json:"status"`
	Priority     int                  `json:"priority"`
	Assignee     string               `json:"assignee"`
	DeferUntil   *time.Time           `json:"defer_until"`
	Pinned       bool                 `json:"pinned"`
	Ephemeral    bool                 `json:"ephemeral"`
	Template     bool                 `json:"is_template"`
	Labels       []string             `json:"labels"`
	Dependencies []beadShowDependency `json:"dependencies"`
	Dependents   []beadShowDependency `json:"dependents"`
}

// BeadDependencyState is one blocking dependency exactly as reported by
// br show. Terminal dependencies are retained so callers can prove that a
// previously blocked bead became unblocked instead of mistaking filtered data
// for an empty dependency set.
type BeadDependencyState struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// BeadDependentState is one local work item blocked by the inspected bead.
// External dependency endpoints are intentionally omitted because they cannot
// be validated or assigned through the local Beads database.
type BeadDependentState struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority int    `json:"priority"`
}

// IsBlockingDependencyType reports whether a Beads relationship participates
// in readiness. Keep this set aligned with the guarded assignment claim query.
func IsBlockingDependencyType(dependencyType string) bool {
	switch strings.ToLower(strings.TrimSpace(dependencyType)) {
	case "blocks", "conditional-blocks", "waits-for":
		return true
	default:
		return false
	}
}

// GetBeadAssignmentDetailsContext returns exact title, status, assignee,
// labels, and unresolved blocking dependencies for one bead using
// br show --json, with caller cancellation.
func GetBeadAssignmentDetailsContext(ctx context.Context, dir, beadID string) (*BeadAssignmentDetails, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil, errors.New("bead ID is required")
	}
	output, err := RunBdContext(ctx, dir, "show", beadID, "--json")
	if err != nil {
		return nil, err
	}
	details, err := parseBeadAssignmentDetailsOutput(output)
	if err != nil {
		return nil, err
	}
	if details.ID != beadID {
		return nil, fmt.Errorf("br show returned bead %q, want %q", details.ID, beadID)
	}
	return details, nil
}

// BeadTextReader narrows a live assignment-details reader to a bead's title
// and description, the shape reservation discovery reads
// (assign.DiscoverReservationPaths), so `ntm assign` and the robot assignment
// surfaces discover a bead's file scope from the same read.
func BeadTextReader(read func(context.Context, string, string) (*BeadAssignmentDetails, error)) func(context.Context, string, string) (string, string, error) {
	return func(ctx context.Context, dir, beadID string) (string, string, error) {
		if read == nil {
			return "", "", errors.New("bead reader is not configured")
		}
		details, err := read(ctx, dir, beadID)
		if err != nil {
			return "", "", err
		}
		if details == nil {
			return "", "", errors.New("live work-item details are missing")
		}
		return details.Title, details.Description, nil
	}
}

// GetBeadBlockingDependentsContext returns every local work item whose
// readiness depends on beadID. The source is one uncapped br show response,
// rather than a ranked triage list, so no dependent is dropped by a limit.
func GetBeadBlockingDependentsContext(ctx context.Context, dir, beadID string) ([]BeadDependentState, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return nil, errors.New("bead ID is required")
	}
	output, err := RunBdContext(ctx, dir, "show", beadID, "--json")
	if err != nil {
		return nil, err
	}
	row, err := parseBeadShowAssignmentRow(output)
	if err != nil {
		return nil, err
	}
	if row.ID != beadID {
		return nil, fmt.Errorf("br show returned bead %q, want %q", row.ID, beadID)
	}
	return blockingDependentStates(row.Dependents)
}

func parseBeadAssignmentDetailsOutput(output string) (*BeadAssignmentDetails, error) {
	row, err := parseBeadShowAssignmentRow(output)
	if err != nil {
		return nil, err
	}

	dependencyStates, err := blockingDependencyStates(row.Dependencies)
	if err != nil {
		return nil, err
	}
	blockedBy := make([]string, 0, len(dependencyStates))
	for _, dependency := range dependencyStates {
		status := strings.ToLower(strings.TrimSpace(dependency.Status))
		if status == "closed" || status == "tombstone" {
			continue
		}
		blockedBy = append(blockedBy, dependency.ID)
	}

	labels := make([]string, 0, len(row.Labels))
	seenLabels := make(map[string]struct{}, len(row.Labels))
	for _, rawLabel := range row.Labels {
		label := strings.TrimSpace(rawLabel)
		if label == "" {
			continue
		}
		if _, duplicate := seenLabels[label]; duplicate {
			continue
		}
		seenLabels[label] = struct{}{}
		labels = append(labels, label)
	}
	sort.Strings(labels)

	return &BeadAssignmentDetails{
		ID: row.ID, Title: row.Title, Description: row.Description, IssueType: row.IssueType, Status: row.Status, Priority: row.Priority,
		Assignee: row.Assignee, DeferUntil: row.DeferUntil, Pinned: row.Pinned, Ephemeral: row.Ephemeral,
		Template: row.Template, Wisp: strings.Contains(strings.ToLower(row.ID), "-wisp-"), Labels: labels,
		BlockedBy: blockedBy, BlockingDependencies: dependencyStates,
	}, nil
}

func parseBeadShowAssignmentRow(output string) (beadShowAssignmentRow, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return beadShowAssignmentRow{}, errors.New("empty bead output")
	}

	var row beadShowAssignmentRow
	var rows []beadShowAssignmentRow
	if err := json.Unmarshal([]byte(trimmed), &rows); err == nil {
		if len(rows) != 1 {
			return beadShowAssignmentRow{}, fmt.Errorf("br show returned %d beads, want exactly 1", len(rows))
		}
		row = rows[0]
	} else if err := json.Unmarshal([]byte(trimmed), &row); err != nil {
		return beadShowAssignmentRow{}, fmt.Errorf("parse bead assignment details: %w", err)
	}

	row.ID = strings.TrimSpace(row.ID)
	row.Title = strings.TrimSpace(row.Title)
	row.IssueType = strings.TrimSpace(row.IssueType)
	row.Status = strings.TrimSpace(row.Status)
	row.Assignee = strings.TrimSpace(row.Assignee)
	if row.ID == "" {
		return beadShowAssignmentRow{}, errors.New("bead ID field not found in br show response")
	}
	if row.Status == "" {
		return beadShowAssignmentRow{}, errors.New("bead status field not found in br show response")
	}
	return row, nil
}

func blockingDependencyStates(dependencies []beadShowDependency) ([]BeadDependencyState, error) {
	statesByID := make(map[string]string, len(dependencies))
	for _, dependency := range dependencies {
		if !IsBlockingDependencyType(dependency.DependencyType) {
			continue
		}
		dependencyID := strings.TrimSpace(dependency.ID)
		if dependencyID == "" {
			return nil, errors.New("blocking dependency has no bead ID")
		}
		status := strings.ToLower(strings.TrimSpace(dependency.Status))
		if status == "" {
			return nil, fmt.Errorf("blocking dependency %s has no status", dependencyID)
		}
		if existing, duplicate := statesByID[dependencyID]; duplicate && existing != status {
			return nil, fmt.Errorf("blocking dependency %s has conflicting statuses %q and %q", dependencyID, existing, status)
		}
		statesByID[dependencyID] = status
	}
	states := make([]BeadDependencyState, 0, len(statesByID))
	for dependencyID, status := range statesByID {
		states = append(states, BeadDependencyState{ID: dependencyID, Status: status})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	return states, nil
}

func blockingDependentStates(dependents []beadShowDependency) ([]BeadDependentState, error) {
	statesByID := make(map[string]BeadDependentState, len(dependents))
	for _, dependent := range dependents {
		if !IsBlockingDependencyType(dependent.DependencyType) {
			continue
		}
		dependentID := strings.TrimSpace(dependent.ID)
		if strings.HasPrefix(strings.ToLower(dependentID), "external:") {
			continue
		}
		if dependentID == "" {
			return nil, errors.New("blocking dependent has no bead ID")
		}
		status := strings.ToLower(strings.TrimSpace(dependent.Status))
		if status == "" {
			return nil, fmt.Errorf("blocking dependent %s has no status", dependentID)
		}
		state := BeadDependentState{
			ID: dependentID, Title: strings.TrimSpace(dependent.Title), Status: status, Priority: dependent.Priority,
		}
		if existing, duplicate := statesByID[dependentID]; duplicate && existing != state {
			return nil, fmt.Errorf("blocking dependent %s has conflicting rows", dependentID)
		}
		statesByID[dependentID] = state
	}
	states := make([]BeadDependentState, 0, len(statesByID))
	for _, state := range statesByID {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })
	return states, nil
}

func parseBeadStatusOutput(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", errors.New("empty bead output")
	}

	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		if len(arr) == 0 {
			return "", errors.New("empty bead response array")
		}
		if status, ok := extractStatusField(arr[0]); ok {
			return status, nil
		}
		return "", errors.New("status field not found in bead response")
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return "", fmt.Errorf("parse bead status: %w", err)
	}
	if status, ok := extractStatusField(obj); ok {
		return status, nil
	}
	return "", errors.New("status field not found in bead response")
}

func extractStatusField(payload map[string]interface{}) (string, bool) {
	raw, ok := payload["status"]
	if !ok {
		return "", false
	}
	status, ok := raw.(string)
	if !ok {
		return "", false
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return "", false
	}
	return status, true
}
