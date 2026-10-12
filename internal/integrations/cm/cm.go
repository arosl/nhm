// Package cm runs CM (cass-memory, the `cm` CLI) for nhm: the rules for a
// task, a helpful or harmful mark, and the implicit outcome of a session
// (plan-v8 2.13). nhm never runs `cm reflect`: it needs an LLM provider,
// spends quota and runs the user's hooks.
package cm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner runs the cm binary with args and returns its stdout. Tests
// replace it.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// WaitDelay is how long a cm call may outlive its context: the time to
// close the pipes a killed cm's children still hold.
var WaitDelay = 2 * time.Second

// Exec runs the binary at path (default "cm") from PATH.
func Exec(path string) Runner {
	if path == "" {
		path = "cm"
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, path, args...)
		// When ctx ends, cm is killed; a child of cm that holds its stdout
		// open must not stretch the call past the caller's limit (and the
		// feedback lease): after WaitDelay the pipes are closed.
		cmd.WaitDelay = WaitDelay
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, ErrNotInstalled
			}
			return out, fmt.Errorf("cm %s: %w: %s", firstArg(args), err, strings.TrimSpace(stderr.String()))
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

// ErrNotInstalled: there is no cm binary.
var ErrNotInstalled = errors.New("cm is not installed")

// Client runs cm commands.
type Client struct {
	Run Runner
}

// Rule is one playbook rule (CM calls it a bullet).
type Rule struct {
	ID        string  `json:"id"`
	Category  string  `json:"category,omitempty"`
	Content   string  `json:"content"`
	Kind      string  `json:"kind,omitempty"`
	Maturity  string  `json:"maturity,omitempty"`
	Negative  bool    `json:"isNegative,omitempty"`
	Score     float64 `json:"finalScore,omitempty"`
	Helpful   int     `json:"helpfulCount,omitempty"`
	Harmful   int     `json:"harmfulCount,omitempty"`
	Reasoning string  `json:"reasoning,omitempty"`
}

// Context is the answer of `cm context --json`.
type Context struct {
	Task         string `json:"task"`
	Rules        []Rule `json:"relevantBullets"`
	AntiPatterns []Rule `json:"antiPatterns"`
}

// envelope is cm's JSON envelope.
type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (c Client) call(ctx context.Context, out any, args ...string) error {
	data, err := c.Run(ctx, args...)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("cm %s: the answer is not JSON: %w", args[0], err)
	}
	if !env.Success {
		return fmt.Errorf("cm %s failed: %s", args[0], strings.TrimSpace(string(env.Error)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("cm %s: %w", args[0], err)
	}
	return nil
}

// Every value goes as --flag=value and every positional after "--": a
// prompt or a rule id that starts with "-" is never read as a flag.

// Context returns the rules for a task. workspace scopes them to a
// project when it is not empty.
func (c Client) Context(ctx context.Context, task, workspace string) (Context, error) {
	args := []string{"context", "--json"}
	if workspace != "" {
		args = append(args, "--workspace="+workspace)
	}
	var out Context
	err := c.call(ctx, &out, append(args, "--", task)...)
	return out, err
}

// Mark tells CM that a rule was helpful or harmful in a session.
// session is the transcript path, or empty.
func (c Client) Mark(ctx context.Context, rule string, helpful bool, session string) error {
	args := []string{"mark", "--json", "--harmful"}
	if helpful {
		args[2] = "--helpful"
	}
	if session != "" {
		args = append(args, "--session="+session)
	}
	return c.call(ctx, nil, append(args, "--", rule)...)
}

// Outcome records a session outcome for the rules it was shown.
func (c Client) Outcome(ctx context.Context, status string, rules []string, session, text string) error {
	args := []string{"outcome", "--json"}
	if session != "" {
		args = append(args, "--session="+session)
	}
	if text != "" {
		args = append(args, "--text="+text)
	}
	return c.call(ctx, nil, append(args, "--", status, strings.Join(rules, ","))...)
}

// OutcomeApply applies the recorded outcomes of one session. CM keys a
// session by the path that Outcome got.
func (c Client) OutcomeApply(ctx context.Context, session string) error {
	args := []string{"outcome-apply", "--json"}
	if session != "" {
		args = append(args, "--session="+session)
	}
	return c.call(ctx, nil, args...)
}
