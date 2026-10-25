// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/session.go (newListCmd, runListPaged,
// buildSessionListResponse, paginateSessionList) and
// internal/output/schema.go (ListResponse, SessionListItem); see LICENSE
// in this directory and PROVENANCE.md. Local changes: a session is a
// herdr workspace, named by its label (else its id); its windows are the
// workspace's tabs, attached is herdr's workspace focus, and its folder
// is the one project folder of its bound agents (herdr keeps none for a
// workspace); its panes are the workspace's (core/session.Panes), a pane
// without an agent being ntm's user pane; tags end the herdr pane label;
// a server that nhm has not enrolled shows herdr's agents only; "tmux" in
// the texts reads "herdr"; --socket beside ntm's flags; the output goes
// to cobra's writer.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/session"
	"github.com/arosl/nhm/internal/core/sessionlabel"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/robot"
	"github.com/arosl/nhm/internal/store"
)

const listUsage = `Usage: nhm list [--tag T]… [-p|--project NAME] [--limit N] [--offset N] [--socket PATH] [--json]
       (also ls, l)

ntm's list: the sessions, each a herdr workspace. On a terminal of 100
columns or more, a table SESSION WINDOWS STATE AGENTS (with PROJECT and
LABEL when a session name has a "--label"): the windows are the
workspace's tabs, the state is attached for the focused workspace, and
the agents are counted by kind ("2 CC, 1 COD, 1 Usr"). Else one line per
session, "  name: N windows (attached)".

--tag (repeatable, or a comma list) keeps a session where a pane's herdr
label ends in one of the tags, "Name model[a,b]". --project keeps the
sessions of one base project name. --limit and --offset page the list;
--json gives ntm's ListResponse, with _agent_hints.next_offset for the
next page. nhm snapshot --json lists every agent with its binding.
`

// listResponse is ntm's output.ListResponse, the JSON of
// `nhm list --json`.
type listResponse struct {
	timestampedResponse
	Sessions     []sessionListItem           `json:"sessions"`
	Count        int                         `json:"count"`
	TotalMatches int                         `json:"total_matches"`
	HasMore      bool                        `json:"has_more"`
	Pagination   *robot.PaginationInfo       `json:"pagination,omitempty"`
	AgentHints   *robot.PaginationAgentHints `json:"_agent_hints,omitempty"`
}

// sessionListItem is ntm's output.SessionListItem, one session of the
// list.
type sessionListItem struct {
	Name             string               `json:"name"`
	BaseProject      string               `json:"base_project"`
	Label            string               `json:"label,omitempty"`
	Windows          int                  `json:"windows"`
	PaneCount        int                  `json:"pane_count"`
	Attached         bool                 `json:"attached"`
	WorkingDirectory string               `json:"working_directory,omitempty"`
	AgentCounts      *agentCountsResponse `json:"agents,omitempty"`
}

// listWidth is the terminal width that picks the wide table; 0 for a
// writer that is not a terminal. Tests replace it.
var listWidth = func(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}

func newListCmd() *cobra.Command {
	var tags []string
	var project, socket string
	var limit, offset int
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "l"},
		Short:   "List all herdr sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListNTM(tags, project, limit, offset, socket, wantJSON(cmd), cmd.OutOrStdout())
		},
	}
	fs := cmd.Flags()
	fs.StringSliceVar(&tags, "tag", nil, "filter sessions by agent tag (shows session if any agent matches)")
	fs.StringVarP(&project, "project", "p", "", "filter by base project name (shows all labeled sessions for the project)")
	fs.IntVar(&limit, "limit", 0, "maximum sessions per page (0 = all)")
	fs.IntVar(&offset, "offset", 0, "pagination offset (use _agent_hints.next_offset for the next page)")
	fs.StringVar(&socket, "socket", "", "herdr API socket")
	return cmd
}

