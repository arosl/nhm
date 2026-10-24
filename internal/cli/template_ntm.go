// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/template.go
// (newTemplateCmd, newTemplateListCmd, runTemplateList, TemplateListItem,
// newTemplateShowCmd, TemplateShowOutput, TemplateVariableOutput,
// runTemplateShow) at commit 9d5ed977; see LICENSE in this directory and
// PROVENANCE.md. Local changes: nhm's loader and its places (the project's
// .nhm/templates, ~/.config/nhm/templates, the palette catalogs, then the
// builtins; the operator's ruling, 2026-10-24: nhm keeps its own paths);
// the project is the cwd's, as send -t finds it; the help names nhm; the
// output goes to cobra's writer.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core/templates"
)

// templateUsage is `nhm template`'s help, as nhm's other verbs have one.
const templateUsage = `Usage: nhm template list|ls [-a|--all] [--json]
       nhm template show <name> [-b|--body] [--json]

ntm's prompt templates: the ones send -t takes, first of a name wins,
from the project's .nhm/templates, ~/.config/nhm/templates, the palette
catalogs (the project's command_palette.md, ~/.config/nhm/command_palette.md,
then ACFS's palette), then the builtins.

list prints ntm's table of names, descriptions and sources; -a adds each
template's variables. show prints one template's card; -b adds its body.
--json gives ntm's TemplateListItem array and TemplateShowOutput.
`

func newTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage prompt templates",
		Long: `Manage reusable prompt templates with variable substitution.

Templates are markdown files with optional YAML frontmatter that define
reusable prompts. They support variable substitution and conditional sections.

Template locations (in order of precedence):
  1. Project: .nhm/templates/*.md
  2. User:    ~/.config/nhm/templates/*.md
  3. Palette: the project's command_palette.md, ~/.config/nhm/command_palette.md,
              then ACFS's palette
  4. Builtin: Embedded in the nhm binary

Example template format:
  ---
  name: code_review
  description: Review code for quality issues
  variables:
    - name: file
      description: File to review
      required: true
    - name: focus
      description: Area to focus on
  ---
  Review the following code:
  {{file}}

  {{#focus}}
  Focus on: {{focus}}
  {{/focus}}

Variables:
  {{variable}}         - Simple substitution
  {{#var}}...{{/var}}  - Conditional (included only if var is set)

A {{variable}} that nothing fills is an error; it is never sent as literal
"{{name}}" text. Wrap optional values in {{#name}}...{{/name}}.

Use with nhm send:
  nhm send myproject --template=code_review --file=src/main.go
  nhm send myproject -t refactor --var goal="simplify" --file=src/main.go`,
	}
	cmd.AddCommand(newTemplateListCmd(), newTemplateShowCmd())
	return cmd
}

// templateLoader is the loader of the cwd's project: its git work tree,
// else the cwd itself, as ntm falls back to the cwd's templates folder.
func templateLoader() *templates.Loader {
	root, _ := os.Getwd()
	if s, err := projectScope(context.Background(), "", ""); err == nil {
		root = s.ProjectRoot
	}
	return templates.NewLoader(root)
}

func newTemplateListCmd() *cobra.Command {
	var showAll bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available templates",
		Long: `List all available prompt templates.

Shows templates from all sources (project, user, palette, builtin) with their
descriptions and source locations.`,
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplateList(cmd.OutOrStdout(), wantJSON(cmd), showAll)
		},
	}
	cmd.Flags().BoolVarP(&showAll, "all", "a", false, "show all details including variables")
	return cmd
}

