// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/modes.go (modesListOutput, modesListRow, modesExplainOutput,
// runModesList, renderModesList, renderModesExplain),
// internal/cli/ensemble_presets.go (the preset row and detail types,
// renderPresetsSummary, renderPresetsVerbose, renderPresetsTable,
// renderPresetsTableVerbose, renderYAML) and internal/output/text.go
// (Table); see LICENSE in this directory and PROVENANCE.md. Local
// changes: the functions build from nhm's Catalog and take the clock; the
// table measures width in runes, where ntm's lipgloss.Width also counts
// wide characters (the catalog is ASCII); modes keep the catalog's fixed
// order, where ntm's map-built catalog lists them in an unspecified one;
// every built-in preset's source is "embedded", as ntm's loader sets it,
// and none has an agent distribution.

package ensemble

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ModesListOutput is `modes list --format json`.
type ModesListOutput struct {
	GeneratedAt time.Time      `json:"generated_at" yaml:"generated_at"`
	Modes       []ModesListRow `json:"modes" yaml:"modes"`
	Count       int            `json:"count" yaml:"count"`
	Filter      string         `json:"filter,omitempty" yaml:"filter,omitempty"`
}

// ModesListRow is one mode of ModesListOutput.
type ModesListRow struct {
	ID        string `json:"id" yaml:"id"`
	Code      string `json:"code" yaml:"code"`
	Name      string `json:"name" yaml:"name"`
	Category  string `json:"category" yaml:"category"`
	Tier      string `json:"tier" yaml:"tier"`
	ShortDesc string `json:"short_desc" yaml:"short_desc"`
}

// ModesExplainOutput is `modes explain --format json`.
type ModesExplainOutput struct {
	GeneratedAt time.Time `json:"generated_at" yaml:"generated_at"`
	Card        *ModeCard `json:"card" yaml:"card"`
}

// ModesList filters the catalog as ntm's runModesList does: the core tier
// unless a tier or all is given, then the category, if any.
func ModesList(c *Catalog, tier, category string, all bool, now time.Time) ModesListOutput {
	if !all && tier == "" {
		tier = "core"
	}
	rows := make([]ModesListRow, 0)
	for _, m := range c.modes {
		if tier != "" && !strings.EqualFold(string(m.Tier), tier) {
			continue
		}
		if category != "" && !strings.EqualFold(string(m.Category), category) {
			continue
		}
		rows = append(rows, ModesListRow{ID: m.ID, Code: m.Code, Name: m.Name, Category: string(m.Category),
			Tier: string(m.Tier), ShortDesc: m.ShortDesc})
	}
	filterDesc := ""
	if category != "" {
		filterDesc = "category=" + category
	}
	if tier != "" && tier != "core" {
		if filterDesc != "" {
			filterDesc += ", "
		}
		filterDesc += "tier=" + tier
	}
	return ModesListOutput{GeneratedAt: now.UTC(), Modes: rows, Count: len(rows), Filter: filterDesc}
}

// RenderModesList prints ModesListOutput as json, yaml or ntm's table.
func RenderModesList(w io.Writer, payload ModesListOutput, format string) error {
	switch format {
	case "json":
		return writeJSON(w, payload)
	case "yaml":
		return RenderYAML(w, payload)
	default:
		if len(payload.Modes) == 0 {
			fmt.Fprintf(w, "No modes found matching criteria\n")
			return nil
		}

		table := NewTable(w, "CODE", "ID", "NAME", "CATEGORY", "TIER")
		for _, row := range payload.Modes {
			table.AddRow(row.Code, row.ID, row.Name, row.Category, row.Tier)
		}
		table.Render()
		fmt.Fprintf(w, "\n%d modes total\n", payload.Count)
		return nil
	}
}

// RenderModesExplain prints ModesExplainOutput as json, yaml or ntm's card.
func RenderModesExplain(w io.Writer, payload ModesExplainOutput, format string) error {
	switch format {
	case "json":
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	case "yaml":
		data, err := yaml.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		fmt.Fprint(w, FormatCard(payload.Card))
		return nil
	}
}

