package bindings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

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
	// ProjectAt maps a folder to the project rooted exactly there
	// (--project's rule). With it, a pane in a subfolder of a git work
	// tree is also tried under that folder's own key: the Bash keys an
	// agent spawned with --cwd there by the folder (acfs-9hkh.80). The key
	// with verified evidence wins; the work tree's key is tried first.
	ProjectAt ProjectOf `json:"-"`
	// Snapshot, when set, is the herdr state to decide on instead of a
	// fresh read: a takeover's real pass decides on what its dry check
	// saw, so herdr changing in between cannot change the plan
	// (acfs-9hkh.85).
	Snapshot *herdr.SessionSnapshot `json:"-"`
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
	// Snapshot is the herdr state the run decided on (ImportRequest's).
	Snapshot *herdr.SessionSnapshot `json:"-"`
}

// ImportOnly runs the import. The caller holds the owner lock shared; any
// owner may run it, since it writes only nhm's own state and Agent Mail's
// identity file.
func ImportOnly(ctx context.Context, env Env, projectOf ProjectOf, req ImportRequest) (ImportReport, error) {
	rep := ImportReport{DryRun: req.DryRun, Items: []ImportItem{}, Warnings: []string{}}
	if req.AcfsHome == "" {
		req.AcfsHome = DefaultAcfsHome()
	}
	rep.Snapshot = req.Snapshot
	if rep.Snapshot == nil {
		s, err := env.Herdr.Snapshot(ctx)
		if err != nil {
			return rep, fmt.Errorf("bindings: import: %w", err)
		}
		rep.Snapshot = &s
	}
	snap := *rep.Snapshot
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
		if err := ctx.Err(); err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("stopped at %s: %v; the next run takes the rest", a.PaneID, err))
			break
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
		// The status is written even when the caller's deadline has passed:
		// a run that claimed its operation must not stay 'claimed'.
		status := "done"
		if ctx.Err() != nil {
			status = "partial"
		}
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if err := env.DB.SetOperationStatus(fctx, rep.OperationID, status, true, env.now()); err != nil {
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
		if !req.DryRun {
			item.Reason = repairIdentity(env, req.AcfsHome, snap, a, b)
		}
		return item, true
	} else if !errors.Is(err, ErrNotFound) {
		return skip(err.Error())
	}
	pane, ok := findPane(snap, a.PaneID)
	if !ok {
		return skip("the pane is not in herdr's snapshot")
	}
	if board, err := boardPane(ctx, env, pane); err != nil {
		return skip("cannot check that the pane is not nhm's board: " + err.Error())
	} else if board {
		return skip("the pane is nhm's board, not an agent")
	}
	cwd := firstNonEmpty(pane.ForegroundCwd, pane.Cwd, a.ForegroundCwd, a.Cwd)
	if cwd == "" {
		return skip("herdr reports no working directory for the pane")
	}
	// The candidate projects: the work tree's, then the folder's own
	// (ProjectAt) when that differs; the first with verified evidence wins.
	type candidate struct{ key, id string }
	var cands []candidate
	projectKey, projectID, err := projectOf(ctx, cwd)
	if err != nil {
		if req.ProjectKey != "" {
			return item, false
		}
		return skip("no project: " + err.Error())
	}
	cands = append(cands, candidate{projectKey, projectID})
	if req.ProjectAt != nil {
		if k, id, err := req.ProjectAt(ctx, cwd); err == nil && k != projectKey {
			cands = append(cands, candidate{k, id})
		}
	}
	if req.ProjectKey != "" {
		var keep []candidate
		for _, c := range cands {
			if c.key == req.ProjectKey {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 {
			return item, false
		}
		cands = keep
	}

	fromFile, fromBash, why := "", "", ""
	var evidence []Evidence
	for i, c := range cands {
		file, bash, w := "", "", ""
		var ev []Evidence
		if rec, ok, err := agentmail.ReadIdentity(env.ConfigHome, c.key, a.PaneID); err != nil {
			return skip("Agent Mail identity file: " + err.Error())
		} else if ok {
			file = rec.Name
			ev = append(ev, Evidence{Kind: "identity_file", Detail: rec.Name + " for " + a.PaneID})
		}
		bash, w = bashEvidence(req.AcfsHome, c.key, snap, pane, a)
		if bash != "" {
			ev = append(ev, Evidence{Kind: "bash_spawn", Detail: w})
		}
		if i == 0 || file != "" || bash != "" {
			projectKey, projectID, fromFile, fromBash, why, evidence = c.key, c.id, file, bash, w, ev
		}
		if file != "" || bash != "" {
			break
		}
	}
	item.ProjectKey = projectKey
	item.Evidence = append(item.Evidence, evidence...)
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

// repairIdentity writes Agent Mail's identity file for an imported
// binding that has none, and returns what it did or what failed ("" for
// nothing). The import writes the file right after it creates the
// binding; a run cut off between the two leaves the binding without it,
// and every later run sees the pane as bound. It writes only while the
// Bash spawn evidence for the binding still holds: the Bash removes the
// file when it retires the agent, and that must stay so. It never
// replaces a file that exists.
func repairIdentity(env Env, acfsHome string, snap herdr.SessionSnapshot, a herdr.AgentInfo, b Binding) string {
	if b.Provenance != Imported || b.PaneID == "" {
		return ""
	}
	if _, ok, err := agentmail.ReadIdentity(env.ConfigHome, b.MailProjectKey, b.PaneID); err != nil {
		return "Agent Mail identity file: " + err.Error()
	} else if ok {
		return ""
	}
	pane, ok := findPane(snap, a.PaneID)
	if !ok {
		return ""
	}
	if name, _ := bashEvidence(acfsHome, b.MailProjectKey, snap, pane, a); !strings.EqualFold(name, b.MailName) {
		return ""
	}
	if _, warn := writeIdentity(env, b); warn != "" {
		return warn
	}
	return "wrote the missing Agent Mail identity file"
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
