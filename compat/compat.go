// Package compat holds the ntm compatibility inventory (ntm-contract.md)
// and reads its table.
package compat

import (
	_ "embed"
	"strings"
)

//go:embed ntm-contract.md
var inventory string

// Inventory returns the inventory as written.
func Inventory() string { return inventory }

// Row is one form of the inventory.
type Row struct {
	Form        string `json:"form"`
	Callers     string `json:"callers"`
	Disposition string `json:"disposition"` // implemented, deferred, waived
	Note        string `json:"note"`
}

// Rows parses the inventory table: every line that starts with "| `"
// and has six cells.
func Rows() []Row {
	var rows []Row
	for _, line := range strings.Split(inventory, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		if len(cells) != 6 {
			continue
		}
		last := strings.TrimSpace(cells[5])
		word, rest, _ := strings.Cut(last, " ")
		word = strings.TrimSuffix(word, ":")
		rows = append(rows, Row{Form: strings.TrimSpace(cells[0]), Callers: strings.TrimSpace(cells[1]),
			Disposition: word, Note: strings.TrimSpace(rest)})
	}
	return rows
}

// Counts counts the rows per disposition.
func Counts(rows []Row) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Disposition]++
	}
	return out
}
