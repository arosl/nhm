// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/bv/bead_status_test.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; imports.
// TestClaimBeadForAssignmentTransactionRejectsDirectStartBlockers and
// ...RejectsEveryOperatorGate call the public claim
// (ClaimBeadForAssignmentWithOperatorGatedLabels) in place of the SQLite
// transaction, which nhm does not take; their refusals are unchanged.
// TestClaimBeadForAssignmentMirrorsStartBlockerSemantics and
// ...StatusConflictAndIdempotency run unchanged against real br. Left out
// with the code nhm does not take: the stale-adoption, non-terminal, release
// and corruption-rebuild transaction tests, their SQLite fixtures and the
// content-hash golden. The readiness-gate cases of
// ...RejectsNonReadyOpenWork are kept in claim_nhm_test.go.

package bv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestParseBeadStatusOutput_Object(t *testing.T) {
	t.Parallel()

	status, err := parseBeadStatusOutput(`{"id":"bd-123","status":"in_progress"}`)
	if err != nil {
		t.Fatalf("parseBeadStatusOutput returned error: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("status = %q, want %q", status, "in_progress")
	}
}

func TestParseBeadStatusOutput_Array(t *testing.T) {
	t.Parallel()

	status, err := parseBeadStatusOutput(`[{"id":"bd-123","status":"closed"}]`)
	if err != nil {
		t.Fatalf("parseBeadStatusOutput returned error: %v", err)
	}
	if status != "closed" {
		t.Fatalf("status = %q, want %q", status, "closed")
	}
}

func TestParseBeadStatusOutput_MissingStatus(t *testing.T) {
	t.Parallel()

	_, err := parseBeadStatusOutput(`{"id":"bd-123"}`)
	if err == nil {
		t.Fatal("expected error for missing status")
	}
}

func TestParseBeadStatusOutput_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := parseBeadStatusOutput(`{`)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseBeadAssignmentDetailsOutput(t *testing.T) {
	t.Parallel()

	details, err := parseBeadAssignmentDetailsOutput(`[{
		"id":"ntm-target",
		"title":"Exact assignment target",
		"description":"## Owned outputs\n- ` + "`docs/out.md`" + `\n",
		"status":"open",
		"priority":2,
		"assignee":"  ExactActor  ",
		"labels":["operator-gated","backend","backend"],
		"dependencies":[
			{"id":"ntm-open-b","status":"in_progress","dependency_type":"blocks"},
			{"id":"ntm-closed","status":"closed","dependency_type":"blocks"},
			{"id":"ntm-parent","status":"open","dependency_type":"parent-child"},
			{"id":"ntm-open-a","status":"open","dependency_type":"blocks"},
			{"id":"ntm-conditional","status":"open","dependency_type":"conditional-blocks"},
			{"id":"ntm-wait","status":"open","dependency_type":"waits-for"},
			{"id":"ntm-open-a","status":"open","dependency_type":"blocks"}
		]
	}]`)
	if err != nil {
		t.Fatalf("parseBeadAssignmentDetailsOutput: %v", err)
	}
	if details.ID != "ntm-target" || details.Title != "Exact assignment target" || details.Status != "open" || details.Priority != 2 || details.Assignee != "ExactActor" {
		t.Fatalf("details=%+v", details)
	}
	// ntm#336: reservation discovery reads paths from the description.
	if want := "## Owned outputs\n- `docs/out.md`\n"; details.Description != want {
		t.Fatalf("description=%q, want %q", details.Description, want)
	}
	if want := []string{"ntm-conditional", "ntm-open-a", "ntm-open-b", "ntm-wait"}; !reflect.DeepEqual(details.BlockedBy, want) {
		t.Fatalf("blocked_by=%v, want %v", details.BlockedBy, want)
	}
	if want := []string{"backend", "operator-gated"}; !reflect.DeepEqual(details.Labels, want) {
		t.Fatalf("labels=%v, want %v", details.Labels, want)
	}
	wantDependencies := []BeadDependencyState{
		{ID: "ntm-closed", Status: "closed"},
		{ID: "ntm-conditional", Status: "open"},
		{ID: "ntm-open-a", Status: "open"},
		{ID: "ntm-open-b", Status: "in_progress"},
		{ID: "ntm-wait", Status: "open"},
	}
	if !reflect.DeepEqual(details.BlockingDependencies, wantDependencies) {
		t.Fatalf("blocking dependency snapshot=%+v, want %+v", details.BlockingDependencies, wantDependencies)
	}
}

func TestParseBeadAssignmentDetailsOutputReadyGates(t *testing.T) {
	t.Parallel()

	details, err := parseBeadAssignmentDetailsOutput(`[{
		"id":"ntm-wisp-ready-gates",
		"title":"Not actually ready",
		"status":"open",
		"defer_until":"2099-01-01T00:00:00Z",
		"pinned":true,
		"ephemeral":true,
		"is_template":true
	}]`)
	if err != nil {
		t.Fatalf("parse ready gates: %v", err)
	}
	if details.DeferUntil == nil || details.DeferUntil.Year() != 2099 || !details.Pinned || !details.Ephemeral || !details.Template || !details.Wisp {
		t.Fatalf("ready gates=%+v", details)
	}
}

func TestParseBeadAssignmentDetailsOutputRejectsAmbiguousOrMalformedRows(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"empty array":           `[]`,
		"multiple rows":         `[{"id":"a","status":"open"},{"id":"b","status":"open"}]`,
		"missing id":            `{"status":"open"}`,
		"missing status":        `{"id":"a"}`,
		"missing dependency id": `{"id":"a","status":"open","dependencies":[{"status":"open","dependency_type":"blocks"}]}`,
		"invalid defer time":    `{"id":"a","status":"open","defer_until":"tomorrow-ish"}`,
		"invalid json":          `{`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseBeadAssignmentDetailsOutput(input); err == nil {
				t.Fatalf("parseBeadAssignmentDetailsOutput(%q) error=nil", input)
			}
		})
	}
}

