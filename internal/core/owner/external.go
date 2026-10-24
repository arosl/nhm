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

// recipient reports an event that delivered mail to the agent.
func (e Event) recipient() bool { return e.Kind == "to" || e.Kind == "cc" || e.Kind == "bcc" }
