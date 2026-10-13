package bv

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// installClaimFakeBR installs a fake br that answers `show` with showJSON,
// `list` with listJSON and `update` with updateScript (sh), and logs every
// call's arguments, one call per line.
func installClaimFakeBR(t *testing.T, showJSON, listJSON, updateScript string) (dir, calls string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	fixtures := t.TempDir()
	for name, body := range map[string]string{"show.json": showJSON, "list.json": listJSON} {
		if err := os.WriteFile(filepath.Join(fixtures, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	calls = filepath.Join(t.TempDir(), "calls")
	// An update script can change what the next show answers ($SHOW) and
	// count its own calls ($STATE).
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\n" +
		"SHOW=" + filepath.Join(fixtures, "show.json") + "; STATE=" + filepath.Join(fixtures, "state") + "\n" +
		"case \" $* \" in\n" +
		"*' show '*) cat " + filepath.Join(fixtures, "show.json") + " ;;\n" +
		"*' list '*) cat " + filepath.Join(fixtures, "list.json") + " ;;\n" +
		"*' update '*)\n" + updateScript + "\n;;\n" +
		"*) echo \"unexpected br call: $*\" >&2; exit 2 ;;\n" +
		"esac\n"
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "br"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, calls
}

func fakeBRCalls(t *testing.T, calls string) []string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read fake br calls: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func countUpdateCalls(lines []string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(" "+line+" ", " update ") {
			n++
		}
	}
	return n
}

const claimedRow = `[{"id":"nhm-t1","title":"Task","status":"in_progress","priority":2,"assignee":"Actor/ntm-0123456789ab"}]`

// The readiness-gate cases of ntm's
// TestClaimBeadForAssignmentTransactionRejectsNonReadyOpenWork: a deferred,
// pinned, ephemeral, template or wisp bead is refused from the read, and br
// is never asked to write.
func TestClaimRefusesNonReadyOpenWorkBeforeWriting(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		fields     string
		wantReason func(*AssignmentEligibilityError) bool
	}{
		{name: "future deferred", id: "nhm-t1", fields: `"defer_until":"2099-01-01T00:00:00Z"`, wantReason: func(e *AssignmentEligibilityError) bool { return e.Deferred }},
		{name: "pinned", id: "nhm-t1", fields: `"pinned":true`, wantReason: func(e *AssignmentEligibilityError) bool { return e.Pinned }},
		{name: "ephemeral", id: "nhm-t1", fields: `"ephemeral":true`, wantReason: func(e *AssignmentEligibilityError) bool { return e.Ephemeral }},
		{name: "template", id: "nhm-t1", fields: `"is_template":true`, wantReason: func(e *AssignmentEligibilityError) bool { return e.Template }},
		{name: "wisp", id: "unit-wisp-claim", fields: `"priority":2`, wantReason: func(e *AssignmentEligibilityError) bool { return e.Wisp }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			show := `[{"id":"` + test.id + `","title":"Task","issue_type":"task","status":"open","priority":2,"updated_at":"2026-10-13T00:00:00Z",` + test.fields + `}]`
			dir, calls := installClaimFakeBR(t, show, `[]`, "echo "+claimedRow)
			_, err := ClaimBeadForAssignment(t.Context(), dir, test.id, "Actor/ntm-0123456789ab")
			var eligibility *AssignmentEligibilityError
			if !errors.Is(err, ErrBeadAssignmentIneligible) || !errors.As(err, &eligibility) || !test.wantReason(eligibility) {
				t.Fatalf("claim error=%v eligibility=%+v", err, eligibility)
			}
			if eligibility.Status != "open" || len(eligibility.UnresolvedBlockers) != 0 || len(eligibility.OperatorLabels) != 0 {
				t.Fatalf("eligibility=%+v", eligibility)
			}
			if n := countUpdateCalls(fakeBRCalls(t, calls)); n != 0 {
				t.Fatalf("br update calls=%d, want 0", n)
			}
		})
	}
}

// The write is br's compare-and-set on the updated_at of the read.
func TestClaimWritesWithIfUnchangedFromTheRead(t *testing.T) {
	show := `[{"id":"nhm-t1","title":"Task","issue_type":"task","status":"open","priority":2,"updated_at":"2026-10-13T12:34:56.789012345Z"}]`
	dir, calls := installClaimFakeBR(t, show, `[]`, "echo '"+claimedRow+"'")
	result, err := ClaimBeadForAssignment(t.Context(), dir, "nhm-t1", "Actor/ntm-0123456789ab")
	if err != nil || result.ID != "nhm-t1" || result.Actor != "Actor/ntm-0123456789ab" || result.Status != "in_progress" {
		t.Fatalf("claim=%+v error=%v", result, err)
	}
	var update string
	for _, line := range fakeBRCalls(t, calls) {
		if strings.Contains(" "+line+" ", " update ") {
			update = line
		}
	}
	for _, want := range []string{"update nhm-t1 --claim", "--actor Actor/ntm-0123456789ab", "--if-unchanged 2026-10-13T12:34:56.789012345Z"} {
		if !strings.Contains(update, want) {
			t.Fatalf("update call %q lacks %q", update, want)
		}
	}
}