func TestBeadTextReaderNarrowsLiveDetails(t *testing.T) {
	t.Parallel()

	var gotDir, gotBead string
	read := BeadTextReader(func(_ context.Context, dir, beadID string) (*BeadAssignmentDetails, error) {
		gotDir, gotBead = dir, beadID
		return &BeadAssignmentDetails{ID: beadID, Title: "Fix internal/a.go", Description: "## Owned outputs\n- internal/b.go\n"}, nil
	})
	title, description, err := read(t.Context(), "/project", "bd-1")
	if err != nil || title != "Fix internal/a.go" || description != "## Owned outputs\n- internal/b.go\n" || gotDir != "/project" || gotBead != "bd-1" {
		t.Fatalf("read = %q, %q, %v (dir %q bead %q)", title, description, err, gotDir, gotBead)
	}

	readErr := errors.New("br unavailable")
	for name, reader := range map[string]func(context.Context, string, string) (string, string, error){
		"read error": BeadTextReader(func(context.Context, string, string) (*BeadAssignmentDetails, error) { return nil, readErr }),
		"nil details": BeadTextReader(func(context.Context, string, string) (*BeadAssignmentDetails, error) {
			return nil, nil
		}),
		"no reader": BeadTextReader(nil),
	} {
		if _, _, err := reader(t.Context(), "/project", "bd-1"); err == nil {
			t.Fatalf("%s: err = nil, want a failed read", name)
		}
	}
}

