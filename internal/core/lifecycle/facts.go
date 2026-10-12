// Package lifecycle is nhm's retire, recycle, reap, kill, restore,
// recovery and export (plan-v8 2.10). Retire and reap keep every refusal
// ACFS's Bash has, and fail closed when Agent Mail, br or git cannot be
// read. Recycle keeps one owner of the identity at every moment.
package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"
)

// factTimeout bounds one external read.
const factTimeout = 15 * time.Second

// Reservation is one active Agent Mail file reservation.
type Reservation struct {
	Agent string `json:"agent"`
	Path  string `json:"path"`
}

// Bead is an assigned bead and whether it carries a handoff comment.
type Bead struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	HasHandoff bool   `json:"has_handoff"`
	// Handoff is the latest handoff comment.
	Handoff string `json:"-"`
}

// Facts are the external reads of retire and reap. Every method fails
// rather than guess.
type Facts interface {
	// Reservations are the active reservations of every agent.
	Reservations(ctx context.Context, project string) ([]Reservation, error)
	// ReservedEver are the patterns an agent ever reserved.
	ReservedEver(ctx context.Context, project, agent string) ([]string, error)
	// DirtyFiles are the changed and untracked files of a checkout.
	DirtyFiles(ctx context.Context, root string) ([]string, error)
	// Worktrees are the checkout's worktree paths.
	Worktrees(ctx context.Context, root string) ([]string, error)
	// WorktreeState says whether a worktree is dirty and whether its HEAD
	// is merged to origin/main.
	WorktreeState(ctx context.Context, wt string) (dirty, merged bool, err error)
	RemoveWorktree(ctx context.Context, root, wt string) error
	// Assigned are the agent's open beads (in progress), with their
	// latest handoff comment.
	Assigned(ctx context.Context, root, agent string) ([]Bead, error)
	// Closed are the beads assigned to the agent that are closed.
	Closed(ctx context.Context, root, agent string) ([]string, error)
}

// CLI reads the facts with the am, git and br commands, argv only.
type CLI struct{}

