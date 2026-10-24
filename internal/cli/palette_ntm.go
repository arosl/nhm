// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/cli/palette.go
// (newPaletteCmd, runPalette's --json branch) and internal/cli/root.go (the
// --robot-palette branch and its flags, resolveOptionalRobotSessionFilter)
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md.
// Local changes: the TUI is nhm's palette (runPalette, palette_go.go), and
// a session is a herdr workspace; the commands are nhm's palette catalogs
// and its pins and favorites (internal/ui/palette), not ntm's config; nhm's
// --list, --show, --file and --workspace stay as extras; the output goes to
// cobra's writer.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/ui/palette"
)

func newPaletteCmd() *cobra.Command {
	var workspace, file, show string
	var list, goFlag bool
	cmd := &cobra.Command{
		Use:   "palette [session]",
		Short: "Open the interactive command palette",
		Long: `Open an interactive TUI to select and send pre-configured prompts to agents.

The palette shows all commands of your palette files, organized by category.
Filter by typing, select with Enter, then choose the target agents.

If no session is specified and you're inside herdr, uses the current workspace.

Examples:
  nhm palette myproject  # Open palette for specific session
  nhm palette            # Use current herdr workspace`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			if wantJSON(cmd) && !list {
				if show != "" {
					return errors.New("--json lists the palette; it does not go with --show")
				}
				ctx, cancel := context.WithTimeout(context.Background(), paletteGoWait)
				defer cancel()
				out, err := paletteOutput(ctx, "", name, file, robot.PaletteOptions{Session: name})
				if err != nil {
					return err
				}
				robotWrite(cmd.OutOrStdout(), out, robotOK)
				return nil
			}
			legacy := []string{}
			if name != "" {
				if workspace != "" {
					return errors.New("give a session or --workspace, not both")
				}
				ws, err := paletteWorkspace(name)
				if err != nil {
					return err
				}
				workspace = ws
			}
			if workspace != "" {
				legacy = append(legacy, "--workspace", workspace)
			}
			if file != "" {
				legacy = append(legacy, "--file", file)
			}
			if list {
				legacy = append(legacy, "--list")
				if wantJSON(cmd) {
					legacy = append(legacy, "--json")
				}
			}
			if show != "" {
				legacy = append(legacy, "--show", show)
			}
			// nhm's palette writes its own errors; the exit is ntm's.
			if code := runPalette(legacy, cmd.OutOrStdout(), cmd.ErrOrStderr()); code != exitOK {
				return verbExit{exitError}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&workspace, "workspace", "", "the workspace whose agents get prompts (nhm)")
	fl.StringVar(&file, "file", "", "read prompts from this file only (nhm)")
	fl.BoolVar(&list, "list", false, "print the prompts and exit (nhm)")
	fl.StringVar(&show, "show", "", "print one prompt's text and exit (nhm)")
	fl.BoolVar(&goFlag, "go", true, "accepted; it does nothing")
	_ = fl.MarkHidden("go")
	return cmd
}

// paletteWorkspace is the herdr workspace of a named session; ntm's
// palette needs the session to be there.
func paletteWorkspace(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), paletteGoWait)
	defer cancel()
	ie, err := openInstance("")
	if err != nil {
		return "", err
	}
	defer ie.db.Close()
	r, err := resolveSession(ctx, ie, name, false, io.Discard, io.Discard)
	var re *session.ResolveExplicitSessionNameError
	if errors.As(err, &re) && re.Kind != session.ErrorAmbiguous {
		return "", fmt.Errorf("session '%s' not found", name)
	}
	if err != nil {
		return "", err
	}
	return r.WorkspaceID, nil
}