func TestBlockingDependencyStatesRetainsTerminalDependencies(t *testing.T) {
	t.Parallel()

	states, err := blockingDependencyStates([]beadShowDependency{
		{ID: "ntm-open", Status: "open", DependencyType: "blocks"},
		{ID: "ntm-closed", Status: "closed", DependencyType: "conditional-blocks"},
		{ID: "ntm-tombstone", Status: "tombstone", DependencyType: "waits-for"},
		{ID: "ntm-progress", Status: "in_progress", DependencyType: "blocks"},
		{ID: "ntm-parent", Status: "open", DependencyType: "parent-child"},
		{ID: "ntm-closed", Status: "closed", DependencyType: "blocks"},
	})
	if err != nil {
		t.Fatalf("blockingDependencyStates: %v", err)
	}
	want := []BeadDependencyState{
		{ID: "ntm-closed", Status: "closed"},
		{ID: "ntm-open", Status: "open"},
		{ID: "ntm-progress", Status: "in_progress"},
		{ID: "ntm-tombstone", Status: "tombstone"},
	}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("dependency states=%+v, want %+v", states, want)
	}
}

func TestIsBlockingDependencyType(t *testing.T) {
	t.Parallel()
	for _, dependencyType := range []string{"blocks", "conditional-blocks", "waits-for", " BLOCKS "} {
		if !IsBlockingDependencyType(dependencyType) {
			t.Errorf("IsBlockingDependencyType(%q)=false, want true", dependencyType)
		}
	}
	for _, dependencyType := range []string{"", "parent-child", "relates-to", "external"} {
		if IsBlockingDependencyType(dependencyType) {
			t.Errorf("IsBlockingDependencyType(%q)=true, want false", dependencyType)
		}
	}
}

func TestBlockingDependentStatesIsUncappedAndSkipsExternalEndpoints(t *testing.T) {
	t.Parallel()
	rows := make([]beadShowDependency, 0, 106)
	for i := 0; i < 105; i++ {
		dependencyType := []string{"blocks", "conditional-blocks", "waits-for"}[i%3]
		rows = append(rows, beadShowDependency{
			ID: fmt.Sprintf("ntm-dependent-%03d", i), Title: fmt.Sprintf("Dependent %d", i),
			Status: "open", Priority: i % 5, DependencyType: dependencyType,
		})
	}
	rows = append(rows,
		beadShowDependency{ID: "external:other/repo:task", Status: "open", DependencyType: "blocks"},
		beadShowDependency{ID: "ntm-parent", Status: "open", DependencyType: "parent-child"},
	)
	states, err := blockingDependentStates(rows)
	if err != nil {
		t.Fatalf("blockingDependentStates: %v", err)
	}
	if len(states) != 105 {
		t.Fatalf("uncapped dependent count=%d, want 105", len(states))
	}
	if states[0].ID != "ntm-dependent-000" || states[104].ID != "ntm-dependent-104" || states[104].Priority != 4 {
		t.Fatalf("dependent boundary rows=%+v ... %+v", states[0], states[104])
	}
}

func TestBlockingDependencyStatesRejectsAmbiguousDependencies(t *testing.T) {
	t.Parallel()

	for name, dependencies := range map[string][]beadShowDependency{
		"missing id":     {{Status: "open", DependencyType: "blocks"}},
		"missing status": {{ID: "ntm-missing", DependencyType: "blocks"}},
		"conflicting duplicate": {
			{ID: "ntm-race", Status: "open", DependencyType: "blocks"},
			{ID: "ntm-race", Status: "closed", DependencyType: "blocks"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if states, err := blockingDependencyStates(dependencies); err == nil {
				t.Fatalf("blockingDependencyStates=%+v, want error", states)
			}
		})
	}
}

func TestParseBeadClaimOutput(t *testing.T) {
	t.Parallel()

	claim, err := parseBeadClaimOutput(`[{
		"id":"ntm-123",
		"title":"Atomic assignment",
		"status":"in_progress",
		"priority":1,
		"updated_at":"2026-07-11T12:00:00Z"
	}]`)
	if err != nil {
		t.Fatalf("parseBeadClaimOutput: %v", err)
	}
	if claim.ID != "ntm-123" || claim.Status != "in_progress" {
		t.Fatalf("claim=%+v", claim)
	}
	if claim.Title != "Atomic assignment" {
		t.Fatalf("claim title=%q", claim.Title)
	}
}

