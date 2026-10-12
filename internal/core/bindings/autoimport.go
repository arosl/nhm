package bindings

import "context"

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
// agent is importable.

// AutoImportResult is what an automatic import did.
type AutoImportResult struct {
	Imported int      `json:"imported"`
	Warnings []string `json:"warnings"`
}

// AutoImport runs a dry run of req and, when an agent is importable, the
// import. The caller holds the owner lock shared.
func AutoImport(ctx context.Context, env Env, projectOf ProjectOf, req ImportRequest) (AutoImportResult, error) {
	res := AutoImportResult{Warnings: []string{}}
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
