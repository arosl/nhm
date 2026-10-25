package lifecycle

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// ntm's kill on herdr (nhm-2p6.5.3; the operator's ruling, 2026-10-24:
// kill does what ntm's kill does). ntm kills the session; on herdr that
// closes the workspace, and with it every pane. ntm then ends the agent
// processes that outlived their panes (SIGTERM, then SIGKILL) and
// releases the file reservations of the session's agents in Agent Mail.
// A kill of tagged panes closes those panes and does neither. nhm's
// bindings of the closed panes are retired with the reason "kill". The
// identities stay in Agent Mail, as ntm leaves them; nhm retire, which
// runs retire's checks, is nhm's own verb for an agent's end.

// KillSessionRequest is one kill: the whole workspace, or with Panes only
// those panes of it.
type KillSessionRequest struct {
	WorkspaceID string
	// Panes are the panes to close (ntm's --tag); nil closes the
	// workspace.
	Panes []string
	// Reaper ends the processes that outlive the workspace's panes; nil
	// leaves them (a test's herdr reports host pids).
	Reaper Reaper
}

// Reaper finds and ends left-over processes.
type Reaper interface {
	// Descendants are the processes below pid.
	Descendants(pid int) []int
	// Reap ends those of pids still alive and returns the ones it
	// signalled.
	Reap(pids []int) []int
}

// KillSessionResult is what a kill did.
type KillSessionResult struct {
	WorkspaceID     string   `json:"workspace_id"`
	WorkspaceClosed bool     `json:"workspace_closed"`
	ClosedPanes     []string `json:"closed_panes,omitempty"`
	// Retired are the agents whose bindings the kill retired.
	Retired []string `json:"retired"`
	// Reaped are the left-over processes the kill signalled.
	Reaped []int `json:"reaped,omitempty"`
	// Released are the agents whose Agent Mail reservations it released.
	Released []string `json:"released,omitempty"`
	// Warnings are best-effort steps that failed; the kill stands.
	Warnings []string `json:"warnings,omitempty"`
}

// killMailTimeout bounds the reservation release, as ntm's
// agentMailKillCleanupTimeout: a slow Agent Mail never holds the kill.
const killMailTimeout = 5 * time.Second

// KillSession kills a workspace, or some of its panes, as ntm's kill does.
// When the calling pane is among the panes that close, the kill retires
// the bindings and releases the reservations first, since closing that
// pane ends this process.
func KillSession(ctx context.Context, env Env, req KillSessionRequest) (KillSessionResult, error) {
	res := KillSessionResult{WorkspaceID: req.WorkspaceID, Retired: []string{}}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return res, err
	}
	var panes []string
	for _, p := range snap.Panes {
		if p.WorkspaceID == req.WorkspaceID && (req.Panes == nil || slices.Contains(req.Panes, p.PaneID)) {
			panes = append(panes, p.PaneID)
		}
	}
	// A displaced binding waits for an adopt in its pane, which the kill
	// closes too.
	live, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: req.WorkspaceID, Statuses: []bindings.Status{bindings.Active, bindings.Displaced}})
	if err != nil {
		return res, err
	}
	var doomed []bindings.Binding
	for _, b := range live {
		if req.Panes == nil || slices.Contains(panes, b.PaneID) {
			doomed = append(doomed, b)
		}
	}
	whole := req.Panes == nil
	// The processes under each pane's shell, read while the shells live.
	var leftovers []int
	if whole && req.Reaper != nil {
		seen := map[int]bool{}
		for _, p := range panes {
			info, err := env.Herdr.PaneProcessInfo(ctx, p)
			if err != nil || info.ShellPID == nil {
				continue
			}
			for _, pid := range req.Reaper.Descendants(*info.ShellPID) {
				if !seen[pid] {
					seen[pid] = true
					leftovers = append(leftovers, pid)
				}
			}
		}
	}
	after := func() {
		for _, b := range doomed {
			if _, err := env.Bindings.Retire(ctx, b.ID, b.Generation, "kill"); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("retire the binding of %s: %v", b.MailName, err))
				continue
			}
			res.Retired = append(res.Retired, b.MailName)
		}
		if whole {
			res.Released, res.Warnings = releaseReservations(ctx, env, doomed, res.Released, res.Warnings)
		}
	}
	selfInside := env.Caller != "" && slices.Contains(panes, env.Caller)
	if selfInside {
		after()
	}
	if whole {
		// herdr may have closed the workspace with its last pane.
		if err := env.Herdr.WorkspaceClose(ctx, req.WorkspaceID); err != nil && !strings.HasSuffix(herdr.ErrorCode(err), "not_found") {
			return res, fmt.Errorf("close workspace %s: %w", req.WorkspaceID, err)
		}
		res.WorkspaceClosed = true
		if req.Reaper != nil {
			res.Reaped = req.Reaper.Reap(leftovers)
		}
	} else {
		for _, p := range panes {
			if err := env.Herdr.PaneClose(ctx, p); err != nil && !strings.HasSuffix(herdr.ErrorCode(err), "not_found") {
				return res, fmt.Errorf("killing pane %s: %w", p, err)
			}
			res.ClosedPanes = append(res.ClosedPanes, p)
		}
	}
	if !selfInside {
		after()
	}
	return res, nil
}

// releaseReservations releases every Agent Mail file reservation of each
// agent, best-effort and within killMailTimeout.
func releaseReservations(ctx context.Context, env Env, list []bindings.Binding, released, warnings []string) ([]string, []string) {
	if env.Mail == nil {
		return released, warnings
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killMailTimeout)
	defer cancel()
	for _, b := range list {
		if b.MailProjectKey == "" || b.MailName == "" {
			continue
		}
		var out map[string]any
		err := env.Mail.Call(mctx, "release_file_reservations", map[string]any{"project_key": b.MailProjectKey, "agent_name": b.MailName}, &out)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("release the reservations of %s: %v", b.MailName, err))
			continue
		}
		released = append(released, b.MailName)
	}
	return released, warnings
}