// Under a workflow capacity soft limit br wraps update --json rows as
// {"updated":[...],"warnings":[...]} (UpdateWithCapacityWarnings in br's
// update.rs); the claim succeeded and must not be reported as failed.
func TestParseBeadClaimOutputAcceptsCapacityWarningEnvelope(t *testing.T) {
	t.Parallel()

	claim, err := parseBeadClaimOutput(`{"updated":[{"id":"ntm-123","title":"Atomic assignment","status":"in_progress"}],"warnings":[{"kind":"in_progress_soft_limit","limit":3,"count":3}]}`)
	if err != nil || claim.ID != "ntm-123" || claim.Status != "in_progress" {
		t.Fatalf("parseBeadClaimOutput(envelope) = %+v, %v", claim, err)
	}
	if _, err := parseBeadClaimOutput(`{"updated":[{"id":"ntm-123","status":"open"}],"warnings":[]}`); err == nil {
		t.Fatal("an enveloped row that is not in_progress must still fail")
	}
	if _, err := parseBeadClaimOutput(`{"warnings":[]}`); err == nil {
		t.Fatal("an object without updated rows must fail")
	}
}

func TestParseBeadClaimOutputRejectsNonClaimedState(t *testing.T) {
	t.Parallel()

	if _, err := parseBeadClaimOutput(`[{"id":"ntm-123","status":"open"}]`); err == nil {
		t.Fatal("expected non-in_progress claim output to fail")
	}
	if _, err := parseBeadClaimOutput(`[]`); err == nil {
		t.Fatal("expected empty claim output to fail")
	}
}

func TestBeadClaimArgsUseAtomicPrimitive(t *testing.T) {
	t.Parallel()

	want := []string{"update", "ntm-123", "--claim", "--actor", "BlueLake/ntm-key", "--json"}
	if got := beadClaimArgs("ntm-123", "BlueLake/ntm-key"); !reflect.DeepEqual(got, want) {
		t.Fatalf("beadClaimArgs=%q, want %q", got, want)
	}
}

func TestOperatorGatedLabelsCanonicalVocabulary(t *testing.T) {
	t.Parallel()

	want := []string{
		"blocked-on-ivan",
		"blocked-on-operator",
		"business-input",
		"human-gated",
		"human-input",
		"needs-operator",
		"operator-action",
		"operator-gated",
	}
	got := OperatorGatedLabels()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OperatorGatedLabels()=%v, want %v", got, want)
	}
	for _, label := range want {
		if !IsOperatorGatedLabelInPolicy(label, nil) || !IsOperatorGatedLabelInPolicy("  "+strings.ToUpper(label)+"  ", nil) {
			t.Fatalf("canonical operator label %q was not recognized after normalization", label)
		}
	}
	for _, label := range []string{"", "backend", "operator", "blocked"} {
		if IsOperatorGatedLabelInPolicy(label, nil) {
			t.Fatalf("non-gated label %q was classified as operator gated", label)
		}
	}
	got[0] = "mutated"
	if fresh := OperatorGatedLabels(); !reflect.DeepEqual(fresh, want) {
		t.Fatalf("caller mutated canonical operator labels: %v", fresh)
	}
}

