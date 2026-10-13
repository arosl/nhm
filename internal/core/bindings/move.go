package bindings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/store"
)

// ErrAmbiguousMove means more than one pane could hold a moved binding's
// agent, so nhm moves the binding to none of them.
var ErrAmbiguousMove = errors.New("bindings: more than one pane holds the moved agent")

// Location is where a binding's agent lives in herdr.
type Location struct {
	WorkspaceID string
	TabID       string
	PaneID      string
}

// Relocate records that an active binding's occupant lives in another
// pane: herdr moved its pane, which on a move to another workspace gets
// a new id. The occupant is the same, so the generation stays (plan-v8
// 2.12). A new workspace gives a new slot there; the old slot's row
// stays. It fails with ErrStale when the binding changed or is not
// active, and with ErrPaneBusy when another active binding holds the
// pane.
func (s *Store) Relocate(ctx context.Context, id string, expected int64, to Location, ev Evidence) (Binding, error) {
	b, err := s.Get(ctx, id)
	if err != nil {
		return Binding{}, err
	}
	evidence := append(b.Evidence, ev)
	if len(evidence) > maxEvidence {
		evidence = evidence[len(evidence)-maxEvidence:]
	}
	_, evidenceJSON, err := encodeJSON(nil, evidence)
	if err != nil {
		return Binding{}, err
	}
	now := store.FormatTime(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkFree(ctx, tx, id, b.MailProjectKey, b.MailName, to.PaneID); err != nil {
		return Binding{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE bindings SET workspace_id = ?, tab_id = ?, pane_id = ?, evidence_json = ?,
		updated_at = ? WHERE binding_id = ? AND generation = ? AND status = 'active'`,
		null(to.WorkspaceID), null(to.TabID), null(to.PaneID), evidenceJSON, now, id, expected)
	if err != nil {
		return Binding{}, fmt.Errorf("bindings: relocate %s: %w", id, err)
	}
	if err := oneRow(res, id, expected); err != nil {
		return Binding{}, err
	}
	if to.WorkspaceID != "" && (to.WorkspaceID != b.WorkspaceID || b.Slot == "") {
		if err := allocateSlot(ctx, tx, id, to.WorkspaceID, to.TabID, now); err != nil {
			return Binding{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, fmt.Errorf("bindings: %w", err)
	}
	return s.Get(ctx, id)
}

// Moved finds an active binding's agent after herdr moved its pane to
// another workspace, which gives the pane a new id (nhm-p1o). It answers
// only when the binding's pane has left herdr's session and exactly one
// pane with no active binding holds the same agent: the same kind, and
// the same process (pid and start time), or, when the binding has no
// process, the same native session. A new process is a new occupant
// (adopt --handoff-from), never a move. ok is false when the pane is
// still there (its agent left) or no pane matches.
func Moved(ctx context.Context, h Herdr, s *Store, b Binding) (agent herdr.AgentInfo, ok bool, err error) {
	snap, err := h.Snapshot(ctx)
	if err != nil {
		return herdr.AgentInfo{}, false, fmt.Errorf("bindings: %w", err)
	}
	if _, there := findPane(snap, b.PaneID); there {
		return herdr.AgentInfo{}, false, nil
	}
	var found []herdr.AgentInfo
	for _, a := range snap.Agents {
		if a.Agent != b.Kind || a.PaneID == b.PaneID {
			continue
		}
		if _, err := s.ActiveByPane(ctx, a.PaneID); !errors.Is(err, ErrNotFound) {
			if err != nil {
				return herdr.AgentInfo{}, false, err
			}
			continue
		}
		if SameOccupant(ctx, h, b, a) {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 0:
		return herdr.AgentInfo{}, false, nil
	case 1:
		return found[0], true, nil
	}
	panes := make([]string, len(found))
	for i, a := range found {
		panes[i] = a.PaneID
	}
	return herdr.AgentInfo{}, false, fmt.Errorf("%w: %s could be in %s", ErrAmbiguousMove, b.MailName, strings.Join(panes, ", "))
}

// CallerPane is the pane id nhm uses for a caller whose HERDR_PANE_ID is
// id. herdr gives a pane a new id when it moves to another workspace, and
// the moved process keeps the old id in its environment. herdr answers
// the old id in its pane calls (pane.process_info, pane.get; not
// agent.get) with the new pane until the server restarts, which also
// ends the moved process (nhm-5qa). So the caller's pane is id while an
// active binding records it (nhm has not followed the move yet), else
// herdr's current id for it, else id. It is id when h is nil.
func CallerPane(ctx context.Context, s *Store, h Herdr, id string) string {
	if id == "" || h == nil {
		return id
	}
	if s != nil {
		if _, err := s.ActiveByPane(ctx, id); err == nil {
			return id
		}
	}
	info, err := h.PaneProcessInfo(ctx, id)
	if err != nil || info.PaneID == "" {
		return id
	}
	return info.PaneID
}

// SameOccupant reports whether the agent a, in any pane, is the
// binding's occupant: the binding's process (pid and start time) is in
// a's pane, or, when the binding has no process, a has its native
// session. It does not compare kinds.
func SameOccupant(ctx context.Context, h Herdr, b Binding, a herdr.AgentInfo) bool {
	if b.ForegroundPID != 0 {
		at := b
		at.PaneID = a.PaneID
		result, _ := checkProcess(ctx, h, at)
		return result == CheckOK
	}
	return b.NativeSession != nil && b.NativeSession.Value != "" && a.AgentSession != nil &&
		a.AgentSession.Value == b.NativeSession.Value
}