// PresetRow is one preset of `ensemble presets`.
type PresetRow struct {
	Name          string   `json:"name" yaml:"name"`
	DisplayName   string   `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Description   string   `json:"description" yaml:"description"`
	ModeCodes     []string `json:"mode_codes" yaml:"mode_codes"`
	ModeCount     int      `json:"mode_count" yaml:"mode_count"`
	Strategy      string   `json:"synthesis_strategy" yaml:"synthesis_strategy"`
	MaxTokens     int      `json:"max_total_tokens" yaml:"max_total_tokens"`
	AllowAdvanced bool     `json:"allow_advanced,omitempty" yaml:"allow_advanced,omitempty"`
	Tags          []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Source        string   `json:"source" yaml:"source"`
}

// PresetDetail is one preset of `ensemble presets --verbose`.
type PresetDetail struct {
	Name              string                   `json:"name" yaml:"name"`
	DisplayName       string                   `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Description       string                   `json:"description" yaml:"description"`
	Modes             []PresetModeDetail       `json:"modes" yaml:"modes"`
	ModeCount         int                      `json:"mode_count" yaml:"mode_count"`
	Synthesis         PresetSynthesisDetail    `json:"synthesis" yaml:"synthesis"`
	Budget            PresetBudgetDetail       `json:"budget" yaml:"budget"`
	AllowAdvanced     bool                     `json:"allow_advanced,omitempty" yaml:"allow_advanced,omitempty"`
	AgentDistribution *AgentDistributionDetail `json:"agent_distribution,omitempty" yaml:"agent_distribution,omitempty"`
	Tags              []string                 `json:"tags,omitempty" yaml:"tags,omitempty"`
	Source            string                   `json:"source" yaml:"source"`
}

// PresetModeDetail is one mode of a PresetDetail.
type PresetModeDetail struct {
	ID       string `json:"id" yaml:"id"`
	Code     string `json:"code,omitempty" yaml:"code,omitempty"`
	Name     string `json:"name,omitempty" yaml:"name,omitempty"`
	Category string `json:"category,omitempty" yaml:"category,omitempty"`
	Tier     string `json:"tier,omitempty" yaml:"tier,omitempty"`
}

// PresetSynthesisDetail is a PresetDetail's synthesis.
type PresetSynthesisDetail struct {
	Strategy      string  `json:"strategy" yaml:"strategy"`
	MinConfidence float64 `json:"min_confidence" yaml:"min_confidence"`
	MaxFindings   int     `json:"max_findings" yaml:"max_findings"`
}

// PresetBudgetDetail is a PresetDetail's budget.
type PresetBudgetDetail struct {
	MaxTokensPerMode int    `json:"max_tokens_per_mode" yaml:"max_tokens_per_mode"`
	MaxTotalTokens   int    `json:"max_total_tokens" yaml:"max_total_tokens"`
	TimeoutPerMode   string `json:"timeout_per_mode" yaml:"timeout_per_mode"`
	TotalTimeout     string `json:"total_timeout" yaml:"total_timeout"`
}

// AgentDistributionDetail is a PresetDetail's agent distribution; no
// built-in preset has one.
type AgentDistributionDetail struct {
	Strategy           string `json:"strategy" yaml:"strategy"`
	MaxAgents          int    `json:"max_agents,omitempty" yaml:"max_agents,omitempty"`
	PreferredAgentType string `json:"preferred_agent_type,omitempty" yaml:"preferred_agent_type,omitempty"`
}

// PresetsOutput is `ensemble presets --format json`.
type PresetsOutput struct {
	GeneratedAt time.Time      `json:"generated_at" yaml:"generated_at"`
	Count       int            `json:"count" yaml:"count"`
	Presets     []PresetRow    `json:"presets,omitempty" yaml:"presets,omitempty"`
	Details     []PresetDetail `json:"details,omitempty" yaml:"details,omitempty"`
}

// PresetSource is where the built-in presets come from, as ntm's loader
// names it.
const PresetSource = "embedded"