// runListNTM is ntm's runListPaged: ntm's ListResponse under --json, else
// the table or the lines.
func runListNTM(tags []string, project string, limit, offset int, socket string, asJSON bool, stdout io.Writer) error {
	resp, err := buildSessionList(context.Background(), socket, tags, project)
	if err != nil {
		if asJSON {
			return jsonFailure(stdout, err)
		}
		return err
	}
	paginateSessionList(&resp, limit, offset)
	if asJSON {
		return writeNTMJSON(stdout, resp)
	}
	if len(resp.Sessions) == 0 {
		fmt.Fprintln(stdout, "No herdr sessions running")
		return nil
	}
	if listWidth(stdout) < 100 {
		for _, s := range resp.Sessions {
			attached := ""
			if s.Attached {
				attached = " (attached)"
			}
			fmt.Fprintf(stdout, "  %s: %d windows%s\n", s.Name, s.Windows, attached)
		}
		return nil
	}
	hasLabels := false
	for _, s := range resp.Sessions {
		if s.Label != "" {
			hasLabels = true
			break
		}
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
	if hasLabels {
		fmt.Fprintln(w, "SESSION\tPROJECT\tLABEL\tWINDOWS\tSTATE\tAGENTS")
	} else {
		fmt.Fprintln(w, "SESSION\tWINDOWS\tSTATE\tAGENTS")
	}
	for _, s := range resp.Sessions {
		attached := "detached"
		if s.Attached {
			attached = "attached"
		}
		agents := listAgentsCell(s.AgentCounts)
		if hasLabels {
			label := "-"
			if s.Label != "" {
				label = s.Label
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", s.Name, s.BaseProject, label, s.Windows, attached, agents)
		} else {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", s.Name, s.Windows, attached, agents)
		}
	}
	return w.Flush()
}

// listAgentsCell is ntm's agents summary, "2 CC, 1 COD, 1 Usr", or "-".
func listAgentsCell(c *agentCountsResponse) string {
	if c == nil {
		return "-"
	}
	var parts []string
	for _, n := range []struct {
		count int
		short string
	}{
		{c.Claude, "CC"}, {c.Codex, "COD"}, {c.Gemini, "GMI"}, {c.Antigravity, "AGY"}, {c.Grok, "Grok"},
		{c.Omp, "OMP"}, {c.Opencode, "OC"}, {c.Ollama, "OLL"}, {c.Cursor, "CUR"}, {c.Windsurf, "WND"},
		{c.Aider, "AID"}, {c.User, "Usr"}, {c.Other, "Oth"},
	} {
		if n.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n.count, n.short))
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

// buildSessionList is ntm's buildSessionListResponse on herdr: every
// workspace, filtered by tag and base project, sorted by base project,
// then unlabeled first, then label, then name.
func buildSessionList(ctx context.Context, socket string, tags []string, project string) (listResponse, error) {
	snap, bound, err := listSources(ctx, socket)
	if err != nil {
		return listResponse{}, err
	}
	dirs := map[string]map[string]bool{}
	for _, b := range bound {
		if b.WorkspaceID == "" || b.MailProjectKey == "" {
			continue
		}
		if dirs[b.WorkspaceID] == nil {
			dirs[b.WorkspaceID] = map[string]bool{}
		}
		dirs[b.WorkspaceID][b.MailProjectKey] = true
	}
	items := make([]sessionListItem, 0, len(snap.Workspaces))
	for _, w := range snap.Workspaces {
		name := w.Label
		if name == "" {
			name = w.WorkspaceID
		}
		if len(tags) > 0 && !workspaceHasTag(snap, w.WorkspaceID, tags) {
			continue
		}
		if project != "" && sessionlabel.SessionBase(name) != project {
			continue
		}
		base, label := sessionlabel.ParseSessionLabel(name)
		item := sessionListItem{Name: name, BaseProject: base, Label: label, Windows: int(w.TabCount), Attached: w.Focused}
		if len(dirs[w.WorkspaceID]) == 1 {
			for d := range dirs[w.WorkspaceID] {
				item.WorkingDirectory = d
			}
		}
		if panes := session.Panes(snap, w.WorkspaceID, bound); len(panes) > 0 {
			item.PaneCount = len(panes)
			counts := agentCountsResponse{}
			for _, p := range panes {
				incrementAgentCounts(&counts, p.Type)
			}
			item.AgentCounts = &counts
		}
		items = append(items, item)
	}
	// Stable: herdr's labels need not be unique, so two sessions can share
	// a name; they keep herdr's order.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].BaseProject != items[j].BaseProject {
			return items[i].BaseProject < items[j].BaseProject
		}
		if (items[i].Label == "") != (items[j].Label == "") {
			return items[i].Label == ""
		}
		if items[i].Label != items[j].Label {
			return items[i].Label < items[j].Label
		}
		return items[i].Name < items[j].Name
	})
	return listResponse{timestampedResponse: newTimestamped(), Sessions: items, Count: len(items), TotalMatches: len(items)}, nil
}

// workspaceHasTag reports whether a pane of the workspace has one of the
// tags at the end of its herdr label.
func workspaceHasTag(snap herdr.SessionSnapshot, workspaceID string, tags []string) bool {
	for _, p := range snap.Panes {
		if p.WorkspaceID == workspaceID && session.HasAnyTag(session.PaneTags(p.Label), tags) {
			return true
		}
	}
	return false
}

// listSources reads herdr's snapshot and, where nhm has enrolled the
// server, the active bindings; list needs no enrolment, as ntm's needs
// only tmux.
func listSources(ctx context.Context, socket string) (herdr.SessionSnapshot, []bindings.Binding, error) {
	if socket == "" {
		socket = os.Getenv("HERDR_SOCKET_PATH")
	}
	if socket == "" {
		return herdr.SessionSnapshot{}, nil, errors.New("no herdr socket: give --socket or run inside herdr (HERDR_SOCKET_PATH)")
	}
	snap, err := herdr.New(socket).Snapshot(ctx)
	if err != nil {
		return herdr.SessionSnapshot{}, nil, err
	}
	root, err := store.StateRoot()
	if err != nil {
		return herdr.SessionSnapshot{}, nil, err
	}
	inst, err := store.Lookup(root, socket)
	if errors.Is(err, store.ErrNotEnrolled) {
		return snap, nil, nil
	}
	if err != nil {
		return herdr.SessionSnapshot{}, nil, err
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		return herdr.SessionSnapshot{}, nil, err
	}
	defer db.Close()
	bound, err := bindings.NewStore(db).List(ctx, bindings.Filter{Statuses: []bindings.Status{bindings.Active}})
	return snap, bound, err
}

// paginateSessionList is ntm's: Sessions becomes one page, Count the page
// size, TotalMatches the count before paging, and HasMore, Pagination and
// _agent_hints.next_offset the rest.
func paginateSessionList(resp *listResponse, limit, offset int) {
	if resp == nil {
		return
	}
	resp.TotalMatches = len(resp.Sessions)
	page, info := robot.ApplyPagination(resp.Sessions, robot.PaginationOptions{Limit: limit, Offset: offset})
	resp.Sessions = page
	resp.Count = len(page)
	if info != nil {
		resp.Pagination = info
		resp.HasMore = info.HasMore
		resp.AgentHints = robot.PaginationHints(info)
	}
}
