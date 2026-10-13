package bindings

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// OnlyOtherProjects lists projects, sorted and once each, when it names
// at least one and none of them is projectKey: an agent of projectKey
// then shares its workspace with other projects' agents only, and a send
// by session and name can miss it there (nhm-8ze). nil when projects is
// empty or holds projectKey.
func OnlyOtherProjects(projects []string, projectKey string) []string {
	if len(projects) == 0 || slices.Contains(projects, projectKey) {
		return nil
	}
	out := slices.Clone(projects)
	slices.Sort(out)
	return slices.Compact(out)
}

// OtherProjectsIn is OnlyOtherProjects over the active bindings of
// workspaceID: nil when the workspace holds none, or one of projectKey.
func (s *Store) OtherProjectsIn(ctx context.Context, workspaceID, projectKey string) ([]string, error) {
	if workspaceID == "" {
		return nil, nil
	}
	list, err := s.List(ctx, Filter{WorkspaceID: workspaceID, Statuses: []Status{Active}})
	if err != nil {
		return nil, err
	}
	projects := make([]string, 0, len(list))
	for _, b := range list {
		projects = append(projects, b.MailProjectKey)
	}
	return OnlyOtherProjects(projects, projectKey), nil
}

// MixedWorkspaceWarning is the warning of nhm add and nhm adopt when an
// agent of projectKey goes into a workspace that holds other projects'
// agents only. They warn and go on: refusing is the operator's choice.
func MixedWorkspaceWarning(workspaceID, projectKey string, others []string) string {
	return fmt.Sprintf("workspace %s holds agents of other projects only (%s), not of %s: nhm send by session and name can miss an agent of this project there",
		workspaceID, strings.Join(others, ", "), projectKey)
}
