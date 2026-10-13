// Taken from ntm (github.com/Dicklesworthstone/ntm)
// internal/tui/dashboard/panels/spawn_wizard_test.go at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md.
// Local changes: package forms; NewSpawnWizard takes a runner, a context
// and nhm's kinds; the counts are per kind (countStrs).

package forms

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/spawn"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/ui/actions"
)

// testKinds are three kinds: two that take a model, one that does not.
var testKinds = []kinds.Adapter{
	{Name: "claude", NTMType: "cc", ModelFlag: "--model"},
	{Name: "codex", NTMType: "cod", ModelFlag: "-m"},
	{Name: "agy", NTMType: "agy"},
}

// spawnCore answers the spawn seam: a dry run's admission, then a start.
type spawnCore struct {
	asked  []actions.SpawnRequest
	opIDs  []string
	review spawn.Result
	err    error
}

func (s *spawnCore) core() actions.Core {
	return actions.Core{Spawn: func(_ context.Context, r actions.SpawnRequest, op string) (spawn.Result, error) {
		s.asked, s.opIDs = append(s.asked, r), append(s.opIDs, op)
		if r.DryRun {
			return s.review, s.err
		}
		return spawn.Result{OperationID: op, Admission: spawn.Admission{Decision: spawn.Admit},
			Targets: []spawn.Target{{Kind: "claude", MailName: "IcyKnoll", Status: "ready", Ready: true}}}, nil
	}}
}

func newTestWizard(t *testing.T, id string, sc *spawnCore) *SpawnWizard {
	t.Helper()
	return NewSpawnWizard(newRunner(t, id, sc.core()), ctxW, testKinds, 120, 30)
}

func TestSpawnWizardBackspaceDoesNotLeaveCountsStep(t *testing.T) {
	t.Parallel()

	sw := newTestWizard(t, "spawn.add", &spawnCore{})
	sw.step = SpawnStepCounts
	sw.countStrs = []string{"12", "1", "0"}
	sw.initCountsForm()

	updated, _ := sw.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	sw = updated.(*SpawnWizard)

	if sw.step != SpawnStepCounts {
		t.Fatalf("expected backspace to stay on counts step, got %v", sw.step)
	}
}

// Going back to the counts keeps an edit of a preset's count.
func TestSpawnWizardBackKeepsEditedCounts(t *testing.T) {
	t.Parallel()

	sw := newTestWizard(t, "spawn.add", &spawnCore{})
	sw.configMethod = "quick"
	sw.step = SpawnStepCounts
	sw.initCountsForm()
	if sw.countStrs[0] != "2" {
		t.Fatalf("quick did not fill the counts: %v", sw.countStrs)
	}
	sw.countStrs[0] = "5"
	sw.step = SpawnStepOptions
	sw.initOptionsForm()
	updated, _ := sw.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	sw = updated.(*SpawnWizard)
	if sw.step != SpawnStepCounts || sw.countStrs[0] != "5" {
		t.Fatalf("back to the counts: step %v, counts %v", sw.step, sw.countStrs)
	}
}

func TestSpawnWizardShiftTabMovesBackOneStep(t *testing.T) {
	t.Parallel()

	sw := newTestWizard(t, "spawn.add", &spawnCore{})
	sw.step = SpawnStepCounts
	sw.initCountsForm()

	updated, cmd := sw.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	sw = updated.(*SpawnWizard)

	if sw.step != SpawnStepMethod {
		t.Fatalf("expected shift+tab to return to method step, got %v", sw.step)
	}
	if cmd == nil {
		t.Fatal("expected shift+tab back navigation to reinitialize the method form")
	}
}
