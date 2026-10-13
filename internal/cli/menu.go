package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/integrations/kinds"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/menu"
	"github.com/arosl/nhm/internal/ui/runner"
	"github.com/arosl/nhm/internal/ui/tui/theme"
)

// runGoMenu is nhm menu --go, and the popup body with [ui] menu = "go":
// the Go menu over the action table, for the context the popup captured
// (plan-v4 section 6). It runs in the captured pane's directory, so every
// verb finds the project there; its mutations run as jobs of their own
// (uiCore), so closing it cancels nothing.
func runGoMenu(stderr io.Writer) int {
	fail := func(code int, err error) int {
		fmt.Fprintf(stderr, "nhm menu: %v\n", err)
		return code
	}
	c, err := popupContext()
	if err != nil {
		return fail(exitError, err)
	}
	if st, err := os.Stat(c.Cwd); c.Cwd != "" && err == nil && st.IsDir() {
		if err := os.Chdir(c.Cwd); err != nil {
			return fail(exitError, err)
		}
	}
	ie, err := openInstance(c.SocketPath)
	if err != nil {
		return fail(exitError, err)
	}
	defer func() { _ = ie.db.Close() }()
	// A restored or handed-on context must be this server's.
	if err := c.CheckInstance(ie.inst.ID); err != nil {
		return fail(exitRefused, err)
	}
	if c.WorkspaceID != "" && c.WorkspaceLabel == "" {
		// A context from a pane's environment has no label: kill asks for
		// the label the operator sees, so read it once now.
		c.WorkspaceLabel = workspaceLabel(ie, c.WorkspaceID)
	}
	src, closeSrc, err := pickerSource(c.SocketPath)
	if err != nil {
		return fail(exitError, err)
	}
	defer closeSrc()
	surface, err := menuSurfaces(c)
	if err != nil {
		return fail(exitError, err)
	}
	jobs := uiJobs(ie, c)
	env := menu.Env{
		Context:    c,
		Core:       uiCore(ie, c, jobs),
		NewID:      func() (string, error) { return store.NewOperationID(time.Now()) },
		Agents:     src,
		Views:      viewsEnv(c),
		Operations: uiOperations{ie: ie, jobs: jobs},
		Kinds:      kinds.All(),
		Surface:    surface,
	}
	theme.ApplyLipGlossDefaults(theme.Current())
	if _, err := tea.NewProgram(menu.New(env), tea.WithAltScreen()).Run(); err != nil {
		return fail(exitError, err)
	}
	return exitOK
}

// workspaceLabel is a workspace's label in herdr's snapshot; "" when
// herdr does not answer or has no such workspace.
func workspaceLabel(ie *instanceEnv, id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := ie.herdr.Snapshot(ctx)
	if err != nil {
		return ""
	}
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == id {
			return w.Label
		}
	}
	return ""
}

// uiOperations reads the menu's recent operations: the core's records
// and the jobs the surfaces started. It only reads.
type uiOperations struct {
	ie   *instanceEnv
	jobs runner.Client
}

func (o uiOperations) RecentOperations(ctx context.Context, limit int) ([]lifecycle.RecentOperation, error) {
	return lifecycle.RecentOperations(ctx, o.ie.db, limit)
}

func (o uiOperations) RecentJobs(limit int) ([]runner.Entry, error) { return o.jobs.Recent(limit) }

// menuSurfaces gives the command of each surface the menu runs in its
// popup, by [ui] in nhm's config, read once: the palette (the Go palette
// when [ui] palette = "go", else today's), "palette (new)" while the Go
// palette is not the default, and the board (the Go board's tab when
// [ui] board = "go", else today's board in this popup).
func menuSurfaces(c ui.Context) (func(id string) *exec.Cmd, error) {
	paletteEngine, err := surfaceEngine("palette")
	if err != nil {
		return nil, err
	}
	boardEngine, err := surfaceEngine("board")
	if err != nil {
		return nil, err
	}
	args := map[string][]string{"palette": {"palette"}, "palette.go": {"palette", "--go"}, "board": {"board"}}
	if paletteEngine == engineGo {
		args["palette"] = []string{"palette", "--go"}
		delete(args, "palette.go")
	}
	if boardEngine == engineGo {
		// It opens the board's tab, or focuses it, and returns.
		args["board"] = []string{"ui", "board"}
	}
	return func(id string) *exec.Cmd {
		a, ok := args[id]
		if !ok {
			return nil
		}
		cmd := exec.Command(self(), a...)
		cmd.Env = nhmEnv(c)
		if st, err := os.Stat(c.Cwd); c.Cwd != "" && err == nil && st.IsDir() {
			cmd.Dir = c.Cwd
		}
		return cmd
	}, nil
}
