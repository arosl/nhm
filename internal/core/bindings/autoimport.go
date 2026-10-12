package bindings

import (
	"context"
	"os"
	"path/filepath"

	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/agentmail"
)

// The automatic import (ACFS acfs-9hkh.41): nhm keeps its bindings of
// Bash-spawned agents current without a person running `nhm admin recover
// reconcile --import-only`, so its read verbs see the agents that `acfs
// agents` sees while the Bash owns the instance.
//
// It is ImportOnly, with its rules: verified evidence only, no rename,
// nothing written in herdr or in the Bash's state, so any owner may run
// it. Read verbs never run it, because plan-v8 section 1 gives reads no
// operation id. It runs where a write is already allowed:
//
//   - after `nhm admin instance enroll`, for every pane (ACFS's install
//     and acfs update run enroll);
//   - from the hooks (nhm-hook), for the hook's pane, while nhm does not
//     own the instance: a Bash-spawned agent's own SessionStart, and
//     herdr's agent events for its pane, which come again when its state
//     changes, so evidence the Bash writes after the start is read then.
//
// A dry run comes first, so an operation row is claimed only when an
// agent is importable. For one pane (the hook path), a cheap check comes
// before the dry run: herdr's agent events come for every agent, and most
// panes without evidence would cost a full session snapshot each time.

// AutoImportResult is what an automatic import did.
type AutoImportResult struct {
	Imported int      `json:"imported"`
	Warnings []string `json:"warnings"`
}

// AutoImport runs a dry run of req and, when an agent is importable, the
// import. The caller holds the owner lock shared.
func AutoImport(ctx context.Context, env Env, projectOf ProjectOf, req ImportRequest) (AutoImportResult, error) {
	res := AutoImportResult{Warnings: []string{}}
	if req.PaneID != "" && !mayHaveEvidence(ctx, env, projectOf, req) {
		return res, nil
	}
	req.DryRun = true
	dry, err := ImportOnly(ctx, env, projectOf, req)
	if err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, dry.Warnings...)
	importable := false
	for _, it := range dry.Items {
		importable = importable || it.Action == ActionWouldImport
	}
	if !importable {
		return res, nil
	}
	req.DryRun = false
	rep, err := ImportOnly(ctx, env, projectOf, req)
	res.Imported = rep.Imported
	res.Warnings = append(res.Warnings, rep.Warnings...)
	return res, err
}

// agentGetter is herdr's call for one agent, cheaper than a snapshot.
type agentGetter interface {
	AgentGet(ctx context.Context, target string) (herdr.AgentInfo, error)
}

// mayHaveEvidence reports whether the agent in req.PaneID can pass
// importOne's evidence check, from one agent.get and a few file reads:
// an Agent Mail identity file for the pane, or a Bash token whose name is
// herdr's name for the agent. It accepts every pane that importOne could
// import, so a pane it refuses would be skipped anyway. When it cannot
// tell (no agent.get, a herdr error, no working directory), it accepts
// and the full check decides.
func mayHaveEvidence(ctx context.Context, env Env, projectOf ProjectOf, req ImportRequest) bool {
	g, ok := env.Herdr.(agentGetter)
	if !ok {
		return true
	}
	a, err := g.AgentGet(ctx, req.PaneID)
	if err != nil {
		return true
	}
	cwd := firstNonEmpty(a.ForegroundCwd, a.Cwd)
	if a.Agent == "" || cwd == "" {
		return a.Agent != ""
	}
	projectKey, _, err := projectOf(ctx, cwd)
	if err != nil {
		return false
	}
	// The import's candidate keys: the work tree's, and the folder's own
	// (ImportRequest.ProjectAt).
	keys := []string{projectKey}
	if req.ProjectAt != nil {
		if k, _, err := req.ProjectAt(ctx, cwd); err == nil && k != projectKey {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if req.ProjectKey == "" || k == req.ProjectKey {
			if keyHasEvidence(env, req, a, k) {
				return true
			}
		}
	}
	return false
}

// keyHasEvidence reports whether the identity file or a Bash token for
// the pane's alias may exist under project key k (true when it cannot
// tell).
func keyHasEvidence(env Env, req ImportRequest, a herdr.AgentInfo, k string) bool {
	if _, ok, err := agentmail.ReadIdentity(env.ConfigHome, k, req.PaneID); ok || err != nil {
		return true
	}
	if a.Name == "" {
		return false
	}
	acfsHome := req.AcfsHome
	if acfsHome == "" {
		acfsHome = DefaultAcfsHome()
	}
	entries, err := os.ReadDir(filepath.Dir(BashTokenFile(acfsHome, k, "_")))
	if err != nil {
		return !os.IsNotExist(err)
	}
	for _, e := range entries {
		if alias, err := Alias(e.Name()); err == nil && alias == a.Name {
			return true
		}
	}
	return false
}