// paletteOutput is ntm's PaletteOutput over nhm's palette: the catalogs of
// the session's project (its bound agents'), else of the cwd and its git
// work tree, as --list reads them; the pins and favorites of the
// palette's state. A catalog or state file that does not read is a
// warning, as ntm's unreadable history is.
func paletteOutput(ctx context.Context, socket, name, file string, opts robot.PaletteOptions) (*robot.PaletteOutput, error) {
	dir, _ := os.Getwd()
	root := ""
	if s, err := core.ResolveScope(ctx, core.ScopeRequest{Cwd: dir}, nil); err == nil {
		root = s.ProjectRoot
	}
	if name = strings.TrimSpace(name); name != "" {
		// The resolver checks the name (after a workspace id or label).
		if ie, err := openInstance(socket); err == nil {
			defer ie.db.Close()
			r, err := resolveSession(ctx, ie, name, true, io.Discard, io.Discard)
			var re *session.ResolveExplicitSessionNameError
			switch {
			case err == nil:
				opts.Session = r.Session
				bound, err := bindings.NewStore(ie.db).List(ctx, bindings.Filter{WorkspaceID: r.WorkspaceID, Statuses: []bindings.Status{bindings.Active}})
				if err != nil {
					return nil, err
				}
				if p := sessionProject(bound); p != "" {
					dir, root = p, p
				}
			case errors.As(err, &re):
				if re.Kind == session.ErrorAmbiguous {
					return nil, err
				}
				// As ntm's: a session that is not there filters as named.
			default:
				return nil, err // an invalid name, or herdr's error
			}
		}
	}
	if file != "" {
		abs, err := filepath.Abs(file)
		if err != nil {
			return nil, err
		}
		file = abs
	}
	cmds, errs := palette.LoadCommands(palette.CatalogPaths(os.Getenv, dir, root, file))
	src := robot.PaletteSource{}
	for _, c := range cmds {
		src.Commands = append(src.Commands, robot.PaletteCmd{Key: c.Key, Label: c.Label, Category: c.Category, Prompt: c.Prompt, Tags: c.Tags})
	}
	state, err := palette.Store{Dir: palette.StateDir(os.Getenv)}.Load()
	if err != nil {
		errs = append(errs, fmt.Errorf("pins and favorites: %w", err))
	}
	src.Favorites, src.Pinned = state.Favorites, state.Pinned
	out := robot.GetPalette(src, opts, time.Now())
	for _, e := range errs {
		out.AgentHints.Warnings = append(out.AgentHints.Warnings, e.Error())
	}
	return out, nil
}

// runRobotPalette is ntm's --robot-palette [--session=S] [--category=C]
// [--search=Q] (and the deprecated --palette-session, --palette-category
// and --palette-search): ntm's PaletteOutput.
func runRobotPalette(args []string, stdout io.Writer) int {
	now := time.Now()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("robot-palette", false, "")
	var sess, category, search string
	for _, alias := range []string{"session", "palette-session"} {
		fs.StringVar(&sess, alias, "", "")
	}
	for _, alias := range []string{"category", "palette-category"} {
		fs.StringVar(&category, alias, "", "")
	}
	for _, alias := range []string{"search", "palette-search"} {
		fs.StringVar(&search, alias, "", "")
	}
	format := fs.String("robot-format", "auto", "")
	socket := fs.String("socket", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		msg := "unexpected arguments"
		if err != nil {
			msg = err.Error()
		}
		return robotFail(stdout, robot.ErrCodeInvalidFlag, msg, "nhm --robot-capabilities lists the robot flags", "json", robotError, now)
	}
	switch *format {
	case "auto", "json":
	default:
		return robotFail(stdout, robot.ErrCodeInvalidFlag, fmt.Sprintf("--robot-format=%s: nhm writes json only", *format),
			"use --robot-format=json", "json", robotError, now)
	}
	ctx, cancel := context.WithTimeout(context.Background(), paletteGoWait)
	defer cancel()
	out, err := paletteOutput(ctx, *socket, sess, "", robot.PaletteOptions{Session: strings.TrimSpace(sess), Category: category, SearchQuery: search})
	if err != nil {
		return robotFail(stdout, robot.ErrCodeSessionNotFound, err.Error(), "Use 'nhm list' to see available sessions", "json", robotError, now)
	}
	return robotWrite(stdout, out, robotOK)
}