// TemplateListItem is ntm's JSON for one template of template list.
type TemplateListItem struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	SourcePath  string   `json:"source_path,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Variables   []string `json:"variables,omitempty"`
}

func runTemplateList(out io.Writer, asJSON, showAll bool) error {
	tmpls, err := templateLoader().List()
	if err != nil {
		return err
	}
	if len(tmpls) == 0 {
		if asJSON {
			return json.NewEncoder(out).Encode([]TemplateListItem{})
		}
		fmt.Fprintln(out, "No templates found")
		return nil
	}
	if asJSON {
		items := make([]TemplateListItem, len(tmpls))
		for i, t := range tmpls {
			items[i] = TemplateListItem{Name: t.Name, Description: t.Description, Source: t.Source.String(),
				SourcePath: t.SourcePath, Tags: t.Tags}
			if showAll {
				for _, v := range t.Variables {
					items[i].Variables = append(items[i].Variables, v.Name)
				}
			}
		}
		return json.NewEncoder(out).Encode(items)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION\tSOURCE")
	fmt.Fprintln(w, "----\t-----------\t------")
	for _, t := range tmpls {
		desc := t.Description
		if len(desc) > 40 {
			desc = desc[:37] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name, desc, t.Source.String())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if showAll {
		fmt.Fprintln(out)
		for _, t := range tmpls {
			if len(t.Variables) == 0 {
				continue
			}
			fmt.Fprintf(out, "Template '%s' variables:\n", t.Name)
			for _, v := range t.Variables {
				req, desc := "", ""
				if v.Required {
					req = " (required)"
				}
				if v.Description != "" {
					desc = " - " + v.Description
				}
				fmt.Fprintf(out, "  - %s%s%s\n", v.Name, req, desc)
			}
			fmt.Fprintln(out)
		}
	}
	return nil
}

func newTemplateShowCmd() *cobra.Command {
	var showBody bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show template details",
		Long: `Show detailed information about a specific template.

Displays the template's metadata, variables, and optionally the template body.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTemplateShow(cmd.OutOrStdout(), wantJSON(cmd), args[0], showBody)
		},
	}
	cmd.Flags().BoolVarP(&showBody, "body", "b", false, "show the template body")
	return cmd
}

// TemplateShowOutput is ntm's JSON of template show.
type TemplateShowOutput struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Source      string                   `json:"source"`
	SourcePath  string                   `json:"source_path,omitempty"`
	Tags        []string                 `json:"tags,omitempty"`
	Variables   []TemplateVariableOutput `json:"variables,omitempty"`
	Body        string                   `json:"body,omitempty"`
}

// TemplateVariableOutput is one variable of TemplateShowOutput.
type TemplateVariableOutput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
}

func runTemplateShow(out io.Writer, asJSON bool, name string, showBody bool) error {
	tmpl, err := templateLoader().Load(name)
	if err != nil {
		return err
	}
	if asJSON {
		o := TemplateShowOutput{Name: tmpl.Name, Description: tmpl.Description, Source: tmpl.Source.String(),
			SourcePath: tmpl.SourcePath, Tags: tmpl.Tags}
		for _, v := range tmpl.Variables {
			o.Variables = append(o.Variables, TemplateVariableOutput{Name: v.Name, Description: v.Description,
				Required: v.Required, Default: v.Default})
		}
		if showBody {
			o.Body = tmpl.Body
		}
		return json.NewEncoder(out).Encode(o)
	}
	fmt.Fprintf(out, "Name:        %s\n", tmpl.Name)
	fmt.Fprintf(out, "Description: %s\n", tmpl.Description)
	fmt.Fprintf(out, "Source:      %s\n", tmpl.Source.String())
	if tmpl.SourcePath != "" {
		fmt.Fprintf(out, "Path:        %s\n", tmpl.SourcePath)
	}
	if len(tmpl.Tags) > 0 {
		fmt.Fprintf(out, "Tags:        %s\n", strings.Join(tmpl.Tags, ", "))
	}
	if len(tmpl.Variables) > 0 {
		fmt.Fprintln(out, "\nVariables:")
		for _, v := range tmpl.Variables {
			req, def := "", ""
			if v.Required {
				req = " (required)"
			}
			if v.Default != "" {
				def = fmt.Sprintf(" [default: %s]", v.Default)
			}
			fmt.Fprintf(out, "  - %s%s%s\n", v.Name, req, def)
			if v.Description != "" {
				fmt.Fprintf(out, "    %s\n", v.Description)
			}
		}
	}
	if showBody {
		fmt.Fprintln(out, "\nBody:")
		fmt.Fprintln(out, "─────────────────────────────────────────")
		fmt.Fprintln(out, tmpl.Body)
		fmt.Fprintln(out, "─────────────────────────────────────────")
	}
	return nil
}
