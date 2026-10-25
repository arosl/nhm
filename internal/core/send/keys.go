package send

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// KeysHerdr is what Keys needs from herdr; *herdr.Client has it.
type KeysHerdr interface {
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentSendKeys(ctx context.Context, target string, keys ...string) error
}

// KeysBindings is what Keys needs from the bindings; *bindings.Store has
// it.
type KeysBindings interface {
	Get(ctx context.Context, id string) (bindings.Binding, error)
}

// KeysEnv is what Keys runs against.
type KeysEnv struct {
	Herdr       KeysHerdr
	Bindings    KeysBindings
	InstanceDir string
}

// KeysRequest is a sequence of keys for the agent in one pane: the answer
// to a dialog, which no prompt can give.
type KeysRequest struct {
	PaneID string
	// Binding is the pane's active binding, as the caller read it; nil
	// for an agent that nhm did not bind.
	Binding *bindings.Binding
	// Keys are herdr's key names, sent one at a time.
	Keys []string
	// Gap is the pause between two keys. Pauses, when set, gives the
	// pause before each key instead (Pauses[0] is not used).
	Gap    time.Duration
	Pauses []time.Duration
	// Before runs before key i is sent, after the gap; an error stops the
	// sequence there, and Keys returns it.
	Before func(i int) error
}

// ErrNoAgent means the pane holds no agent of the expected kind: Keys
// types nothing into a shell.
var ErrNoAgent = errors.New("the pane holds no agent")

// KeyError is herdr's refusal of key Index of the sequence; the keys
// before it were sent.
type KeyError struct {
	Index int
	Err   error
}

func (e *KeyError) Error() string { return fmt.Sprintf("send key %d: %v", e.Index, e.Err) }
func (e *KeyError) Unwrap() error { return e.Err }

// Keys types a sequence of keys to an agent: the one key path of the
// robot verbs that answer the agent's screen (rule 5). A bound agent's
// target lock is held for the whole sequence, as a send holds it, so no
// other nhm writer acts on the agent between two keys; the binding must
// still hold the pane, and herdr must see an agent of its kind there. An
// agent that nhm did not bind is checked only for an agent in the pane.
// sent is the number of keys herdr took.
func Keys(ctx context.Context, env KeysEnv, req KeysRequest) (sent int, err error) {
	if b := req.Binding; b != nil {
		lctx, cancel := context.WithTimeout(ctx, lockWait)
		defer cancel()
		unlock, err := lockTarget(lctx, env.InstanceDir, b.ID)
		if err != nil {
			return 0, err
		}
		defer unlock()
		cur, err := env.Bindings.Get(ctx, b.ID)
		if err != nil {
			return 0, err
		}
		if cur.Generation != b.Generation || cur.Status != bindings.Active || cur.PaneID != req.PaneID {
			return 0, fmt.Errorf("the binding changed since selection (generation %d, %s, pane %s)", cur.Generation, cur.Status, cur.PaneID)
		}
	}
	a, err := env.Herdr.AgentGet(ctx, req.PaneID)
	switch {
	case herdr.ErrorCode(err) == herdr.CodeAgentNotFound:
		return 0, fmt.Errorf("%w: %s", ErrNoAgent, req.PaneID)
	case err != nil:
		return 0, err
	case a.Agent == "":
		return 0, fmt.Errorf("%w: %s", ErrNoAgent, req.PaneID)
	case req.Binding != nil && req.Binding.Kind != "" && a.Agent != req.Binding.Kind:
		return 0, fmt.Errorf("%w: the pane holds a %s agent; the binding is %s", ErrNoAgent, a.Agent, req.Binding.Kind)
	}
	for i, key := range req.Keys {
		pause := req.Gap
		if req.Pauses != nil {
			pause = 0
			if i < len(req.Pauses) {
				pause = req.Pauses[i]
			}
		}
		if i > 0 && pause > 0 {
			timer := time.NewTimer(pause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return sent, ctx.Err()
			case <-timer.C:
			}
		}
		if req.Before != nil {
			if err := req.Before(i); err != nil {
				return sent, err
			}
		}
		if err := env.Herdr.AgentSendKeys(ctx, req.PaneID, key); err != nil {
			return sent, &KeyError{Index: i, Err: err}
		}
		sent++
	}
	return sent, nil
}
