package session

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
)

// ntm's pane forms in --pane, --panes and positional panes, on herdr (the
// operator's ruling, 2026-10-24): a pane index K is pane M=K of the
// session's agent tab, and with several agent tabs the active tab's; W.P
// is the slot W.P; %N is a tmux pane id, which herdr has none of. nhm's
// own forms stay: a herdr pane id, a name or a kind.

// PaneForm is the form of a parsed pane selector.
type PaneForm int

const (
	// PaneSlot is a slot N.M, given as N.M (ntm's W.P) or as an index.
	PaneSlot PaneForm = iota
	// PaneHerdrID is a herdr pane id.
	PaneHerdrID
	// PaneWord is a name or a kind, which the caller resolves.
	PaneWord
)

// PaneTarget is a parsed pane selector.
type PaneTarget struct {
	Form   PaneForm
	Slot   string // PaneSlot
	PaneID string // PaneHerdrID
	Word   string // PaneWord
}

// AgentTab is a tab of the session that holds bound agents: N, the tab
// part of their slots, and herdr's tab id.
type AgentTab struct {
	N     int
	TabID string
}

var (
	paneIndexPattern = regexp.MustCompile(`^[0-9]+$`)
	paneSlotPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	tmuxPaneIDRegexp = regexp.MustCompile(`^%[0-9]+$`)
	herdrPaneIDRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*:p[0-9A-Za-z]+$`)
)

// ParsePane parses one pane selector of a session. tabs are the session's
// agent tabs (AgentTabs) and activeTabID is herdr's active tab of the
// workspace; only an index reads them.
func ParsePane(text string, tabs []AgentTab, activeTabID string) (PaneTarget, error) {
	switch {
	case tmuxPaneIDRegexp.MatchString(text):
		return PaneTarget{}, fmt.Errorf("pane %s: herdr has no tmux pane ids; give a pane index, the slot N.M (nhm status shows it) or a herdr pane id", text)
	case paneIndexPattern.MatchString(text):
		n, err := agentTabN(text, tabs, activeTabID)
		if err != nil {
			return PaneTarget{}, err
		}
		k, _ := strconv.Atoi(text)
		return PaneTarget{Form: PaneSlot, Slot: bindings.Slot{Tab: n, Pane: k}.String()}, nil
	case paneSlotPattern.MatchString(text):
		s, err := bindings.ParseSlot(text)
		if err != nil {
			return PaneTarget{}, fmt.Errorf("pane %s: %w", text, err)
		}
		return PaneTarget{Form: PaneSlot, Slot: s.String()}, nil
	case herdrPaneIDRegex.MatchString(text):
		return PaneTarget{Form: PaneHerdrID, PaneID: text}, nil
	}
	return PaneTarget{Form: PaneWord, Word: text}, nil
}

// agentTabN is the tab an index counts in: the session's one agent tab,
// or with several, the active tab when it holds agents.
func agentTabN(index string, tabs []AgentTab, activeTabID string) (int, error) {
	ns := map[int]bool{}
	for _, t := range tabs {
		ns[t.N] = true
	}
	switch len(ns) {
	case 0:
		return 0, fmt.Errorf("pane %s: the session has no bound agents", index)
	case 1:
		return tabs[0].N, nil
	}
	for _, t := range tabs {
		if activeTabID != "" && t.TabID == activeTabID {
			return t.N, nil
		}
	}
	list := make([]int, 0, len(ns))
	for n := range ns {
		list = append(list, n)
	}
	sort.Ints(list)
	words := make([]string, len(list))
	for i, n := range list {
		words[i] = strconv.Itoa(n)
	}
	return 0, fmt.Errorf("pane %s: the session's agents are in tabs %s, and the active tab holds none; give the slot N.M (nhm status shows it)",
		index, strings.Join(words, ", "))
}

// AgentTabs are the agent tabs of the bindings given (a session's active
// bindings), by slot: each tab once, in the order of N.
func AgentTabs(bs []bindings.Binding) []AgentTab {
	seen := map[int]bool{}
	var out []AgentTab
	for _, b := range bs {
		s, err := bindings.ParseSlot(b.Slot)
		if err != nil || seen[s.Tab] {
			continue
		}
		seen[s.Tab] = true
		out = append(out, AgentTab{N: s.Tab, TabID: b.TabID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

// SlotIndexes are the ints that ntm's JSON writes for a pane: the window
// index is the slot's N, and the pane index (ntm's pane and pane_index)
// is its M. A pane without a slot is 0, 0.
func SlotIndexes(slot string) (window, pane int) {
	s, err := bindings.ParseSlot(slot)
	if err != nil {
		return 0, 0
	}
	return s.Tab, s.Pane
}
