package cli

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/picker"
)

// TestPickerOnTheSnapshot: the picker reads the in-process snapshot of
// the captured workspace and picks the bound agent at its generation;
// the unbound codex cannot be interrupted.
func TestPickerOnTheSnapshot(t *testing.T) {
	s := swarmInstance(t)
	src, closeSrc, err := pickerSource(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSrc()
	interrupt, _ := actions.ByID("agent.interrupt")
	m := picker.ForAction(interrupt, ui.Context{WorkspaceID: "w1", WorkspaceLabel: "swarm"}, src)
	m, _ = m.Update(m.Init()())
	v := m.View()
	if !strings.Contains(v, "IcyKnoll") || !strings.Contains(v, "no binding: nhm adopt binds it") {
		t.Fatalf("view:\n%s", v)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || m.Done() {
		t.Fatal("the unbound codex was picked to interrupt")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p, ok := cmd().(picker.Picked)
	if !ok || len(p.Targets) != 1 || !p.Targets[0].Bound() || p.Targets[0].Generation != 1 || p.Targets[0].PaneID != "w1:p1" {
		t.Fatalf("picked %#v", cmd())
	}
}

// TestPickerSourceRefusesAFailedSnapshot: herdr failing is an error, not
// an empty list.
func TestPickerSourceRefusesAFailedSnapshot(t *testing.T) {
	s := swarmInstance(t)
	src, closeSrc, err := pickerSource(s.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSrc()
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "internal", Message: "snapshot failed"}
	})
	if m, err := src(context.Background()); err == nil || m != nil || !strings.Contains(err.Error(), "herdr did not answer") {
		t.Fatalf("failed snapshot: %v %v", m, err)
	}
}
