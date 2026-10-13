package dashboard

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/board"
	"github.com/arosl/nhm/internal/ui/forms"
)

// spawnActionID is the table's spawn into this workspace: ntm's ctrl+w
// opens its spawn wizard (nhm add).
const spawnActionID = "spawn.add"

// spawnFunc is the core's spawn seam.
type spawnFunc = func(ctx context.Context, req actions.SpawnRequest, opID string) (spawn.Result, error)

// frozenSpawn wraps the spawn seam for the wizard: a spawn (not its dry
// run) checks the binary first and is refused when the board is frozen.
// The wizard submits from its own command, so the check is the binary's
// own (a stat), not the model's state.
func frozenSpawn(bin *board.Binary, run spawnFunc) spawnFunc {
	if bin == nil {
		return run
	}
	return func(ctx context.Context, req actions.SpawnRequest, opID string) (spawn.Result, error) {
		if !req.DryRun {
			if err := bin.Check(); err != nil {
				return spawn.Result{}, err
			}
		}
		return run(ctx, req, opID)
	}
}

// openWizard opens ntm's spawn wizard (the menu's, nhm-qbi.4) for the
// board's workspace: the kinds and counts, the dry run, then the spawn
// once through the board's guard.
func (m *Model) openWizard() tea.Cmd {
	if err := m.Frozen(); err != nil {
		m.say(true, err.Error())
		return nil
	}
	a, ok := actions.ByID(spawnActionID)
	if !ok || m.guard == nil {
		m.say(true, "spawn is not available here")
		return nil
	}
	if why := a.Unavailable(m.cfg.Context, m.cfg.Core); why != "" {
		m.say(true, a.Label+": "+why)
		return nil
	}
	// m.cfg.Core's spawn checks the binary at the call (New).
	w, h := m.wizardSize()
	m.wizard = forms.NewSpawnWizard(forms.Runner{Action: a, Core: m.cfg.Core, Guard: m.guard}, m.cfg.Context, m.cfg.Kinds, w, h)
	m.view = viewWizard
	return m.wizard.Init()
}

// wizardSize is the room the wizard sizes its own box in: the board's.
func (m *Model) wizardSize() (int, int) {
	return max(m.width, minWidth), max(m.height, minHeight)
}

// wizardUpdate hands a message to the open wizard; its done message
// closes it. A spawn still running when it closes runs on as its job.
func (m *Model) wizardUpdate(msg tea.Msg) tea.Cmd {
	if done, ok := msg.(forms.SpawnWizardDoneMsg); ok {
		m.wizard, m.view = nil, viewMain
		switch r := done.Result; {
		case done.Cancelled:
		case r.Err != nil:
			m.say(true, "spawn: "+r.Err.Error())
		case r.Outcome != nil:
			m.say(false, "spawn "+r.Outcome.OperationID+": "+r.Outcome.Status)
		}
		return nil
	}
	if m.wizard == nil {
		return nil
	}
	if _, ok := msg.(tea.WindowSizeMsg); ok {
		m.wizard.SetSize(m.wizardSize())
		return nil
	}
	_, cmd := m.wizard.Update(msg)
	return cmd
}
