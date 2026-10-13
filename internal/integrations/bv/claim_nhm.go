package bv

// nhm's side of the guarded assignment claim (claimBeadForAssignmentAttempt
// in bv.go): the per-project claim lock, the updated_at token of the read,
// and how br's answers map onto ntm's claim errors (nhm-i85, ACFS
// acfs-f2mc.2).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	nhmstore "github.com/arosl/nhm/internal/store"
)

// claimLockPoll is how often a waiting claimer tries the lock again.
const claimLockPoll = 20 * time.Millisecond

// claimLockPath is the claim lock of one resolved project directory, under
// nhm's state root: never in the project, whose .beads may be tracked.
func claimLockPath(normalizedDir string) (string, error) {
	root, err := nhmstore.StateRoot()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(normalizedDir))
	return filepath.Join(root, "locks", "claims", hex.EncodeToString(sum[:16])+".lock"), nil
}

// lockProjectClaims takes the project's claim lock: the in-process gate of
// br calls for the directory is not enough, since several nhm processes can
// claim in one project. It serializes nhm claimers of one user; ctx bounds
// the wait.
func lockProjectClaims(ctx context.Context, normalizedDir string) (func(), error) {
	if ctx == nil {
		return nil, errors.New("claim lock context is required")
	}
	path, err := claimLockPath(normalizedDir)
	if err != nil {
		return nil, fmt.Errorf("claim lock for %s: %w", normalizedDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("claim lock for %s: %w", normalizedDir, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("claim lock for %s: %w", normalizedDir, err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("claim lock %s: %w", path, err)
		}
		timer := time.NewTimer(claimLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// parseBeadShowUpdatedAt returns the updated_at of the one bead in a br show
// --json answer, exactly as br wrote it: the --if-unchanged token.
func parseBeadShowUpdatedAt(output string) (string, error) {
	type row struct {
		ID        string `json:"id"`
		UpdatedAt string `json:"updated_at"`
	}
	trimmed := strings.TrimSpace(output)
	var picked row
	var rows []row
	if err := json.Unmarshal([]byte(trimmed), &rows); err == nil {
		if len(rows) != 1 {
			return "", fmt.Errorf("br show returned %d beads, want exactly 1", len(rows))
		}
		picked = rows[0]
	} else if err := json.Unmarshal([]byte(trimmed), &picked); err != nil {
		return "", fmt.Errorf("parse bead updated_at: %w", err)
	}
	updatedAt := strings.TrimSpace(picked.UpdatedAt)
	if updatedAt == "" {
		return "", fmt.Errorf("bead %s has no updated_at in br show; the claim needs it for --if-unchanged", strings.TrimSpace(picked.ID))
	}
	return updatedAt, nil
}

// brDatabaseRefusal reports a br answer that it cannot use its database:
// corruption, or a WAL index it refuses until recovered.
func brDatabaseRefusal(err error) bool {
	if isBeadsDBCorruptionError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "wal index") || strings.Contains(message, "migrate-schema recover")
}

// errBrDatabase names br's own recovery; nhm never rebuilds a tracker.
func errBrDatabase(beadID string, err error) error {
	return fmt.Errorf("claim %s: br cannot use its database; recover it with br's own tools (br doctor, br doctor migrate-schema recover), nhm does not rebuild it: %w", beadID, err)
}

// claimReadError maps a failed br show before the claim. Nothing was written.
func claimReadError(beadID string, err error) error {
	if brDatabaseRefusal(err) {
		return errBrDatabase(beadID, err)
	}
	return fmt.Errorf("read bead %s for the assignment claim: %w", beadID, err)
}

// claimWriteError maps br update --claim --if-unchanged's refusals onto
// ntm's claim errors. br writes nothing on exit 6 (the record moved since the
// read) and on a validation refusal (blocked, already assigned). Any other
// failure leaves the outcome unknown and is returned as it is, so the
// assignment ledger records the claim as uncertain.
func claimWriteError(beadID string, err error) error {
	message := err.Error()
	var exitErr *exec.ExitError
	switch {
	case (errors.As(err, &exitErr) && exitErr.ExitCode() == 6) || strings.Contains(message, "UPDATE_PRECONDITION_FAILED"):
		return fmt.Errorf("%w: assignment claim precondition changed for %s: %v", ErrBeadAlreadyClaimed, beadID, err)
	case strings.Contains(message, "already assigned to"):
		return fmt.Errorf("%w: %v", ErrBeadAlreadyClaimed, err)
	case strings.Contains(message, "cannot claim blocked issue"):
		return fmt.Errorf("%w: %s: br refused the claim of a blocked issue: %v", ErrBeadAssignmentIneligible, beadID, err)
	case brDatabaseRefusal(err):
		return errBrDatabase(beadID, err)
	}
	return err
}