func TestProjectOperatorGatedLabelsRemainIsolatedUnderConcurrentAccess(t *testing.T) {
	projects := []struct {
		dir   string
		gate  string
		other string
	}{
		{dir: t.TempDir(), gate: "project-alpha-approval", other: "project-beta-approval"},
		{dir: t.TempDir(), gate: "project-beta-approval", other: "project-alpha-approval"},
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(projects))
	for _, project := range projects {
		project := project
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ConfigureProjectOperatorGatedLabels(project.dir, []string{project.gate}); err != nil {
				errs <- err
				return
			}
			for i := 0; i < 1000; i++ {
				if !IsOperatorGatedLabelForProject(project.dir, project.gate) {
					errs <- fmt.Errorf("project %s lost its configured gate %s", project.dir, project.gate)
					return
				}
				if IsOperatorGatedLabelForProject(project.dir, project.other) {
					errs <- fmt.Errorf("project %s inherited unrelated gate %s", project.dir, project.other)
					return
				}
				if !IsOperatorGatedLabelForProject(project.dir, "operator-gated") {
					errs <- fmt.Errorf("project %s lost the built-in gate vocabulary", project.dir)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectOperatorGatedLabelsUseAuthoritativeRootAliases(t *testing.T) {
	originalGlobal := OperatorGatedLabels()
	ConfigureOperatorGatedLabels([]string{"global-only-gate"})
	t.Cleanup(func() { ConfigureOperatorGatedLabels(originalGlobal) })

	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	nestedDir := filepath.Join(projectDir, "internal", "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested project directory: %v", err)
	}
	if err := ConfigureProjectOperatorGatedLabels(projectDir, []string{"project-only-gate"}); err != nil {
		t.Fatalf("configure project gate: %v", err)
	}

	for _, alias := range []string{projectDir, nestedDir} {
		if !IsOperatorGatedLabelForProject(alias, "project-only-gate") {
			t.Fatalf("project gate missing through alias %q", alias)
		}
		if IsOperatorGatedLabelForProject(alias, "global-only-gate") {
			t.Fatalf("alias %q fell back to conflicting process-global policy", alias)
		}
	}

	t.Chdir(nestedDir)
	if !IsOperatorGatedLabelForProject("", "project-only-gate") {
		t.Fatal("project gate missing through empty cwd alias")
	}
	if IsOperatorGatedLabelForProject("", "global-only-gate") {
		t.Fatal("empty cwd alias fell back to conflicting process-global policy")
	}
}

func TestValidateBeadAssignmentAuthorizationAllowsExplicitStaleOwnershipState(t *testing.T) {
	details := &BeadAssignmentDetails{ID: "ntm-stale", Status: "in_progress"}
	authorization := BeadAssignmentAuthorization{
		BeadID: "ntm-stale", AllowUnassignedInProgress: true,
	}
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, authorization, []string{"project-approval"}); err != nil {
		t.Fatalf("authorize unassigned in_progress stale work: %v", err)
	}

	withoutAllowance := authorization
	withoutAllowance.AllowUnassignedInProgress = false
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(details, withoutAllowance, nil); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("unapproved unassigned in_progress error=%v, want ineligible", err)
	}

	owned := *details
	owned.Assignee = "OtherActor"
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(&owned, authorization, nil); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("owned stale work error=%v, want ineligible", err)
	}

	gated := *details
	gated.Labels = []string{"project-approval"}
	if err := ValidateBeadAssignmentAuthorizationWithOperatorGatedLabels(&gated, authorization, []string{"project-approval"}); !errors.Is(err, ErrBeadAssignmentIneligible) {
		t.Fatalf("gated stale work error=%v, want ineligible", err)
	}
}

