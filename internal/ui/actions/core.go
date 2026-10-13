package actions

import (
	"context"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/core/wait"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
)

// Core is nhm's core as the caller runs it for the UI's context: the CLI
// wires each field with the same Env its verb uses, under the owner lock,
// and refuses when nhm does not own the herdr server. The handlers call
// these and nothing else. A nil field makes the actions that need it
// unavailable.
type Core struct {
	// SendBase is the send request for the UI's context, as the CLI
	// resolves it for nhm send: session, workspace, project, scope and the
	// enrichment defaults. A handler sets the selector, Expect, the text
	// and the operation id on a copy.
	SendBase func(ctx context.Context) (send.Request, error)
	// Send runs nhm's send (send.Send); the palette's Sender is the same.
	Send func(ctx context.Context, req send.Request) (send.Result, error)
	// Interrupt sends each expected binding's interrupt key (wait.Interrupt
	// with Expect): the CLI loads the bindings by id; one that is gone or
	// at another generation is refused, never replaced.
	Interrupt func(ctx context.Context, expect map[string]int64, opID string) (wait.InterruptResult, error)
	// Retire runs lifecycle.Retire.
	Retire func(ctx context.Context, req lifecycle.RetireRequest) (lifecycle.RetireResult, error)
	// Recycle runs lifecycle.Recycle.
	Recycle func(ctx context.Context, req lifecycle.RecycleRequest) (lifecycle.RecycleResult, error)

	// Kill runs lifecycle.KillWith: without Yes the checks only (the
	// review), with Yes the kill, as nhm kill --op-id req.OperationID.
	Kill func(ctx context.Context, req lifecycle.KillRequest) (lifecycle.KillResult, error)
	// Reap runs lifecycle.Reap with nhm reap's keep list and mail check: a
	// dry run is the review; a sweep retires exactly req.Only. opID is the
	// review's operation id.
	Reap func(ctx context.Context, req lifecycle.ReapRequest, opID string) ([]lifecycle.ReapItem, error)
	// Wake runs one manual wake pass for the targets (nhm wake
	// --recipients).
	Wake func(ctx context.Context, targets []ui.Target, opID string) (wake.Report, error)
	// New creates a workspace with the label and the project as its cwd,
	// with no agent (nhm new); it returns the workspace's id.
	New func(ctx context.Context, label, project, opID string) (string, error)
	// Spawn runs nhm spawn or add: a dry run is the review (admission and
	// the plan); else the spawn, under opID, where admission runs again.
	Spawn func(ctx context.Context, req SpawnRequest, opID string) (spawn.Result, error)
	// Operation reads one operation's record and receipts
	// (lifecycle.ReadOperation); it only reads. A form shows a running
	// operation's progress with it.
	Operation func(ctx context.Context, id string) (lifecycle.RecentOperation, error)
}

// SpawnRequest is a spawn or an add as the spawn wizard collects it. The
// CLI adds the project (the surface's directory) and the enrichment of
// nhm's config.
type SpawnRequest struct {
	// Session is the workspace label; Add adds to an existing workspace.
	Session   string       `json:"session"`
	Add       bool         `json:"add,omitempty"`
	Agents    []spawn.Want `json:"agents"`
	Label     string       `json:"label,omitempty"`
	Group     string       `json:"group,omitempty"`
	Prompt    string       `json:"prompt,omitempty"`
	NoKickoff bool         `json:"no_kickoff,omitempty"`
	// Memory puts CM rules in the kickoff; CASS adds CASS context.
	Memory bool `json:"memory,omitempty"`
	CASS   bool `json:"cass,omitempty"`
	DryRun bool `json:"dry_run,omitempty"`
}

func needsSeam(ok bool, verb string) string {
	if !ok {
		return "this surface cannot " + verb
	}
	return ""
}

func needsKill(c Core) string  { return needsSeam(c.Kill != nil, "kill") }
func needsReap(c Core) string  { return needsSeam(c.Reap != nil, "reap") }
func needsWake(c Core) string  { return needsSeam(c.Wake != nil, "wake agents") }
func needsNew(c Core) string   { return needsSeam(c.New != nil, "create a workspace") }
func needsSpawn(c Core) string { return needsSeam(c.Spawn != nil, "spawn") }

// needsSend is why the core cannot send.
func needsSend(c Core) string {
	if c.Send == nil || c.SendBase == nil {
		return "this surface cannot send"
	}
	return ""
}

func needsInterrupt(c Core) string {
	if c.Interrupt == nil {
		return "this surface cannot interrupt"
	}
	return ""
}

func needsRetire(c Core) string {
	if c.Retire == nil {
		return "this surface cannot retire"
	}
	return ""
}

func needsRecycle(c Core) string {
	if c.Recycle == nil {
		return "this surface cannot recycle"
	}
	return ""
}
