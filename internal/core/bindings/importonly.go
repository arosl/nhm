package bindings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
	"github.com/arosl/nhm/internal/store"
)

// `nhm admin recover reconcile --import-only` (plan-v8 section 5): while
// ACFS's Bash owns the instance, nhm reads the agents the Bash spawned
// into bindings of its own, so its read verbs see them. It changes
// nothing in herdr and nothing the Bash keeps.
//
// An agent is imported only on verified evidence, never on a name alone:
//
//   - identity_file: Agent Mail's identity file for the pane, in the
//     pane's project, names the identity; or
//   - bash_spawn: the Bash's token file for the identity exists in the
//     pane's project, and the pane is the only one in a tab labelled with
//     the exact mail name, and herdr's alias is that name in lowercase.
//     The Bash sets those three things at spawn and nothing else does.
//
// When both are present they must agree.

// ProjectOf maps a pane's working directory to its project: the Agent
// Mail project key and nhm's project id. core.ResolveScope does it.
type ProjectOf func(ctx context.Context, cwd string) (projectKey, projectID string, err error)

// ImportRequest is one import run.
type ImportRequest struct {
	// ProjectKey limits the run to one project; empty takes every project.
	ProjectKey string `json:"mail_project_key,omitempty"`
	// PaneID limits the run to one pane (the hook path); empty takes every
	// pane.
	PaneID string `json:"pane_id,omitempty"`
	DryRun bool   `json:"dry_run"`
	// AcfsHome is where the Bash keeps its state ("" means
	// DefaultAcfsHome()).
	AcfsHome    string `json:"-"`
	OperationID string `json:"-"`
}

// Import actions.
const (
	ActionImported    = "imported"
	ActionWouldImport = "would_import"
	ActionBound       = "already_bound"
	ActionSkipped     = "skipped"
)

// ImportItem is what the run did with one agent.
type ImportItem struct {
	PaneID     string     `json:"pane_id"`
	Kind       string     `json:"kind"`
	HerdrName  string     `json:"herdr_name,omitempty"`
	ProjectKey string     `json:"mail_project_key,omitempty"`
	MailName   string     `json:"mail_name,omitempty"`
	Action     string     `json:"action"`
	Reason     string     `json:"reason,omitempty"`
	BindingID  string     `json:"binding_id,omitempty"`
	Evidence   []Evidence `json:"evidence"`
}

// ImportReport is the result of a run.
type ImportReport struct {
	OperationID string       `json:"operation_id,omitempty"`
	DryRun      bool         `json:"dry_run"`
	Items       []ImportItem `json:"items"`
	Imported    int          `json:"imported"`
	Warnings    []string     `json:"warnings"`
}

// ImportOnly runs the import. The caller holds the owner lock shared; any
// owner may run it, since it writes only nhm's own state and Agent Mail's
// identity file.
func ImportOnly(ctx context.Context, env Env, projectOf ProjectOf, req ImportRequest) (ImportReport, error) {
	rep := ImportReport{DryRun: req.DryRun, Items: []ImportItem{}, Warnings: []string{}}
	if req.AcfsHome == "" {
		req.AcfsHome = DefaultAcfsHome()
	}
	snap, err := env.Herdr.Snapshot(ctx)
	if err != nil {
		return rep, fmt.Errorf("bindings: import: %w", err)
	}
	if !req.DryRun {
		claim, err := env.DB.ClaimOperation(ctx, store.ClaimRequest{ID: req.OperationID, Verb: "recover.import",
			Scope: map[string]string{"mail_project_key": req.ProjectKey}, Request: req}, env.now())
		if err != nil {
			return rep, err
		}
		if claim.Existing {
			return rep, fmt.Errorf("bindings: operation %s exists (status %s); nothing was done again", claim.Operation.ID, claim.Operation.Status)
		}
		rep.OperationID = claim.Operation.ID
	}
	agents := append([]herdr.AgentInfo(nil), snap.Agents...)
	sort.Slice(agents, func(i, j int) bool { return agents[i].PaneID < agents[j].PaneID })
	for _, a := range agents {
		if a.Agent == "" || (req.PaneID != "" && a.PaneID != req.PaneID) {
			continue
		}
		item, keep := importOne(ctx, env, projectOf, req, snap, a, rep.OperationID)
		if !keep {
			continue
		}
		if item.Action == ActionImported {
			rep.Imported++
		}
		rep.Items = append(rep.Items, item)
	}
	if rep.OperationID != "" {
		if err := env.DB.SetOperationStatus(ctx, rep.OperationID, "done", true, env.now()); err != nil {
			rep.Warnings = append(rep.Warnings, err.Error())
		}
	}
	return rep, nil
}