func TestClaimBeadForAssignmentTransactionRejectsDirectStartBlockers(t *testing.T) {
	requireRealBR(t)

	for _, dependencyType := range []string{"blocks", "conditional-blocks", "waits-for"} {
		t.Run(dependencyType, func(t *testing.T) {
			dir := t.TempDir()
			runRealBR(t, dir, "init", "--quiet")
			targetID := createRealBRBead(t, dir, "assignment claim target "+dependencyType)
			blockerID := createRealBRBead(t, dir, "assignment claim blocker "+dependencyType)
			runRealBR(t, dir, "dep", "add", targetID, blockerID, "--type", dependencyType, "--json")

			result, err := ClaimBeadForAssignmentWithOperatorGatedLabels(
				t.Context(), dir, targetID, "AtomicActor", OperatorGatedLabels(),
			)
			var eligibilityErr *AssignmentEligibilityError
			if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) {
				t.Fatalf("claim result=%+v error=%v, want typed ineligible error", result, err)
			}
			if eligibilityErr.BeadID != targetID || eligibilityErr.Status != "open" ||
				!reflect.DeepEqual(eligibilityErr.UnresolvedBlockers, []string{blockerID}) || len(eligibilityErr.OperatorLabels) != 0 {
				t.Fatalf("eligibility error=%+v", eligibilityErr)
			}
			assertRealBRStatusAndAssignee(t, dir, targetID, "open", "")
		})
	}
}

func TestClaimBeadForAssignmentTransactionRejectsEveryOperatorGate(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	targetID := createRealBRBead(t, dir, "assignment operator gate target")
	for _, label := range OperatorGatedLabels() {
		runRealBR(t, dir, "update", targetID, "--add-label", label, "--json")
	}

	result, err := ClaimBeadForAssignmentWithOperatorGatedLabels(
		t.Context(), dir, targetID, "AtomicActor", OperatorGatedLabels(),
	)
	var eligibilityErr *AssignmentEligibilityError
	if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) {
		t.Fatalf("claim result=%+v error=%v, want typed ineligible error", result, err)
	}
	if !reflect.DeepEqual(eligibilityErr.OperatorLabels, OperatorGatedLabels()) || len(eligibilityErr.UnresolvedBlockers) != 0 {
		t.Fatalf("eligibility error=%+v", eligibilityErr)
	}
	assertRealBRStatusAndAssignee(t, dir, targetID, "open", "")
}

func TestClaimBeadForAssignmentMirrorsStartBlockerSemantics(t *testing.T) {
	requireRealBR(t)

	t.Run("parent-child rollup does not block start", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		parentID := createRealBRBead(t, dir, "claimable parent")
		childID := createRealBRBead(t, dir, "open child")
		runRealBR(t, dir, "dep", "add", childID, parentID, "--type", "parent-child", "--json")
		claim, err := ClaimBeadForAssignment(t.Context(), dir, parentID, "AtomicActor")
		if err != nil || claim.ID != parentID || claim.Status != "in_progress" {
			t.Fatalf("parent claim=%+v error=%v", claim, err)
		}
		assertRealBRStatusAndAssignee(t, dir, parentID, "in_progress", "AtomicActor")
	})

	t.Run("closed direct blocker does not block start", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		targetID := createRealBRBead(t, dir, "claimable after blocker closes")
		blockerID := createRealBRBead(t, dir, "closed blocker")
		runRealBR(t, dir, "dep", "add", targetID, blockerID, "--type", "blocks", "--json")
		runRealBR(t, dir, "close", blockerID, "--reason", "test prerequisite complete", "--json")
		claim, err := ClaimBeadForAssignment(t.Context(), dir, targetID, "AtomicActor")
		if err != nil || claim.ID != targetID || claim.Status != "in_progress" {
			t.Fatalf("unblocked claim=%+v error=%v", claim, err)
		}
	})
}

