package spawn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// --spawn-safety (Request.Safety) refuses a session whose workspace
// exists, before anything is created or claimed, a dry run too, as ntm
// refuses to reuse a session. A new session spawns, and without Safety
// an existing one takes the agents (nhm-lrx).
func TestSafetyRefusesAnExistingSession(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	first := req(Want{Kind: "claude", Count: 1})
	first.Safety = true
	if res, err := Spawn(ctx, w.env, first); err != nil || !res.WorkspaceNew {
		t.Fatalf("a new session with safety = %+v %v", res, err)
	}
	calls := len(w.h.calls)
	for _, dry := range []bool{false, true} {
		again := req(Want{Kind: "claude", Count: 1})
		again.Safety, again.DryRun, again.OperationID = true, dry, "op-safety"
		_, err := Spawn(ctx, w.env, again)
		if !errors.Is(err, ErrSessionExists) ||
			!strings.Contains(err.Error(), "session 'swarm' already exists (--spawn-safety mode prevents reuse; use 'nhm kill swarm' first)") {
			t.Fatalf("dry %v: an existing session with safety = %v", dry, err)
		}
	}
	if len(w.h.calls) != calls {
		t.Fatalf("a refused spawn called herdr: %v", w.h.calls[calls:])
	}
	if _, err := w.db.Operation(ctx, "op-safety"); err == nil {
		t.Fatal("a refused spawn claimed its operation id")
	}
	if res, err := Spawn(ctx, w.env, req(Want{Kind: "claude", Count: 1})); err != nil || res.WorkspaceNew {
		t.Fatalf("an existing session without safety = %+v %v", res, err)
	}
}

// appearMail makes the session's workspace appear in herdr when the
// spawn registers its first identity, after the spawn's first look.
type appearMail struct {
	*fakeMail
	h    *fakeHerdr
	done bool
}

func (m *appearMail) Call(ctx context.Context, tool string, args, out any) error {
	if !m.done {
		m.done = true
		m.h.mu.Lock()
		m.h.workspaces = append(m.h.workspaces, herdr.WorkspaceInfo{WorkspaceID: "w7", Label: "swarm"})
		m.h.mu.Unlock()
	}
	return m.fakeMail.Call(ctx, tool, args, out)
}

// --spawn-safety looks again just before it makes the session, as ntm
// does: a session that appeared meanwhile is refused, and no second
// workspace of that label is made. A retry that changes only Safety is
// another request, and Safety with add is refused at once (YellowPeak's
// review of 3d64d95).
func TestSafetyLooksAgainBeforeItMakesTheSession(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.env.Mail = &appearMail{fakeMail: w.mail, h: w.h}
	r := req(Want{Kind: "claude", Count: 1})
	r.Safety = true
	res, _ := Spawn(ctx, w.env, r)
	if len(res.Targets) != 1 || res.Targets[0].Status != StatusFailed || !strings.Contains(res.Targets[0].Error, "appeared while the spawn ran") {
		t.Fatalf("targets = %+v", res.Targets)
	}
	for _, c := range w.h.calls {
		if strings.HasPrefix(c, "workspace.create") {
			t.Fatalf("a second workspace was made: %v", w.h.calls)
		}
	}

	w = newWorld(t)
	r = req(Want{Kind: "claude", Count: 1})
	r.OperationID = "op-fp"
	if _, err := Spawn(ctx, w.env, r); err != nil {
		t.Fatal(err)
	}
	r.Safety = true
	if _, err := Spawn(ctx, w.env, r); !errors.Is(err, store.ErrOperationIDConflict) {
		t.Fatalf("a retry with --spawn-safety added = %v", err)
	}

	r = req(Want{Kind: "claude", Count: 1})
	r.Safety, r.Add = true, true
	if _, err := Spawn(ctx, newWorld(t).env, r); err == nil || !strings.Contains(err.Error(), "--spawn-safety") {
		t.Fatalf("safety with add = %v", err)
	}
}
