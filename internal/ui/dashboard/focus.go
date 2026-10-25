// Taken from ntm (github.com/Dicklesworthstone/ntm) internal/tui/dashboard/focus.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: this header; the import path. Boundary edits
// (evidence/dashboard-closure.md): the board's own PanelID set (agents,
// attention, history; panels.go) in panelIDString and
// syncFocusAnimations; buildFocusTargets asks the board's visiblePanels
// in place of ntm's visiblePanelsForHelpVerbosity; without a spring
// manager the focused panel gets the full focus colour; logFocusf reads
// nhm's NHM_DEBUG in place of ntm's NTM_DEBUG (nhm-2p6.28.3);
// focusIntensity and focusBorderColor take a pointer, as every method of
// the board's Model.

package dashboard

import (
	"log"
	"os"

	"github.com/charmbracelet/lipgloss"

	"github.com/arosl/nhm/internal/ui/tui/styles"
)

// FocusTarget describes a focusable dashboard panel.
type FocusTarget struct {
	ID      string
	Panel   PanelID
	Visible func() bool
}

// FocusRing tracks panel focus while layouts change.
type FocusRing struct {
	targets []FocusTarget
	current int
	prevID  string
}

// Current returns the currently focused target, if any.
func (fr *FocusRing) Current() FocusTarget {
	if len(fr.targets) == 0 {
		return FocusTarget{}
	}
	if fr.current < 0 || fr.current >= len(fr.targets) {
		return FocusTarget{}
	}
	return fr.targets[fr.current]
}

// Next moves focus forward, skipping hidden targets.
func (fr *FocusRing) Next() {
	fr.step(1)
}

// Prev moves focus backward, skipping hidden targets.
func (fr *FocusRing) Prev() {
	fr.step(-1)
}

// SetByID focuses a target by ID when it is visible.
func (fr *FocusRing) SetByID(id string) bool {
	for i := range fr.targets {
		if fr.targets[i].ID == id && fr.isVisible(i) {
			fr.current = i
			fr.prevID = id
			return true
		}
	}
	return false
}

// Rebuild refreshes the target set while preserving focus by ID when possible.
func (fr *FocusRing) Rebuild(targets []FocusTarget) {
	currentID := fr.currentID()
	fr.targets = targets
	fr.current = 0

	if fr.prevID != "" && fr.SetByID(fr.prevID) {
		return
	}
	if currentID != "" && fr.SetByID(currentID) {
		return
	}
	if idx, ok := fr.firstVisibleIndex(); ok {
		fr.current = idx
		fr.prevID = fr.targets[idx].ID
		return
	}
	if len(fr.targets) == 0 {
		fr.prevID = ""
		return
	}
	fr.current = 0
	fr.prevID = fr.targets[0].ID
}

func (fr *FocusRing) step(dir int) {
	if len(fr.targets) == 0 {
		return
	}

	start := fr.current
	if start < 0 || start >= len(fr.targets) {
		start = 0
	}
	if !fr.isVisible(start) {
		if idx, ok := fr.firstVisibleIndex(); ok {
			start = idx
		}
	}

	idx := start
	for range fr.targets {
		idx = (idx + dir + len(fr.targets)) % len(fr.targets)
		if fr.isVisible(idx) {
			fr.current = idx
			fr.prevID = fr.targets[idx].ID
			return
		}
	}

	fr.current = start
	if start >= 0 && start < len(fr.targets) {
		fr.prevID = fr.targets[start].ID
	}
}

func (fr *FocusRing) isVisible(index int) bool {
	if index < 0 || index >= len(fr.targets) {
		return false
	}
	if fr.targets[index].Visible == nil {
		return true
	}
	return fr.targets[index].Visible()
}

func (fr *FocusRing) firstVisibleIndex() (int, bool) {
	for i := range fr.targets {
		if fr.isVisible(i) {
			return i, true
		}
	}
	return 0, false
}

func (fr *FocusRing) currentID() string {
	if len(fr.targets) == 0 {
		return fr.prevID
	}
	if fr.current < 0 || fr.current >= len(fr.targets) {
		return fr.prevID
	}
	return fr.targets[fr.current].ID
}