// br's refusals map onto ntm's claim errors after a new read; an unknown
// failure stays unknown unless the read shows the claim; a database
// refusal names br's recovery and nhm never rebuilds. The write never
// retries itself: every update call follows its own read.
func TestClaimMapsBrRefusals(t *testing.T) {
	show := `[{"id":"nhm-t1","title":"Task","issue_type":"task","status":"open","priority":2,"updated_at":"2026-10-13T00:00:00Z"}]`
	const actor = "Actor/ntm-0123456789ab"
	ours := `[{"id":"nhm-t1","title":"Task","issue_type":"task","status":"in_progress","priority":2,"assignee":"` + actor + `","updated_at":"2026-10-13T00:00:01Z"}]`
	others := `[{"id":"nhm-t1","title":"Task","issue_type":"task","status":"in_progress","priority":2,"assignee":"Other","updated_at":"2026-10-13T00:00:01Z"}]`
	moved := `[{"id":"nhm-t1","title":"Task","issue_type":"task","status":"open","priority":2,"updated_at":"2026-10-13T00:00:02Z"}]`
	const exit6 = `echo '{"error":{"code":"UPDATE_PRECONDITION_FAILED","message":"nhm-t1 changed since you read it","context":{"written":false}}}'; exit 6`
	tests := []struct {
		name    string
		update  string
		check   func(error) bool
		updates int
	}{
		{
			// A comment moved updated_at; the bead is still free: the
			// claim reads again and writes with the new token.
			name:   "record moved, then free",
			update: `if [ ! -e "$STATE" ]; then : > "$STATE"; echo '` + moved + `' > "$SHOW"; ` + exit6 + `; fi; echo '` + claimedRow + `'`,
			check:  func(err error) bool { return err == nil }, updates: 2,
		},
		{
			name:   "record keeps moving",
			update: exit6,
			check: func(err error) bool {
				return err != nil && !errors.Is(err, ErrBeadAlreadyClaimed) && !errors.Is(err, ErrBeadAssignmentIneligible)
			},
			updates: claimTries,
		},
		{
			name:   "already assigned",
			update: `echo '` + others + `' > "$SHOW"; echo '{"error":{"code":"VALIDATION_FAILED","message":"Validation failed: claim: issue nhm-t1 already assigned to Other"}}'; exit 4`,
			check:  func(err error) bool { return errors.Is(err, ErrBeadAlreadyClaimed) }, updates: 1,
		},
		{
			// br committed, then reported a busy database: the claim is ours.
			name:   "busy after the commit",
			update: `echo '` + ours + `' > "$SHOW"; echo 'database is locked' >&2; exit 1`,
			check:  func(err error) bool { return err == nil }, updates: 1,
		},
		{
			name:   "blocked",
			update: `echo '{"error":{"code":"VALIDATION_FAILED","message":"Validation failed: claim: cannot claim blocked issue: nhm-t1"}}'; exit 4`,
			check: func(err error) bool {
				return errors.Is(err, ErrBeadAssignmentIneligible) && !errors.Is(err, ErrBeadAlreadyClaimed)
			},
			updates: 1,
		},
		{
			name:   "unknown failure",
			update: `echo 'database is locked' >&2; exit 1`,
			check: func(err error) bool {
				return err != nil && !errors.Is(err, ErrBeadAlreadyClaimed) && !errors.Is(err, ErrBeadAssignmentIneligible)
			},
			updates: 1,
		},
		{
			name:   "database refusal",
			update: `echo 'Hint: The WAL index (.beads/beads.db-shm) must be rebuilt before br can read the database. Run br doctor migrate-schema recover' >&2; exit 1`,
			check: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "nhm does not rebuild it") &&
					!errors.Is(err, ErrBeadAlreadyClaimed) && !errors.Is(err, ErrBeadAssignmentIneligible)
			},
			updates: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, calls := installClaimFakeBR(t, show, `[]`, test.update)
			_, err := ClaimBeadForAssignment(t.Context(), dir, "nhm-t1", actor)
			if !test.check(err) {
				t.Fatalf("claim error=%v", err)
			}
			if n := countUpdateCalls(fakeBRCalls(t, calls)); n != test.updates {
				t.Fatalf("br update calls=%d, want %d", n, test.updates)
			}
			for _, line := range fakeBRCalls(t, calls) {
				if strings.Contains(line, "sync") || strings.Contains(line, "doctor") {
					t.Fatalf("claim ran a repair: %q", line)
				}
			}
		})
	}
}

// A project has one claim lock, whatever path names it.
func TestClaimLockFollowsSymlinks(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	direct, err := claimLockPath(project)
	if err != nil {
		t.Fatal(err)
	}
	viaLink, err := claimLockPath(link)
	if err != nil || viaLink != direct {
		t.Fatalf("lock via link=%q %v, direct=%q", viaLink, err, direct)
	}
}

