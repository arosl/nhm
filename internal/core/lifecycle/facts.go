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
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	// Fetch updates origin/main, so that "merged" is about what origin
	// has now.
	Fetch(ctx context.Context, root string) error
	// WorktreeState says whether a worktree is dirty, whether its HEAD is
	// merged to origin/main, and which ignored files it holds.
	WorktreeState(ctx context.Context, wt string) (WorktreeInfo, error)
	RemoveWorktree(ctx context.Context, root, wt string) error
	// Assigned are the agent's open beads (in progress), with their
	// latest handoff comment.
	Assigned(ctx context.Context, root, agent string) ([]Bead, error)
	// Closed are the beads assigned to the agent that are closed.
	Closed(ctx context.Context, root, agent string) ([]string, error)
	// Known reports any bead, of any status, assigned to the agent in the
	// databases searched: without one, the search could not have found
	// the agent's work, and a handoff check would pass on nothing.
	Known(ctx context.Context, root, agent string) (bool, error)
}

// CLI reads the facts with the am, git and br commands, argv only.
type CLI struct {
	// BeadDBs are bead databases searched besides the project's own.
	BeadDBs []string
	// LoadBeadDBs, when set, gives more of them, read only when beads
	// are looked up (so a bad config fails only the verbs that read
	// beads).
	LoadBeadDBs func() ([]string, error)
}

func run(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	return runEnv(ctx, dir, nil, name, args...)
}

// runEnv runs a command with extra environment. It never waits for a
// person: git asks for no credentials (stdin is /dev/null too), and
// WaitDelay ends the wait for output when a child (an ssh that git
// started) outlives the command.
func runEnv(ctx context.Context, dir string, extra []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, factTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), extra...)
	cmd.WaitDelay = 5 * time.Second
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

// WorktreeInfo is a worktree's state as retire needs it.
type WorktreeInfo struct {
	Dirty  bool
	Merged bool
	// Ignored are the ignored files and dirs that `git worktree remove`
	// would delete, build caches left out.
	Ignored []string
}

// buildCaches are ignored dirs that a worktree remove may delete: they
// are rebuilt, never work.
var buildCaches = map[string]bool{"node_modules": true, "target": true, "dist": true, "build": true,
	".cache": true, "__pycache__": true, ".venv": true, ".next": true, ".turbo": true}

// Fetch runs `git fetch --quiet origin main`.
func (CLI) Fetch(ctx context.Context, root string) error {
	var extra []string
	if ssh := batchSSH(os.Getenv, func() string {
		out, _ := run(ctx, root, "git", "config", "--get", "core.sshCommand")
		return strings.TrimSpace(string(out))
	}); ssh != "" {
		extra = append(extra, "GIT_SSH_COMMAND="+ssh)
	}
	_, err := runEnv(ctx, root, extra, "git", "fetch", "--quiet", "origin", "main")
	return err
}

// batchSSH is the ssh command for a fetch that must not prompt: the
// user's own (GIT_SSH_COMMAND, else core.sshCommand, else ssh) with
// BatchMode on, so a custom key or jump host still works. With only
// GIT_SSH set (a program, not a command line) it returns "": that program
// is left as it is, and GIT_TERMINAL_PROMPT and the closed stdin remain.
func batchSSH(getenv func(string) string, coreSSH func() string) string {
	base := getenv("GIT_SSH_COMMAND")
	if base == "" {
		base = coreSSH()
	}
	if base == "" {
		if getenv("GIT_SSH") != "" {
			return ""
		}
		base = "ssh"
	}
	return base + " -o BatchMode=yes"
}

