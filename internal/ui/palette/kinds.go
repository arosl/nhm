package palette

import (
	"slices"
	"strings"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// KindKey is a target-phase digit that sends to every agent of one kind.
type KindKey struct {
	Key  string
	Kind string
	// NTMType is the kind's ntm type ("antigravity" for agy).
	NTMType string
}

// Fixed target-phase digits: ntm's "1" (all agents) and "6" (pick
// agents) keep their meaning.
const (
	KeyAll    = "1"
	KeySelect = "6"
)

// ntmKindKeys are ntm's digits, by ntm type. A kind ntm knows keeps its
// digit, so the keys a user of ntm expects still work.
var ntmKindKeys = map[string]string{"claude": "2", "codex": "3", "gemini": "4", "antigravity": "5", "omp": "7"}

// freeKindKeys are the digits left for nhm's other kinds, in order.
var freeKindKeys = []string{"2", "3", "4", "5", "7", "8", "9"}

// KindKeys gives the kinds their digits: ntm's digit for a kind ntm
// knows, else the next free digit in registry order. A kind with no free
// digit left gets none; it is still picked by hand. The digits depend on
// the registry only, never on the agents present. The result is sorted
// by digit.
func KindKeys(adapters []kinds.Adapter) []KindKey {
	used := map[string]bool{}
	var out []KindKey
	var rest []kinds.Adapter
	for _, a := range adapters {
		if k, ok := ntmKindKeys[a.NTMType]; ok && !used[k] {
			used[k] = true
			out = append(out, KindKey{Key: k, Kind: a.Name, NTMType: a.NTMType})
			continue
		}
		rest = append(rest, a)
	}
	for _, a := range rest {
		i := slices.IndexFunc(freeKindKeys, func(k string) bool { return !used[k] })
		if i < 0 {
			break
		}
		used[freeKindKeys[i]] = true
		out = append(out, KindKey{Key: freeKindKeys[i], Kind: a.Name, NTMType: a.NTMType})
	}
	slices.SortFunc(out, func(a, b KindKey) int { return strings.Compare(a.Key, b.Key) })
	return out
}