// ntm's mutex rules on br's data: an in-progress or assigned holder of the
// same group refuses (naming only the group); a closed holder does not; a
// holder without a lifecycle fails closed; group spelling is normalized.
func TestClaimMutexHoldersFromBrList(t *testing.T) {
	show := `[{"id":"nhm-t1","title":"Task","issue_type":"task","status":"open","priority":2,"updated_at":"2026-10-13T00:00:00Z","labels":["mutex:DB"]}]`
	tests := []struct {
		name  string
		list  string
		check func(*testing.T, error)
	}{
		{
			name: "in-progress holder",
			list: `{"issues":[{"id":"nhm-h1","status":"in_progress","labels":[" Mutex:db "]},{"id":"nhm-t1","status":"open","labels":["mutex:DB"]}]}`,
			check: func(t *testing.T, err error) {
				var refusal *AssignmentMutexError
				if !errors.Is(err, ErrAssignmentMutexHeld) || !errors.Is(err, ErrBeadAssignmentIneligible) ||
					!errors.As(err, &refusal) || !reflect.DeepEqual(refusal.Groups, []string{"db"}) || strings.Contains(err.Error(), "nhm-h1") {
					t.Fatalf("error=%v", err)
				}
			},
		},
		{
			name: "assigned open holder",
			list: `[{"id":"nhm-h1","status":"open","assignee":"Other","labels":["mutex:db"]}]`,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrAssignmentMutexHeld) {
					t.Fatalf("error=%v", err)
				}
			},
		},
		{
			name: "closed holder",
			list: `[{"id":"nhm-h1","status":"closed","assignee":"Other","labels":["mutex:db"]}]`,
			check: func(t *testing.T, err error) {
				if err != nil {
					t.Fatalf("error=%v", err)
				}
			},
		},
		{
			name: "holders on more than one page",
			list: `{"issues":[{"id":"nhm-h1","status":"closed","labels":["mutex:db"]}],"has_more":true}`,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrBeadAssignmentIneligible) || errors.Is(err, ErrAssignmentMutexHeld) {
					t.Fatalf("error=%v", err)
				}
			},
		},
		{
			name: "holder without a status",
			list: `[{"id":"nhm-h1","status":"","labels":["mutex:db"]}]`,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrBeadAssignmentIneligible) || errors.Is(err, ErrAssignmentMutexHeld) {
					t.Fatalf("error=%v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := installClaimFakeBR(t, show, test.list, "echo '"+claimedRow+"'")
			_, err := ClaimBeadForAssignment(t.Context(), dir, "nhm-t1", "Actor/ntm-0123456789ab")
			test.check(t, err)
		})
	}
}

// Two claimers of different beads in one mutex group, racing: the claim
// lock serializes them, so the later one sees the earlier holder (ntm's
// TestAssignmentMutexClaimsSerializeAcrossConnections, on real br).
func TestClaimMutexClaimsSerializeOnRealBR(t *testing.T) {
	requireRealBR(t)
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	const count = 4
	ids := make([]string, count)
	for i := range ids {
		ids[i] = createRealBRBead(t, dir, "mutex task")
		runRealBR(t, dir, "update", ids[i], "--add-label", "mutex:db", "--json")
	}
	var wg sync.WaitGroup
	errs := make([]error, count)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ClaimBeadForAssignment(context.Background(), dir, ids[i], "Actor/ntm-00000000000"+string(rune('0'+i)))
		}(i)
	}
	wg.Wait()
	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrAssignmentMutexHeld):
		default:
			t.Fatalf("claim %s: unexpected error %v", ids[i], err)
		}
	}
	if won != 1 {
		t.Fatalf("claims won=%d, want exactly 1 (errors %v)", won, errs)
	}
}

// The reason for the adaptation: after nhm's claim, a bv read and then a br
// write still work (ntm's direct SQLite claim leaves br refusing the write;
// ACFS acfs-f2mc.2 closure record).
func TestClaimLeavesBrWritableAfterBvRead(t *testing.T) {
	requireRealBR(t)
	if _, err := exec.LookPath("bv"); err != nil {
		t.Skip("bv is not installed")
	}
	dir := t.TempDir()
	runRealBR(t, dir, "init", "--quiet")
	target := createRealBRBead(t, dir, "claim target")
	other := createRealBRBead(t, dir, "later write")
	if _, err := ClaimBeadForAssignment(t.Context(), dir, target, "Actor/ntm-0123456789ab"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	bv := exec.Command("bv", "--robot-triage")
	bv.Dir = dir
	if output, err := bv.CombinedOutput(); err != nil {
		t.Fatalf("bv --robot-triage: %v\n%s", err, output)
	}
	runRealBR(t, dir, "update", other, "--status", "in_progress", "--json")
	assertRealBRStatusAndAssignee(t, dir, target, "in_progress", "Actor/ntm-0123456789ab")
}
