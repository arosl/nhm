// Package cass runs CASS (the `cass` CLI) for nhm: a search for context
// and for duplicate work, and the index of one transcript (plan-v8 2.13).
//
// Experiment 5 (acfs-9hkh.5) decided how nhm indexes: cass indexes a
// transcript only under its provider's own root, so nhm indexes the
// original in place with `--watch-once` and an idempotency key. cass
// answers success even when it indexed nothing, so nhm confirms the index
// by the exact session path.
package cass

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs the cass binary with args and returns its stdout. Tests
// replace it.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// ErrNotInstalled: there is no cass binary.
var ErrNotInstalled = errors.New("cass is not installed")

// Exec runs the binary at path (default "cass") from PATH, without its
// update prompt, auto refresh and progress events.
func Exec(path string) Runner {
	if path == "" {
		path = "cass"
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Env = append(os.Environ(), "CODING_AGENT_SEARCH_NO_UPDATE_PROMPT=1", "CASS_AUTO_REFRESH=0",
			"CASS_INDEX_NO_PROGRESS_EVENTS=1", "NO_COLOR=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, ErrNotInstalled
			}
			return out, fmt.Errorf("cass %s: %w: %s", firstArg(args), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// Client runs cass commands.
type Client struct {
	Run Runner
	// DataDir is --data-dir, or empty for cass's own.
	DataDir string
}

// Hit is one search hit.
type Hit struct {
	Title          string  `json:"title"`
	Snippet        string  `json:"snippet"`
	Content        string  `json:"content"`
	Score          float64 `json:"score"`
	SourcePath     string  `json:"source_path"`
	Agent          string  `json:"agent"`
	Workspace      string  `json:"workspace"`
	CreatedAtMS    int64   `json:"created_at"`
	ConversationID int64   `json:"conversation_id"`
	LineNumber     int     `json:"line_number"`
	MatchType      string  `json:"match_type"`
}

// ID is the hit's stable id: its session path and line.
func (h Hit) ID() string {
	return h.SourcePath + ":" + strconv.Itoa(h.LineNumber)
}

// CreatedAt is the hit's time, or zero.
func (h Hit) CreatedAt() time.Time {
	if h.CreatedAtMS <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(h.CreatedAtMS).UTC()
}

// Search is one query.
type Search struct {
	Query     string
	Workspace string
	Limit     int
	// Days limits the hits to the last N days; 0 is no limit.
	Days int
	// MaxContent truncates each hit's content and snippet.
	MaxContent int
}

// args adds --data-dir. Every value goes as --flag=value and every
// positional after "--": a query or a path that starts with "-" is never
// read as a flag.
func (c Client) args(args ...string) []string {
	if c.DataDir != "" {
		args = append(args, "--data-dir="+c.DataDir)
	}
	return args
}

// Search runs `cass search --robot`.
func (c Client) Search(ctx context.Context, s Search) ([]Hit, error) {
	args := []string{"search", "--robot"}
	if s.Limit > 0 {
		args = append(args, "--limit="+strconv.Itoa(s.Limit))
	}
	if s.Workspace != "" {
		args = append(args, "--workspace="+s.Workspace)
	}
	if s.Days > 0 {
		args = append(args, "--days="+strconv.Itoa(s.Days))
	}
	if s.MaxContent > 0 {
		args = append(args, "--max-content-length="+strconv.Itoa(s.MaxContent))
	}
	data, err := c.Run(ctx, append(c.args(args...), "--", s.Query)...)
	if err != nil {
		return nil, err
	}
	var out struct {
		Hits []Hit `json:"hits"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("cass search: the answer is not JSON: %w", err)
	}
	return out.Hits, nil
}

// Session is one indexed session.
type Session struct {
	Path      string `json:"path"`
	Workspace string `json:"workspace"`
	Agent     string `json:"agent"`
	Title     string `json:"title"`
}

// Sessions lists indexed sessions, of one workspace when it is not empty.
func (c Client) Sessions(ctx context.Context, workspace string, limit int) ([]Session, error) {
	args := []string{"sessions", "--json", "--limit=" + strconv.Itoa(limit)}
	if workspace != "" {
		args = append(args, "--workspace="+workspace)
	}
	data, err := c.Run(ctx, c.args(args...)...)
	if err != nil {
		return nil, err
	}
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("cass sessions: the answer is not JSON: %w", err)
	}
	return out.Sessions, nil
}

// Index outcomes.
const (
	Indexed     = "indexed"
	NotFoundYet = "not_found_yet"
	// Queued: another `cass index` run holds the lock of cass's data
	// dir, so nhm's run did nothing. On an ACFS host that is always the
	// watcher (`cass index --watch`, acfs-cass-index.service); it indexes
	// the original in place itself, once the file stops changing.
	Queued = "queued"
)

// confirmLimit bounds the sessions read to confirm an index.
const confirmLimit = 200

// IndexSession indexes one transcript in place and confirms it by its
// exact path. key is the idempotency key, <binding>-<session>. A
// session that cass does not list is "not found yet", never an error:
// the transcript is not lost. When another run holds cass's lock (exit 7,
// kind index-busy), the session is Queued for that run unless cass lists
// it already.
func (c Client) IndexSession(ctx context.Context, path, workspace, key string) (string, error) {
	args := []string{"index", "--watch-once=" + path, "--json"}
	if key != "" {
		args = append(args, "--idempotency-key="+key)
	}
	data, err := c.Run(ctx, c.args(args...)...)
	// cass answers with JSON on stdout, a refusal too; stderr is empty
	// then, so the exit status alone would say nothing.
	var res struct {
		Success bool   `json:"success"`
		Kind    string `json:"kind"`
		Error   string `json:"error"`
	}
	jerr := json.Unmarshal(data, &res)
	busy := jerr == nil && res.Kind == "index-busy"
	switch {
	case busy:
	case err != nil && jerr == nil && res.Error != "":
		return "", fmt.Errorf("%w: %s (%s)", err, res.Error, res.Kind)
	case err != nil:
		return "", err
	case jerr != nil:
		return "", fmt.Errorf("cass index: the answer is not JSON: %w", jerr)
	case !res.Success:
		return "", fmt.Errorf("cass index %s did not succeed", path)
	}
	list, err := c.Sessions(ctx, workspace, confirmLimit)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Path == path {
			return Indexed, nil
		}
	}
	if busy {
		return Queued, nil
	}
	return NotFoundYet, nil
}
