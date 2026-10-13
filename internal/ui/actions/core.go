package actions

import (
	"context"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/wait"
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
}

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
