package bindings

import "path/filepath"

// ProjectTabLabel is the label of a tab that several agents of one
// project share: the project's directory name (nhm-4c4). A tab with one
// agent has that agent's Agent Mail name instead. ACFS's Agent Mail hook
// reads the tab label as the agent's name when no variable names it, so a
// shared tab must never carry one agent's name.
func ProjectTabLabel(projectKey string) string {
	if projectKey == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(projectKey))
}
