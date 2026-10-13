package bindings

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
)

// maxAliasSuffix is the last suffix an alias takes: base-2 … base-9.
const maxAliasSuffix = 9

// AliasFits reports whether a herdr name is one nhm gives mail name: its
// Alias, or that with a suffix -2 … -9, as FreeAlias gives one when a
// same-name agent of another project holds the bare name (nhm-4xt).
func AliasFits(name, mailName string) bool {
	base, err := Alias(mailName)
	if err != nil {
		return false
	}
	if name == base {
		return true
	}
	n, ok := strings.CutPrefix(name, base+"-")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(n)
	return err == nil && i >= 2 && i <= maxAliasSuffix && n == strconv.Itoa(i)
}

// FreeAlias is the herdr name for an agent of mail name, among the live
// agents: its Alias, or that with a suffix -2 … -9 when an agent in
// another pane holds it, such as a same-name agent of another project.
// The agents in own (the agent's pane, a handoff's source pane) hold
// nothing against it. prefer, when it fits mailName and is free, wins: a
// binding keeps the name herdr already shows, and a handoff its source's.
// spawn, adopt and the hooks' bind give names with it.
func FreeAlias(mailName string, live []herdr.AgentInfo, prefer string, own ...string) (string, error) {
	base, err := Alias(mailName)
	if err != nil {
		return "", err
	}
	held := map[string]bool{}
	for _, a := range live {
		if a.Name != "" && !slices.Contains(own, a.PaneID) {
			held[a.Name] = true
		}
	}
	if prefer != "" && AliasFits(prefer, mailName) && !held[prefer] {
		return prefer, nil
	}
	if !held[base] {
		return base, nil
	}
	for i := 2; i <= maxAliasSuffix; i++ {
		if c := base + "-" + strconv.Itoa(i); len(c) <= 32 && !held[c] {
			return c, nil
		}
	}
	return "", fmt.Errorf("bindings: the herdr name %s and its suffixes are taken", base)
}