func TestClaimBeadForAssignmentStatusConflictAndIdempotency(t *testing.T) {
	requireRealBR(t)

	t.Run("terminal status is ineligible", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "terminal assignment claim")
		runRealBR(t, dir, "close", beadID, "--reason", "terminal test", "--json")
		_, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "AtomicActor")
		var eligibilityErr *AssignmentEligibilityError
		if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) || eligibilityErr.Status != "closed" {
			t.Fatalf("terminal claim error=%v eligibility=%+v", err, eligibilityErr)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "closed", "")
	})

	t.Run("other owner is conflict", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "assignment owner conflict")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "FirstActor"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "OtherActor"); !errors.Is(err, ErrBeadAlreadyClaimed) || errors.Is(err, ErrBeadAssignmentIneligible) {
			t.Fatalf("other-owner claim error=%v, want conflict only", err)
		}
	})

	t.Run("same owner retry is idempotent while gates remain clear", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "idempotent assignment claim")
		first, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		if err != nil {
			t.Fatalf("first claim: %v", err)
		}
		second, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		if err != nil || second.ID != first.ID || second.Actor != first.Actor || second.Status != "in_progress" {
			t.Fatalf("idempotent claim=%+v first=%+v error=%v", second, first, err)
		}
	})

	t.Run("same owner retry rechecks operator gates", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "gated assignment recovery")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		runRealBR(t, dir, "update", beadID, "--add-label", "operator-gated", "--json")
		_, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		var eligibilityErr *AssignmentEligibilityError
		if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) ||
			!reflect.DeepEqual(eligibilityErr.OperatorLabels, []string{"operator-gated"}) {
			t.Fatalf("same-owner gated claim error=%v eligibility=%+v", err, eligibilityErr)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "in_progress", "StableActor")
	})

	t.Run("same owner retry rechecks blockers", func(t *testing.T) {
		dir := t.TempDir()
		runRealBR(t, dir, "init", "--quiet")
		beadID := createRealBRBead(t, dir, "blocked assignment recovery")
		blockerID := createRealBRBead(t, dir, "new blocker")
		if _, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		runRealBR(t, dir, "dep", "add", beadID, blockerID, "--type", "blocks", "--json")
		_, err := ClaimBeadForAssignment(t.Context(), dir, beadID, "StableActor")
		var eligibilityErr *AssignmentEligibilityError
		if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibilityErr) ||
			!reflect.DeepEqual(eligibilityErr.UnresolvedBlockers, []string{blockerID}) {
			t.Fatalf("same-owner blocked claim error=%v eligibility=%+v", err, eligibilityErr)
		}
		assertRealBRStatusAndAssignee(t, dir, beadID, "in_progress", "StableActor")
	})
}

func TestIsBeadsDBCorruptionError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"malformed image", fmt.Errorf("compare-and-set guarded Beads claim: database disk image is malformed (11)"), true},
		{"wrapped malformed schema", fmt.Errorf("outer: %w", errors.New("malformed database schema (users)")), true},
		{"not a database", errors.New("file is not a database"), true},
		{"free-page corruption class", errors.New("database corruption detected by pager"), true},
		{"already claimed", ErrBeadAlreadyClaimed, false},
		{"generic busy", errors.New("database is locked"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isBeadsDBCorruptionError(tc.err); got != tc.want {
				t.Fatalf("isBeadsDBCorruptionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func assertRealBRStatusAndAssignee(t *testing.T, dir, beadID, wantStatus, wantAssignee string) {
	t.Helper()
	output := runRealBR(t, dir, "show", beadID, "--json")
	var rows []struct {
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal(output, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("parse br show for %s: rows=%v error=%v output=%s", beadID, rows, err, output)
	}
	if rows[0].Status != wantStatus || rows[0].Assignee != wantAssignee {
		t.Fatalf("bead %s status=%q assignee=%q, want %q/%q", beadID, rows[0].Status, rows[0].Assignee, wantStatus, wantAssignee)
	}
}

func requireRealBR(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br is not installed")
	}
}

func runRealBR(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("br", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("br %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, output, stderr.Bytes())
	}
	return output
}

func createRealBRBead(t *testing.T, dir, title string) string {
	t.Helper()
	output := runRealBR(t, dir, "create", "--title", title, "--type", "task", "--priority", "2", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &created); err != nil {
		t.Fatalf("parse br create output: %v\n%s", err, output)
	}
	if created.ID == "" {
		t.Fatalf("br create returned no ID: %s", output)
	}
	return created.ID
}