func panelIDString(id PanelID) string {
	switch id {
	case PanelPaneList:
		return "panes"
	case PanelAttention:
		return "attention"
	case PanelHistory:
		return "history"
	default:
		return ""
	}
}

func (m *Model) buildFocusTargets() []FocusTarget {
	visible := m.visiblePanels()
	targets := make([]FocusTarget, 0, len(visible))
	for _, panel := range visible {
		targets = append(targets, FocusTarget{
			ID:    panelIDString(panel),
			Panel: panel,
		})
	}
	return targets
}

func (m *Model) syncFocusRing() {
	m.rebuildFocusRing(true)
}

func (m *Model) refreshFocusRing() {
	m.rebuildFocusRing(false)
}

func (m *Model) rebuildFocusRing(logChanges bool) {
	targets := m.buildFocusTargets()
	previousID := panelIDString(m.focusedPanel)
	removed := previousID != "" && !focusTargetsContain(targets, previousID)

	m.focusRing.prevID = previousID
	m.focusRing.Rebuild(targets)

	current := m.focusRing.Current()
	if current.ID == "" {
		m.focusedPanel = PanelPaneList
		m.syncFocusAnimations()
		return
	}

	if logChanges {
		logFocusf("focus ring: rebuilt with %d targets (%d visible)", len(targets), countVisibleTargets(targets))
		if removed && previousID != current.ID {
			logFocusf("focus ring: %s removed, falling back to %s", previousID, current.ID)
		}
	}
	m.focusedPanel = current.Panel
	m.syncFocusAnimations()
}

func (m *Model) setFocusedPanel(panel PanelID) bool {
	targetID := panelIDString(panel)
	if targetID == "" {
		return false
	}
	m.refreshFocusRing()
	if !m.focusRing.SetByID(targetID) {
		return false
	}
	previous := m.focusedPanel
	m.focusedPanel = panel
	if previous != panel {
		logFocusf("focus: %s -> %s", panelIDString(previous), targetID)
	}
	m.syncFocusAnimations()
	return true
}

func (m *Model) syncFocusAnimations() {
	if m == nil || m.dashboardSprings == nil {
		return
	}

	for _, panel := range []PanelID{
		PanelPaneList,
		PanelAttention,
		PanelHistory,
	} {
		id := panelIDString(panel)
		if id == "" {
			continue
		}

		target := 0.0
		if m.focusedPanel == panel {
			target = 1.0
		}

		key := "focus:" + id
		if !m.dashboardSprings.Has(key) {
			m.dashboardSprings.SetImmediate(key, target)
			continue
		}
		m.dashboardSprings.SetWithParams(key, target, 10.0, 0.7)
	}
}

func (m *Model) focusIntensity(panel PanelID) float64 {
	if m.dashboardSprings == nil {
		// nhm: no springs, no animation; the focused panel is focused.
		if m.focusedPanel == panel {
			return 1
		}
		return 0
	}
	id := panelIDString(panel)
	if id == "" {
		return 0
	}
	value := m.dashboardSprings.Get("focus:" + id)
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (m *Model) focusBorderColor(base lipgloss.Color, panels ...PanelID) lipgloss.Color {
	intensity := 0.0
	for _, panel := range panels {
		if current := m.focusIntensity(panel); current > intensity {
			intensity = current
		}
	}
	if intensity <= 0 {
		return base
	}

	from := styles.ParseHex(string(base))
	to := styles.ParseHex(string(m.theme.Primary))
	return styles.Lerp(from, to, intensity).ToLipgloss()
}

func countVisibleTargets(targets []FocusTarget) int {
	count := 0
	for i := range targets {
		if targets[i].Visible == nil || targets[i].Visible() {
			count++
		}
	}
	return count
}

func focusTargetsContain(targets []FocusTarget, id string) bool {
	for _, target := range targets {
		if target.ID == id {
			return true
		}
	}
	return false
}

func logFocusf(format string, args ...any) {
	if os.Getenv("NHM_DEBUG") == "1" {
		log.Printf(format, args...)
	}
}