func run(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, factTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Reservations reads `am robot reservations --all --json`.
func (CLI) Reservations(ctx context.Context, project string) ([]Reservation, error) {
	out, err := run(ctx, project, "am", "robot", "reservations", "--project", project, "--all", "--json")
	if err != nil {
		return nil, err
	}
	var v struct {
		All []Reservation `json:"all_active"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("am robot reservations: %w", err)
	}
	return v.All, nil
}

var columns = regexp.MustCompile(`\s{2,}`)

// ReservedEver reads `am file_reservations list <project> --all`, as the
// Bash does: the pattern and agent columns of every row.
func (CLI) ReservedEver(ctx context.Context, project, agent string) ([]string, error) {
	out, err := run(ctx, project, "am", "file_reservations", "list", project, "--all")
	if err != nil {
		return nil, err
	}
	var patterns []string
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := columns.Split(strings.TrimSpace(line), -1)
		if len(f) >= 3 && f[2] == agent {
			patterns = append(patterns, f[1])
		}
	}
	return patterns, nil
}

// DirtyFiles reads `git status --porcelain=v1 -z --untracked-files=all`.
func (CLI) DirtyFiles(ctx context.Context, root string) ([]string, error) {
	out, err := run(ctx, root, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return parsePorcelainZ(out), nil
}

// parsePorcelainZ reads `git status --porcelain=v1 -z`: "XY path" records;
// a rename or copy is followed by one more record, the old path, with no
// status. Both paths count.
func parsePorcelainZ(out []byte) []string {
	var files []string
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if len(rec) < 4 {
			continue
		}
		files = append(files, rec[3:])
		if (rec[0] == 'R' || rec[0] == 'C') && i+1 < len(recs) {
			i++
			if recs[i] != "" {
				files = append(files, recs[i])
			}
		}
	}
	return files
}

// Worktrees reads `git worktree list --porcelain`.
func (CLI) Worktrees(ctx context.Context, root string) ([]string, error) {
	out, err := run(ctx, root, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []string
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			list = append(list, p)
		}
	}
	return list, nil
}

// WorktreeState reads the worktree's status and whether its HEAD is an
// ancestor of origin/main.
func (CLI) WorktreeState(ctx context.Context, wt string) (bool, bool, error) {
	out, err := run(ctx, wt, "git", "status", "--porcelain")
	if err != nil {
		return false, false, err
	}
	dirty := len(bytes.TrimSpace(out)) > 0
	_, err = run(ctx, wt, "git", "merge-base", "--is-ancestor", "HEAD", "origin/main")
	var exit *exec.ExitError
	switch {
	case err == nil:
		return dirty, true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return dirty, false, nil
	}
	return dirty, false, err
}

// RemoveWorktree runs `git worktree remove` without --force.
func (CLI) RemoveWorktree(ctx context.Context, root, wt string) error {
	_, err := run(ctx, root, "git", "worktree", "remove", wt)
	return err
}

type brIssue struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
}

// brList runs `br list --status S --assignee A --limit 0 --json`, whose
// output is {"issues": [...], "has_more": ...}. A page that is not the
// whole list is an error: a bead left out could be work in progress.
func brList(ctx context.Context, root, status, agent string) ([]brIssue, error) {
	out, err := run(ctx, root, "br", "list", "--status", status, "--assignee", agent, "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	return parseBrList(out)
}

func parseBrList(out []byte) ([]brIssue, error) {
	var page struct {
		Issues  *[]brIssue `json:"issues"`
		HasMore bool       `json:"has_more"`
	}
	if err := json.Unmarshal(out, &page); err != nil {
		return nil, fmt.Errorf("br list: %w", err)
	}
	if page.Issues == nil {
		return nil, errors.New("br list: the output has no issues list")
	}
	if page.HasMore {
		return nil, errors.New("br list: the output is not the whole list (has_more)")
	}
	return *page.Issues, nil
}

// Assigned reads `br list --status in_progress --assignee A` and each
// bead's comments.
func (CLI) Assigned(ctx context.Context, root, agent string) ([]Bead, error) {
	issues, err := brList(ctx, root, "in_progress", agent)
	if err != nil {
		return nil, err
	}
	var beads []Bead
	for _, is := range issues {
		if is.Assignee != "" && is.Assignee != agent {
			continue
		}
		b := Bead{ID: is.ID, Title: is.Title, Status: is.Status}
		out, err := run(ctx, root, "br", "comments", "list", is.ID, "--json")
		if err != nil {
			return nil, err
		}
		var comments []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(out, &comments); err != nil {
			return nil, fmt.Errorf("br comments %s: %w", is.ID, err)
		}
		for _, c := range comments {
			if IsHandoff(c.Text) {
				b.HasHandoff, b.Handoff = true, c.Text
			}
		}
		beads = append(beads, b)
	}
	return beads, nil
}

// IsHandoff reports a handoff comment: its first line names a handoff.
func IsHandoff(text string) bool {
	first, _, _ := strings.Cut(text, "\n")
	return strings.Contains(strings.ToLower(first), "handoff")
}

// Closed reads `br list --status closed --assignee A`.
func (CLI) Closed(ctx context.Context, root, agent string) ([]string, error) {
	issues, err := brList(ctx, root, "closed", agent)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, is := range issues {
		if is.Assignee == "" || is.Assignee == agent {
			ids = append(ids, is.ID)
		}
	}
	return ids, nil
}

// globMatch is a reservation glob match, as the Bash's [[ == ]]: "*"
// matches across "/" and "**" is the same.
func globMatch(pattern, file string) bool {
	if pattern == file {
		return true
	}
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	re = strings.ReplaceAll(re, `\?`, ".")
	ok, err := regexp.MatchString(re, file)
	if err != nil {
		ok, _ = path.Match(pattern, file)
	}
	return ok
}
