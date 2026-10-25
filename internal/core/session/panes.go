// Ported in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/copy.go (AgentFilter, the target order of runCopy and
// paneMatchesSelector) at commit 9d5ed977; see LICENSE in this directory
// and PROVENANCE.md. Local changes: a session's panes are a herdr
// workspace's (Panes, nhm's own); a pane's type is its herdr kind's ntm
// type, "user" for a pane without an agent; the pane after "session:"
// takes ParsePane's forms, an agent's name and ntm's pane id suffix, in
// place of tmux's index and %N ids.

package session

import (
	"sort"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/kinds"
)

// UserType is ntm's agent type of a pane without an agent (tmux.AgentUser).
const UserType = "user"

// Pane is one pane of a session as ntm's pane verbs list it (tmux.GetPanes,
// on herdr): every pane of the workspace, agents and shells, in ntm's
// window and pane order, as spawn's output lists them: the window is the
// N of the slots in the pane's tab (0 for a tab without one), the index
// the slot's M (0 for a pane without a slot), then herdr's order.
type Pane struct {
	PaneID string
	TabID  string
	// Window is ntm's window index: the N of the slots in the pane's tab.
	Window int
	// Slot is the bound agent's slot N.M; "" for a pane without one.
	Slot string
	// Index is ntm's pane index: the slot's M, 0 without a slot.
	Index int
	// Title is herdr's label (nhm's "<MailName> <model>"), else the title
	// that the program in the pane set.
	Title string
	// Kind is the bound agent's kind, else herdr's agent kind; "" for a
	// pane without an agent.
	Kind string
	// Type is ntm's agent type: the kind's ntm type, UserType for a pane
	// without an agent.
	Type string
	// Active is the focused pane of the workspace's active tab: tmux's
	// active pane.
	Active bool
	Cwd    string
	// State is herdr's agent state of the pane (herdr says unknown for a
	// pane without an agent).
	State herdr.AgentStatus
	// Binding is the active binding of the pane, if any.
	Binding *bindings.Binding
}

// Ref is the pane's place in ntm's texts, where ntm writes its pane
// index: the slot's M, or the herdr pane id for a pane without a slot.
func (p Pane) Ref() string {
	if p.Slot == "" {
		return p.PaneID
	}
	return strconv.Itoa(p.Index)
}

// Panes lists the panes of a workspace in snap. bound are the active
// bindings; a pane they hold gets its slot and binding.
func Panes(snap herdr.SessionSnapshot, workspaceID string, bound []bindings.Binding) []Pane {
	activeTab := ""
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == workspaceID {
			activeTab = w.ActiveTabID
		}
	}
	active := ""
	for _, l := range snap.Layouts {
		if l.WorkspaceID == workspaceID && l.TabID == activeTab {
			active = l.FocusedPaneID
		}
	}
	held := map[string]*bindings.Binding{}
	window := map[string]int{}
	for i, b := range bound {
		if b.PaneID == "" || b.Status != bindings.Active {
			continue
		}
		held[b.PaneID] = &bound[i]
		if s, err := bindings.ParseSlot(b.Slot); err == nil {
			window[b.TabID] = s.Tab
		}
	}
	var out []Pane
	for _, hp := range snap.Panes {
		if hp.WorkspaceID != workspaceID {
			continue
		}
		p := Pane{PaneID: hp.PaneID, TabID: hp.TabID, Window: window[hp.TabID], Kind: hp.Agent, Type: UserType,
			Title: firstNonEmpty(hp.Label, hp.Title, hp.TerminalTitleStripped), Cwd: firstNonEmpty(hp.ForegroundCwd, hp.Cwd),
			State: hp.AgentStatus}
		if active != "" {
			p.Active = hp.PaneID == active
		} else {
			p.Active = hp.Focused && hp.TabID == activeTab
		}
		if b := held[hp.PaneID]; b != nil {
			p.Binding = b
			if b.Kind != "" {
				p.Kind = b.Kind
			}
			if s, err := bindings.ParseSlot(b.Slot); err == nil {
				p.Slot, p.Index = s.String(), s.Pane
			}
		}
		if p.Kind != "" {
			p.Type = kinds.Lookup(p.Kind).NTMType
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Window != out[j].Window {
			return out[i].Window < out[j].Window
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// AgentEntry is one Agent Mail agent in a live pane of a session: ntm's
// agent registry entry (agentMappingEntry), from nhm's bindings.
type AgentEntry struct {
	Name      string
	PaneIndex int
	PaneID    string
	PaneTitle string
	Pane      Pane
}

// Agents are the bound agents in panes, by name, each name once (the
// first pane that holds it), as ntm's loadMappingForSession lists its
// registry's live entries.
func Agents(panes []Pane) []AgentEntry {
	seen := map[string]bool{}
	var out []AgentEntry
	for _, p := range panes {
		if p.Binding == nil || p.Binding.MailName == "" || seen[p.Binding.MailName] {
			continue
		}
		seen[p.Binding.MailName] = true
		out = append(out, AgentEntry{Name: p.Binding.MailName, PaneIndex: p.Index, PaneID: p.PaneID, PaneTitle: p.Title, Pane: p})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// TypeFilter is ntm's AgentFilter: --all and the type flags of copy,
// save, grep and errors.
type TypeFilter struct {
	All         bool
	Claude      bool
	Codex       bool
	Gemini      bool
	Antigravity bool
}

// IsEmpty reports whether no flag is set.
func (f TypeFilter) IsEmpty() bool {
	return !f.All && !f.Claude && !f.Codex && !f.Gemini && !f.Antigravity
}

// Matches reports whether a pane of ntm type agentType passes: every
// pane with --all, else the four types ntm's flags name.
func (f TypeFilter) Matches(agentType string) bool {
	if f.All {
		return true
	}
	switch agentType {
	case "claude":
		return f.Claude
	case "codex":
		return f.Codex
	case "gemini":
		return f.Gemini
	case "antigravity":
		return f.Antigravity
	default:
		return false
	}
}

// MatchPane reports whether p is the pane that t names: its slot, its
// herdr pane id, or for a word, the name of its agent (the Agent Mail
// name or the herdr alias, in any case) or, as ntm matches a pane id, the
// end of its pane id ("pY" names "wQ:pY").
func MatchPane(p Pane, t PaneTarget) bool {
	switch t.Form {
	case PaneSlot:
		return p.Slot != "" && p.Slot == t.Slot
	case PaneHerdrID:
		return p.PaneID == t.PaneID
	}
	if t.Word == "" {
		return false
	}
	if b := p.Binding; b != nil && (strings.EqualFold(b.MailName, t.Word) || strings.EqualFold(b.HerdrAlias, t.Word)) {
		return true
	}
	return strings.HasSuffix(p.PaneID, t.Word)
}

// CopyTargets is copy's choice of panes (ntm's runCopy): the first pane
// that spec names, else the panes of the filter's types, else the active
// pane, else the first pane.
func CopyTargets(panes []Pane, spec *PaneTarget, f TypeFilter) []Pane {
	switch {
	case spec != nil:
		for _, p := range panes {
			if MatchPane(p, *spec) {
				return []Pane{p}
			}
		}
		return nil
	case f.IsEmpty():
		for _, p := range panes {
			if p.Active {
				return []Pane{p}
			}
		}
		if len(panes) > 0 {
			return panes[:1]
		}
		return nil
	}
	var out []Pane
	for _, p := range panes {
		if f.Matches(p.Type) {
			out = append(out, p)
		}
	}
	return out
}
