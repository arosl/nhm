// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/tools/xf.go
// and internal/tools/adapter.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: this header; only the search
// (Detect, Search and their types) is kept, without ntm's tool registry,
// version, capability, health and doctor checks; the names lose ntm's "XF"
// prefix (XFAdapter is Adapter); the binary is a field; the output limit is
// an io.LimitReader in place of ntm's LimitedBuffer.

// Package xf searches an indexed X/Twitter archive with the xf CLI, for
// the palette's xf search (ctrl+k).
package xf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// ErrTimeout means xf did not answer within the adapter's timeout.
var ErrTimeout = errors.New("operation timed out")

// ErrOutputLimitExceeded means xf printed more than the output limit.
var ErrOutputLimitExceeded = errors.New("output limit exceeded")

// outputLimit bounds what a search reads from xf; tests lower it.
var outputLimit = 10 * 1024 * 1024

// Adapter provides integration with the XF (X Find) tool.
// XF is a CLI for indexing and searching X/Twitter data archives,
// supporting full-text search with BM25 ranking via Tantivy.
type Adapter struct {
	// Binary is the xf executable ("xf": found on PATH).
	Binary  string
	Timeout time.Duration
}

// NewAdapter creates a new XF adapter
func NewAdapter() *Adapter {
	return &Adapter{Binary: "xf", Timeout: 30 * time.Second}
}

// Detect checks if xf is installed
func (a *Adapter) Detect() (string, bool) {
	path, err := exec.LookPath(a.Binary)
	if err != nil {
		return "", false
	}
	return path, true
}

// SearchResult is one record of `xf search --format json`, which prints a
// bare array of these.
type SearchResult struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	CreatedAt  string  `json:"created_at,omitempty"`
	ResultType string  `json:"result_type,omitempty"` // tweet, like, dm, grok, bookmark
	Score      float64 `json:"score,omitempty"`
}

// SearchParams narrows an xf search; zero values keep xf's defaults.
type SearchParams struct {
	Limit int
	Mode  string // lexical, semantic, hybrid, two-tier
	Sort  string // relevance, date, date-desc, engagement
}

// Search performs a full-text search on the indexed archive
func (a *Adapter) Search(ctx context.Context, query string, params SearchParams) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.Timeout)
	defer cancel()

	// --format is xf's global output selector; "--" keeps a query that starts
	// with "-" from being parsed as a flag.
	args := []string{"search", "--format", "json"}
	if params.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(params.Limit))
	}
	if params.Mode != "" {
		args = append(args, "--mode", params.Mode)
	}
	if params.Sort != "" {
		args = append(args, "--sort", params.Sort)
	}
	args = append(args, "--", query)

	cmd := exec.CommandContext(ctx, a.Binary, args...)
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("xf search failed: %w", err)
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, int64(outputLimit)+1))
	if len(out) > outputLimit {
		// Stop xf, which may still be writing.
		cancel()
		_ = cmd.Wait()
		return nil, ErrOutputLimitExceeded
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, ErrTimeout
		}
		return nil, fmt.Errorf("xf search failed: %w: %s", err, stderr.String())
	}
	if readErr != nil {
		return nil, fmt.Errorf("xf search failed: %w", readErr)
	}

	// With no matches xf prints nothing on stdout (its "[]" goes through a
	// message channel that is silent off a terminal). Any other output that is
	// not a JSON array is a contract break, not an empty result.
	output := bytes.TrimSpace(out)
	if len(output) == 0 {
		return []SearchResult{}, nil
	}
	var results []SearchResult
	if err := json.Unmarshal(output, &results); err != nil {
		return nil, fmt.Errorf("failed to parse xf search results: %w", err)
	}
	if results == nil {
		results = []SearchResult{}
	}

	return results, nil
}
