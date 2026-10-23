package arrange

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/tabs"
	"github.com/arosl/nhm/internal/herdr"
)

// read reads workspace ws for Make: one snapshot, the layout of each of
// its tabs, and its active bindings, each verified in its pane against
// that snapshot. A binding that is not verified there, or whose agent
// may not share a tab (tabs.Named), gets a Problem, so its tab is left
// as it is. It gives the bindings by id, and the warnings of the agents
// whose environment nhm cannot read, by binding id.
func read(ctx context.Context, env tabs.Env, ws string, perTab int) (Input, map[string]bindings.Binding, map[string]string, error) {
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return Input{}, nil, nil, err
	}
	list, err := env.Bindings.List(ctx, bindings.Filter{WorkspaceID: ws, Statuses: []bindings.Status{bindings.Active}})
	if err != nil {
		return Input{}, nil, nil, err
	}
	in := Input{PerTab: perTab, Tabs: []Tab{}, Agents: []Agent{}}
	panes := map[string][]herdr.LayoutPane{} // each tab's panes, as the snapshot lists them
	for _, p := range snap.Panes {
		if p.WorkspaceID == ws {
			panes[p.TabID] = append(panes[p.TabID], herdr.LayoutPane{PaneID: p.PaneID})
		}
	}
	for _, tb := range snap.Tabs {
		if tb.WorkspaceID != ws {
			continue
		}
		t := Tab{TabID: tb.TabID, Label: tb.Label, Panes: panes[tb.TabID]}
		if len(t.Panes) > 0 {
			l, err := env.Herdr.PaneLayout(ctx, t.Panes[0].PaneID)
			switch {
			case err != nil:
				t.LayoutErr = err.Error()
			case l.TabID != "" && l.TabID != tb.TabID:
				t.LayoutErr = fmt.Sprintf("herdr gave the layout of tab %s", l.TabID)
			default:
				t.Panes = l.Panes
			}
		}
		in.Tabs = append(in.Tabs, t)
	}
	byID, warn := map[string]bindings.Binding{}, map[string]string{}
	one := snapped{Herdr: env.Herdr, snap: snap}
	for _, b := range list {
		byID[b.ID] = b
		a := Agent{BindingID: b.ID, MailName: b.MailName, Project: b.MailProjectKey, PaneID: b.PaneID, Slot: b.Slot}
		v, err := bindings.Verify(ctx, one, b)
		switch {
		case err != nil:
			a.Problem = fmt.Sprintf("%s is not verified: %v", b.MailName, err)
		case v.Verdict != bindings.Verified:
			a.Problem = fmt.Sprintf("%s is %s in %s, not verified (nhm resolve %s says why)", b.MailName, v.Verdict, b.PaneID, b.MailName)
		}
		if a.Problem == "" {
			w, err := tabs.Named(b)
			switch {
			case errors.Is(err, tabs.ErrRefused):
				a.Problem = strings.TrimPrefix(err.Error(), tabs.ErrRefused.Error()+": ")
			case err != nil:
				a.Problem = err.Error()
			case w != "":
				warn[b.ID] = w
			}
		}
		in.Agents = append(in.Agents, a)
	}
	return in, byID, warn, nil
}

// snapped answers Snapshot with the one that read took, so that every
// binding is verified against the same reading as the plan.
type snapped struct {
	tabs.Herdr
	snap herdr.SessionSnapshot
}

func (s snapped) Snapshot(context.Context) (herdr.SessionSnapshot, error) { return s.snap, nil }
