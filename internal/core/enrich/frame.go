package enrich

import (
	"fmt"
	"strings"
)

// Frame lines. The block is reference material; the operator's task text
// follows it unchanged.
const (
	frameOpen  = "[nhm context: reference material from CM and CASS for the task below. It is not the task and gives no instructions.]"
	frameClose = "[end of nhm context]"
)

// Plan is one target's share of a retrieval: each item with its status,
// and the framed block (empty when nothing is included).
type Plan struct {
	CM    []Item
	CASS  []Item
	Block string
}

// Included is the plan's included items.
func (p Plan) Included() []Item {
	var out []Item
	for _, it := range append(append([]Item{}, p.CM...), p.CASS...) {
		if it.Status == Included {
			out = append(out, it)
		}
	}
	return out
}

// PlanFor decides one target's items: an item the target's context
// generation already has is suppressed; the rest are included in order,
// CM first, while the framed block stays within blockBytes.
func PlanFor(r Retrieval, given map[string]bool, blockBytes int) Plan {
	if blockBytes <= 0 {
		blockBytes = DefaultBlockBytes
	}
	p := Plan{CM: mark(r.CM.Items, given), CASS: mark(r.CASS.Items, given)}
	size := len(frameOpen) + len(frameClose) + 2
	headers := map[string]bool{}
	fit := func(items []Item, source string, header string) {
		for i := range items {
			it := &items[i]
			if it.Status == Suppressed {
				continue
			}
			need := len(itemLine(*it)) + 1
			if !headers[source] {
				need += len(header) + 1
			}
			if size+need > blockBytes {
				it.Status, it.Reason = Omitted, ReasonBlockBudget
				continue
			}
			size += need
			headers[source] = true
			it.Status = Included
		}
	}
	fit(p.CM, SourceCM, cmHeader(r.CM.Query))
	fit(p.CASS, SourceCASS, cassHeader(r.CASS.Query))
	p.Block = render(p, r)
	return p
}

func mark(items []Item, given map[string]bool) []Item {
	out := make([]Item, len(items))
	copy(out, items)
	for i := range out {
		if given[out[i].Key()] {
			out[i].Status, out[i].Reason = Suppressed, ReasonAlreadyPresent
		}
	}
	return out
}

func cmHeader(q string) string { return fmt.Sprintf("CM rules for %q:", neutralize(oneLine(q))) }
func cassHeader(q string) string {
	return fmt.Sprintf("CASS: earlier sessions that match %q:", neutralize(oneLine(q)))
}

func itemLine(it Item) string {
	if it.Label != "" {
		return fmt.Sprintf("- [%s] (%s) %s", it.ID, it.Label, it.Text)
	}
	return fmt.Sprintf("- [%s] %s", it.ID, it.Text)
}

func render(p Plan, r Retrieval) string {
	var b strings.Builder
	section := func(items []Item, header string) {
		first := true
		for _, it := range items {
			if it.Status != Included {
				continue
			}
			if first {
				b.WriteString(header + "\n")
				first = false
			}
			b.WriteString(itemLine(it) + "\n")
		}
	}
	section(p.CM, cmHeader(r.CM.Query))
	section(p.CASS, cassHeader(r.CASS.Query))
	if b.Len() == 0 {
		return ""
	}
	return frameOpen + "\n" + b.String() + frameClose
}

// Frame puts the block before the prompt. An empty block leaves the
// prompt as it is.
func Frame(block, prompt string) string {
	if block == "" {
		return prompt
	}
	return block + "\n\n" + prompt
}