// importOne decides about one agent; keep is false for an agent outside
// the requested project.
func importOne(ctx context.Context, env Env, projectOf ProjectOf, req ImportRequest, snap herdr.SessionSnapshot, a herdr.AgentInfo, opID string) (ImportItem, bool) {
	item := ImportItem{PaneID: a.PaneID, Kind: a.Agent, HerdrName: a.Name, Evidence: []Evidence{}}
	skip := func(reason string) (ImportItem, bool) {
		item.Action, item.Reason = ActionSkipped, reason
		return item, true
	}
	if b, err := env.Bindings.ActiveByPane(ctx, a.PaneID); err == nil {
		if req.ProjectKey != "" && b.MailProjectKey != req.ProjectKey {
			return item, false
		}
		item.Action, item.MailName, item.ProjectKey, item.BindingID = ActionBound, b.MailName, b.MailProjectKey, b.ID
		return item, true
	} else if !errors.Is(err, ErrNotFound) {
		return skip(err.Error())
	}
	pane, ok := findPane(snap, a.PaneID)
	if !ok {
		return skip("the pane is not in herdr's snapshot")
	}
	cwd := firstNonEmpty(pane.ForegroundCwd, pane.Cwd, a.ForegroundCwd, a.Cwd)
	if cwd == "" {
		return skip("herdr reports no working directory for the pane")
	}
	projectKey, projectID, err := projectOf(ctx, cwd)
	if err != nil {
		if req.ProjectKey != "" {
			return item, false
		}
		return skip("no project: " + err.Error())
	}
	if req.ProjectKey != "" && projectKey != req.ProjectKey {
		return item, false
	}
	item.ProjectKey = projectKey

	fromFile := ""
	if rec, ok, err := agentmail.ReadIdentity(env.ConfigHome, projectKey, a.PaneID); err != nil {
		return skip("Agent Mail identity file: " + err.Error())
	} else if ok {
		fromFile = rec.Name
		item.Evidence = append(item.Evidence, Evidence{Kind: "identity_file", Detail: rec.Name + " for " + a.PaneID})
	}
	fromBash, why := bashEvidence(req.AcfsHome, projectKey, snap, pane, a)
	if fromBash != "" {
		item.Evidence = append(item.Evidence, Evidence{Kind: "bash_spawn", Detail: why})
	}
	switch {
	case fromFile != "" && fromBash != "" && !strings.EqualFold(fromFile, fromBash):
		return skip(fmt.Sprintf("the identity file names %s, the Bash spawn names %s", fromFile, fromBash))
	case fromFile != "":
		item.MailName = fromFile
	case fromBash != "":
		item.MailName = fromBash
	default:
		return skip("no verified evidence (" + why + "); a name match alone is not evidence")
	}
	alias, err := Alias(item.MailName)
	if err != nil {
		return skip(err.Error())
	}
	if a.Name != "" && a.Name != alias {
		return skip(fmt.Sprintf("herdr's name %s does not fit %s", a.Name, item.MailName))
	}
	if b, err := env.Bindings.ActiveByName(ctx, projectKey, item.MailName); err == nil {
		return skip(fmt.Sprintf("%s is already bound to %s (%s)", item.MailName, orNone(b.PaneID), b.ID))
	}
	if req.DryRun {
		item.Action = ActionWouldImport
		return item, true
	}
	occ := occupantOf(ctx, env.Herdr, pane, a, alias, "")
	occ.HerdrAlias = a.Name // what herdr has; the import renames nothing
	occ.Evidence = append(item.Evidence, occ.Evidence...)
	b, err := env.Bindings.Create(ctx, New{MailName: item.MailName, MailProjectKey: projectKey, ProjectID: projectID,
		Provenance: Imported, CreatedByOp: opID, Occupant: occ})
	if err != nil {
		return skip(err.Error())
	}
	item.Action, item.BindingID, item.Evidence = ActionImported, b.ID, b.Evidence
	if fromFile == "" {
		if _, warn := writeIdentity(env, b); warn != "" {
			item.Reason = warn
		}
	}
	return item, true
}

// bashEvidence returns the mail name the Bash spawn evidence proves for a
// pane, or "" and what is missing.
func bashEvidence(acfsHome, projectKey string, snap herdr.SessionSnapshot, pane herdr.PaneInfo, a herdr.AgentInfo) (string, string) {
	tab, ok := findTab(snap, pane.TabID)
	if !ok || tab.Label == "" {
		return "", "the pane's tab has no label"
	}
	name := tab.Label
	if tab.PaneCount != 1 {
		return "", fmt.Sprintf("tab %q holds %d panes", name, tab.PaneCount)
	}
	alias, err := Alias(name)
	if err != nil || a.Name != alias {
		return "", fmt.Sprintf("herdr's name %q is not tab %q in lowercase", a.Name, name)
	}
	token := BashTokenFile(acfsHome, projectKey, name)
	if info, err := os.Stat(token); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Sprintf("no Bash token for %s in the project", name)
	}
	return name, fmt.Sprintf("tab %q, herdr name %s, Bash token for %s", name, a.Name, name)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