// PresetsList builds ntm's presets output: in ntm's order (the embedded
// order), with an exact tag filter as ntm's ListByTag, as rows or, with
// verbose, as details.
func PresetsList(c *Catalog, tag string, verbose bool, now time.Time) PresetsOutput {
	out := PresetsOutput{GeneratedAt: now.UTC()}
	for _, p := range c.presets {
		if tag != "" && !containsString(p.Tags, tag) {
			continue
		}
		if verbose {
			out.Details = append(out.Details, presetDetail(c, p))
		} else {
			out.Presets = append(out.Presets, presetRow(c, p))
		}
	}
	out.Count = len(out.Presets) + len(out.Details)
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func (c *Catalog) modeByID(id string) *ReasoningMode {
	if i, ok := c.byID[id]; ok {
		return &c.modes[i]
	}
	return nil
}

func presetRow(c *Catalog, p EnsemblePreset) PresetRow {
	modeCodes := make([]string, 0, len(p.Modes))
	for _, mref := range p.Modes {
		code := mref.ID
		if mode := c.modeByID(mref.ID); mode != nil {
			code = mode.Code
		}
		modeCodes = append(modeCodes, code)
	}
	return PresetRow{
		Name:          p.Name,
		DisplayName:   p.DisplayName,
		Description:   p.Description,
		ModeCodes:     modeCodes,
		ModeCount:     len(p.Modes),
		Strategy:      string(p.Synthesis.Strategy),
		MaxTokens:     p.Budget.MaxTotalTokens,
		AllowAdvanced: p.AllowAdvanced,
		Tags:          p.Tags,
		Source:        PresetSource,
	}
}

func presetDetail(c *Catalog, p EnsemblePreset) PresetDetail {
	modes := make([]PresetModeDetail, 0, len(p.Modes))
	for _, mref := range p.Modes {
		md := PresetModeDetail{ID: mref.ID}
		if mode := c.modeByID(mref.ID); mode != nil {
			md.Code = mode.Code
			md.Name = mode.Name
			md.Category = string(mode.Category)
			md.Tier = string(mode.Tier)
		}
		modes = append(modes, md)
	}
	return PresetDetail{
		Name:        p.Name,
		DisplayName: p.DisplayName,
		Description: p.Description,
		Modes:       modes,
		ModeCount:   len(p.Modes),
		Synthesis: PresetSynthesisDetail{
			Strategy:      string(p.Synthesis.Strategy),
			MinConfidence: float64(p.Synthesis.MinConfidence),
			MaxFindings:   p.Synthesis.MaxFindings,
		},
		Budget: PresetBudgetDetail{
			MaxTokensPerMode: p.Budget.MaxTokensPerMode,
			MaxTotalTokens:   p.Budget.MaxTotalTokens,
			TimeoutPerMode:   p.Budget.TimeoutPerMode.String(),
			TotalTimeout:     p.Budget.TotalTimeout.String(),
		},
		AllowAdvanced: p.AllowAdvanced,
		Tags:          p.Tags,
		Source:        PresetSource,
	}
}

// RenderPresets prints PresetsOutput as json, yaml or ntm's table (the
// verbose one when it holds details).
func RenderPresets(w io.Writer, result PresetsOutput, verbose bool, format string) error {
	switch format {
	case "json":
		return writeJSON(w, result)
	case "yaml", "yml":
		return RenderYAML(w, result)
	}
	if verbose {
		return renderPresetsTableVerbose(w, result.Details)
	}
	return renderPresetsTable(w, result.Presets)
}

// renderPresetsTable renders presets as a human-readable table.
func renderPresetsTable(w io.Writer, rows []PresetRow) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No ensemble presets found.")
		return nil
	}

	// Print header
	fmt.Fprintf(w, "%-20s %-25s %-6s %-12s %-8s %-8s %s\n",
		"NAME", "DISPLAY", "MODES", "STRATEGY", "TOKENS", "SOURCE", "TAGS")
	fmt.Fprintln(w, strings.Repeat("-", 100))

	for _, r := range rows {
		displayName := r.DisplayName
		if len(displayName) > 25 {
			displayName = displayName[:22] + "..."
		}

		tags := "-"
		if len(r.Tags) > 0 {
			tags = strings.Join(r.Tags, ", ")
		}

		fmt.Fprintf(w, "%-20s %-25s %-6d %-12s %-8d %-8s %s\n",
			r.Name,
			displayName,
			r.ModeCount,
			r.Strategy,
			r.MaxTokens,
			r.Source,
			tags,
		)
	}

	fmt.Fprintf(w, "\nTotal: %d presets\n", len(rows))
	fmt.Fprintln(w, "\nUse --verbose for full configuration details.")
	return nil
}

