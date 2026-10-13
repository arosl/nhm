package owner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// serverDownImport is the import's answer when no herdr server runs: the
// socket is missing.
func serverDownImport(context.Context, bool, *herdr.SessionSnapshot) (bindings.ImportReport, error) {
	return bindings.ImportReport{}, fmt.Errorf("bindings: import: %w", &herdr.TransportError{Method: "session.snapshot",
		Err: &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ENOENT}}})
}

// emptyExport is a fresh install's Bash: no token, no wake state.
func (w *world) emptyExport(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(State{Format: StateFormat, Version: StateVersion, ExportedAt: "2026-10-13T20:00:00Z",
		Tokens: []TokenRef{}, Wake: []WakeEntry{}, Keep: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	w.bash.export = data
}

// nhm-swm (ACFS acfs-9hkh.93): a fresh install has no herdr server, and
// its Bash keeps nothing. The takeover goes on: the dry run passes and
// changes nothing, and the real one makes nhm the owner with nothing
// imported.
func TestTakeoverWithNoServerAndNothingToImport(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.emptyExport(t)
	w.env.ImportBindings = serverDownImport
	w.env.Exclude = nil // a fresh install names no agent to leave behind

	rep, err := Takeover(ctx, w.env, true)
	if err != nil || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "herdr is not running") {
		t.Fatalf("dry run = %+v %v", rep, err)
	}
	if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash {
		t.Fatalf("a dry run changed the owner: %+v", o)
	}

	// The real pass imports from the empty snapshot the check decided on
	// and does not dial herdr again (acfs-9hkh.85).
	var redialed bool
	w.env.ImportBindings = func(ctx context.Context, dry bool, snap *herdr.SessionSnapshot) (bindings.ImportReport, error) {
		if snap == nil {
			if !dry {
				redialed = true
			}
			return serverDownImport(ctx, dry, snap)
		}
		return bindings.ImportReport{DryRun: dry, Items: []bindings.ImportItem{}, Warnings: []string{}, Snapshot: snap}, nil
	}
	rep, err = Takeover(ctx, w.env, false)
	if redialed {
		t.Fatal("the real pass read herdr again instead of the checked snapshot")
	}
	if err != nil || rep.Owner.Owner != core.OwnerNhm || rep.Bindings == nil || len(rep.Bindings.Items) != 0 ||
		len(rep.Tokens) != 0 || len(rep.Wake) != 0 {
		t.Fatalf("takeover = %+v %v", rep, err)
	}
	if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerNhm || o.Generation != 1 {
		t.Fatalf("owner = %+v", o)
	}
}

// With no server, a Bash that keeps tokens or wake state refuses, dry or
// not: its agents need the server for their import, and the Bash stays
// the owner.
func TestTakeoverWithNoServerRefusesTheBashsAgents(t *testing.T) {
	ctx := context.Background()
	for _, dry := range []bool{true, false} {
		w := newWorld(t)
		w.export(t, "greencastle")
		w.env.ImportBindings = serverDownImport
		_, err := Takeover(ctx, w.env, dry)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "start herdr") {
			t.Fatalf("dry=%v: %v", dry, err)
		}
		if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash || o.Generation != 0 {
			t.Fatalf("dry=%v: owner = %+v", dry, o)
		}
	}
}

// Any other herdr failure (a server that answers badly) still fails the
// takeover: only a server that is not running counts as "no pane".
func TestTakeoverOtherHerdrFailuresStillFail(t *testing.T) {
	w := newWorld(t)
	w.emptyExport(t)
	w.env.ImportBindings = func(context.Context, bool, *herdr.SessionSnapshot) (bindings.ImportReport, error) {
		return bindings.ImportReport{}, fmt.Errorf("bindings: import: %w", &herdr.TransportError{Method: "session.snapshot",
			Err: errors.New("decode reply: unexpected EOF")})
	}
	if _, err := Takeover(context.Background(), w.env, false); err == nil || !strings.Contains(err.Error(), "cannot be imported") {
		t.Fatalf("takeover = %v", err)
	}
	if o, _ := core.ReadOwner(w.env.InstanceDir); o.Owner != core.OwnerBash {
		t.Fatalf("owner = %+v", o)
	}
}
