package owner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Bash runs ACFS's export and import of its state.
type Bash interface {
	// Export prints the Bash's state (`acfs agents export`).
	Export(ctx context.Context) ([]byte, error)
	// Import writes a state file back (`acfs agents import FILE`). It
	// checks the whole file first and writes nothing if an entry is wrong.
	Import(ctx context.Context, file string) error
}

// Event is one Agent Mail delivery event of a recipient.
type Event struct {
	Cursor    int64  `json:"cursor"`
	MessageID int64  `json:"message_id"`
	Kind      string `json:"kind"`
}

// Events reads a recipient's delivery events.
type Events interface {
	// After lists every event after cursor (all pages).
	After(ctx context.Context, project, agent string, cursor int64) ([]Event, error)
	// Tail is the current cursor: mail after it is new.
	Tail(ctx context.Context, project, agent string) (int64, error)
}

// commandTimeout bounds one external command.
const commandTimeout = 60 * time.Second

func runCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// BashCLI runs `acfs agents export|import`. Acfs is the acfs command
// ("" is acfs on PATH).
type BashCLI struct{ Acfs string }

func (b BashCLI) bin() string {
	if b.Acfs == "" {
		return "acfs"
	}
	return b.Acfs
}

// Export runs `acfs agents export`.
func (b BashCLI) Export(ctx context.Context) ([]byte, error) {
	return runCmd(ctx, b.bin(), "agents", "export")
}

// Import runs `acfs agents import FILE`.
func (b BashCLI) Import(ctx context.Context, file string) error {
	_, err := runCmd(ctx, b.bin(), "agents", "import", file)
	return err
}

// EventsCLI reads delivery events with `am inbox-events`.
type EventsCLI struct{}

type eventPage struct {
	Events     []Event `json:"events"`
	NextCursor *int64  `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// After pages `am inbox-events --after N --limit 1000 --json`.
func (EventsCLI) After(ctx context.Context, project, agent string, cursor int64) ([]Event, error) {
	var all []Event
	for {
		out, err := runCmd(ctx, "am", "inbox-events", "--agent", agent, "--project", project,
			"--after", strconv.FormatInt(cursor, 10), "--limit", "1000", "--json")
		if err != nil {
			return nil, err
		}
		var p eventPage
		if err := json.Unmarshal(out, &p); err != nil || p.NextCursor == nil {
			return nil, fmt.Errorf("am inbox-events for %s: not a page of events: %v", agent, err)
		}
		all = append(all, p.Events...)
		if !p.HasMore {
			return all, nil
		}
		if *p.NextCursor <= cursor {
			return nil, fmt.Errorf("am inbox-events for %s: the cursor did not move past %d", agent, cursor)
		}
		cursor = *p.NextCursor
	}
}

// Tail runs `am inbox-events --position-now --json`.
func (EventsCLI) Tail(ctx context.Context, project, agent string) (int64, error) {
	out, err := runCmd(ctx, "am", "inbox-events", "--agent", agent, "--project", project, "--position-now", "--json")
	if err != nil {
		return 0, err
	}
	var p eventPage
	if err := json.Unmarshal(out, &p); err != nil || p.NextCursor == nil {
		return 0, fmt.Errorf("am inbox-events --position-now for %s: no cursor: %v", agent, err)
	}
	return *p.NextCursor, nil
}

// recipient reports an event that delivered mail to the agent.
func (e Event) recipient() bool { return e.Kind == "to" || e.Kind == "cc" || e.Kind == "bcc" }