// WorktreeState reads the worktree's status with its ignored files and
// whether its HEAD is an ancestor of origin/main.
func (CLI) WorktreeState(ctx context.Context, wt string) (WorktreeInfo, error) {
	var info WorktreeInfo
	out, err := run(ctx, wt, "git", "status", "--porcelain=v1", "-z", "--ignored=matching")
	if err != nil {
		return info, err
	}
	for _, rec := range strings.Split(string(out), "\x00") {
		switch {
		case len(rec) < 4:
		case strings.HasPrefix(rec, "!! "):
			// Only a cache directory itself is exempt (git lists a wholly
			// ignored directory as one "dir/" entry); an ignored file
			// inside a dir named like one still counts.
			p := strings.TrimSuffix(rec[3:], "/")
			if !strings.HasSuffix(rec, "/") || !buildCaches[path.Base(p)] {
				info.Ignored = append(info.Ignored, rec[3:])
			}
		default:
			info.Dirty = true
		}
	}
	_, err = run(ctx, wt, "git", "merge-base", "--is-ancestor", "HEAD", "origin/main")
	var exit *exec.ExitError
	switch {
	case err == nil:
		info.Merged = true
		return info, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return info, nil
	}
	return info, err
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
func brList(ctx context.Context, root string, db []string, status, agent string) ([]brIssue, error) {
	args := append(brRead(db), "list", "--status", status, "--assignee", agent, "--limit", "0", "--json")
	out, err := run(ctx, root, "br", args...)
	if err != nil {
		return nil, err
	}
	return parseBrList(out)
}

// brRead are br's leading arguments for a read of db: the database, and
// no import of its JSONL, no flush and no freshness wait, so reading
// another repository's database takes no write lock there.
func brRead(db []string) []string {
	return append(append([]string{}, db...), "--no-auto-import", "--no-auto-flush", "--allow-stale")
}

// ErrNoBeadDB means no bead database can be searched for a project, so a
// handoff cannot be verified.
var ErrNoBeadDB = errors.New("no bead database: the project has no .beads and nhm's config names none ([lifecycle] bead_dbs or NHM_BEAD_DBS)")

// beadDBs are the databases to search: the project's own (br finds it
// from root) and every one the config names, as br's leading arguments.
// An agent's beads may live in another repository's database (nhm's own
// beads are ACFS's), so a project's database alone could find nothing
// and pass every check.
// projectBeadDB is the database file of root's .beads: beads.db, else the
// only *.db there; "" when there is none.
func projectBeadDB(root string) string {
	dir := filepath.Join(root, ".beads")
	if fi, err := os.Stat(filepath.Join(dir, "beads.db")); err == nil && fi.Mode().IsRegular() {
		return filepath.Join(dir, "beads.db")
	}
	if list, _ := filepath.Glob(filepath.Join(dir, "*.db")); len(list) == 1 {
		return list[0]
	}
	return ""
}

func (c CLI) beadDBs(root string) ([][]string, error) {
	var dbs [][]string
	// The project's own database by its path, so BEADS_DB in the
	// environment cannot put another one in its place.
	if own := projectBeadDB(root); own != "" {
		dbs = append(dbs, []string{"--db", own})
	}
	extra := c.BeadDBs
	if c.LoadBeadDBs != nil {
		more, err := c.LoadBeadDBs()
		if err != nil {
			return nil, fmt.Errorf("the bead databases of nhm's config: %w", err)
		}
		extra = append(append([]string{}, extra...), more...)
	}
	for _, db := range extra {
		dbs = append(dbs, []string{"--db", db})
	}
	if len(dbs) == 0 {
		return nil, ErrNoBeadDB
	}
	return dbs, nil
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
func (c CLI) Assigned(ctx context.Context, root, agent string) ([]Bead, error) {
	dbs, err := c.beadDBs(root)
	if err != nil {
		return nil, err
	}
	var beads []Bead
	seen := map[string]bool{}
	for _, db := range dbs {
		found, err := assignedIn(ctx, root, db, agent)
		if err != nil {
			return nil, err
		}
		for _, b := range found {
			if !seen[b.ID] {
				seen[b.ID] = true
				beads = append(beads, b)
			}
		}
	}
	return beads, nil
}

func assignedIn(ctx context.Context, root string, db []string, agent string) ([]Bead, error) {
	issues, err := brList(ctx, root, db, "in_progress", agent)
	if err != nil {
		return nil, err
	}
	var beads []Bead
	for _, is := range issues {
		if is.Assignee != "" && is.Assignee != agent {
			continue
		}
		b := Bead{ID: is.ID, Title: is.Title, Status: is.Status}
		args := append(brRead(db), "comments", "list", is.ID, "--json")
		out, err := run(ctx, root, "br", args...)
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
func (c CLI) Closed(ctx context.Context, root, agent string) ([]string, error) {
	dbs, err := c.beadDBs(root)
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, db := range dbs {
		issues, err := brList(ctx, root, db, "closed", agent)
		if err != nil {
			return nil, err
		}
		for _, is := range issues {
			if (is.Assignee == "" || is.Assignee == agent) && !seen[is.ID] {
				seen[is.ID] = true
				ids = append(ids, is.ID)
			}
		}
	}
	return ids, nil
}

// InProgress lists the beads in progress in root's databases (the
// project's own and the config's), by assignee: one br list per
// database, for nhm status (acfs-9hkh.82).
func (c CLI) InProgress(ctx context.Context, root string) (map[string][]string, error) {
	dbs, err := c.beadDBs(root)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, db := range dbs {
		args := append(brRead(db), "list", "--status", "in_progress", "--limit", "0", "--json")
		raw, err := run(ctx, root, "br", args...)
		if err != nil {
			return nil, err
		}
		issues, err := parseBrList(raw)
		if err != nil {
			return nil, err
		}
		for _, is := range issues {
			if is.Assignee != "" && !seen[is.ID] {
				seen[is.ID] = true
				out[is.Assignee] = append(out[is.Assignee], is.ID)
			}
		}
	}
	return out, nil
}

// Known lists beads of every status assigned to the agent, in each
// database, and reports whether any exists.
func (c CLI) Known(ctx context.Context, root, agent string) (bool, error) {
	dbs, err := c.beadDBs(root)
	if err != nil {
		return false, err
	}
	for _, db := range dbs {
		issues, err := brList(ctx, root, db, "all", agent)
		if err != nil {
			return false, err
		}
		for _, is := range issues {
			if is.Assignee == "" || is.Assignee == agent {
				return true, nil
			}
		}
	}
	return false, nil
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