// renderPresetsTableVerbose renders detailed preset info in a readable format.
func renderPresetsTableVerbose(w io.Writer, details []PresetDetail) error {
	if len(details) == 0 {
		fmt.Fprintln(w, "No ensemble presets found.")
		return nil
	}

	for i, d := range details {
		if i > 0 {
			fmt.Fprintln(w, strings.Repeat("=", 80))
		}

		fmt.Fprintf(w, "Name:        %s\n", d.Name)
		if d.DisplayName != "" {
			fmt.Fprintf(w, "Display:     %s\n", d.DisplayName)
		}
		fmt.Fprintf(w, "Description: %s\n", d.Description)
		fmt.Fprintf(w, "Source:      %s\n", d.Source)

		if len(d.Tags) > 0 {
			fmt.Fprintf(w, "Tags:        %s\n", strings.Join(d.Tags, ", "))
		}

		if d.AllowAdvanced {
			fmt.Fprintf(w, "Advanced:    yes (may include advanced-tier modes)\n")
		}

		fmt.Fprintf(w, "\nModes (%d):\n", d.ModeCount)
		for _, m := range d.Modes {
			if m.Name != "" {
				fmt.Fprintf(w, "  - %-20s [%s] %s (%s)\n", m.ID, m.Code, m.Name, m.Tier)
			} else {
				fmt.Fprintf(w, "  - %s\n", m.ID)
			}
		}

		fmt.Fprintf(w, "\nSynthesis:\n")
		fmt.Fprintf(w, "  Strategy:       %s\n", d.Synthesis.Strategy)
		fmt.Fprintf(w, "  Min Confidence: %.2f\n", d.Synthesis.MinConfidence)
		fmt.Fprintf(w, "  Max Findings:   %d\n", d.Synthesis.MaxFindings)

		fmt.Fprintf(w, "\nBudget:\n")
		fmt.Fprintf(w, "  Tokens/Mode: %d\n", d.Budget.MaxTokensPerMode)
		fmt.Fprintf(w, "  Total:       %d\n", d.Budget.MaxTotalTokens)
		fmt.Fprintf(w, "  Timeout/Mode: %s\n", d.Budget.TimeoutPerMode)
		fmt.Fprintf(w, "  Total Timeout: %s\n", d.Budget.TotalTimeout)

		if d.AgentDistribution != nil {
			fmt.Fprintf(w, "\nAgent Distribution:\n")
			fmt.Fprintf(w, "  Strategy: %s", d.AgentDistribution.Strategy)
			if d.AgentDistribution.MaxAgents > 0 {
				fmt.Fprintf(w, ", Max Agents: %d", d.AgentDistribution.MaxAgents)
			}
			if d.AgentDistribution.PreferredAgentType != "" {
				fmt.Fprintf(w, ", Preferred: %s", d.AgentDistribution.PreferredAgentType)
			}
			fmt.Fprintln(w)
		}

		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "Total: %d presets\n", len(details))
	return nil
}

// writeJSON is ntm's output.WriteJSON with pretty printing.
func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// RenderYAML outputs data as YAML.
func RenderYAML(w io.Writer, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		_, err = w.Write([]byte("\n"))
		return err
	}
	return nil
}

// Table outputs tabular data in text format
type Table struct {
	writer  io.Writer
	headers []string
	rows    [][]string
	widths  []int
}

// NewTable creates a new table with headers
func NewTable(w io.Writer, headers ...string) *Table {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	return &Table{
		writer:  w,
		headers: headers,
		rows:    [][]string{},
		widths:  widths,
	}
}

// AddRow adds a row to the table
func (t *Table) AddRow(cols ...string) {
	for i, c := range cols {
		w := utf8.RuneCountInString(c)
		if i < len(t.widths) && w > t.widths[i] {
			t.widths[i] = w
		}
	}
	t.rows = append(t.rows, cols)
}

// Render outputs the table
func (t *Table) Render() {
	// Build format string
	formats := make([]string, len(t.widths))
	for i, w := range t.widths {
		formats[i] = fmt.Sprintf("%%-%ds", w)
	}
	rowFmt := "  " + strings.Join(formats, "  ") + "\n"

	// Print headers
	headerArgs := make([]interface{}, len(t.headers))
	for i, h := range t.headers {
		headerArgs[i] = h
	}
	fmt.Fprintf(t.writer, rowFmt, headerArgs...)

	// Print separator
	seps := make([]interface{}, len(t.widths))
	for i, w := range t.widths {
		seps[i] = strings.Repeat("-", w)
	}
	fmt.Fprintf(t.writer, rowFmt, seps...)

	// Print rows
	for _, row := range t.rows {
		rowArgs := make([]interface{}, len(t.headers))
		for i := range t.headers {
			if i < len(row) {
				rowArgs[i] = row[i]
			} else {
				rowArgs[i] = ""
			}
		}
		fmt.Fprintf(t.writer, rowFmt, rowArgs...)
	}
}
