package actions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/arosl/nhm/internal/core/lifecycle"
	"github.com/arosl/nhm/internal/core/wake"
	"github.com/arosl/nhm/internal/ui"
)

// Params of the workspace handlers.
const (
	// ParamConfirm is kill's confirmation: the workspace's label, typed.
	// It is not part of the review: leave it out of the review key.
	ParamConfirm = "confirm"
	// ParamIdle is reap's idle limit in minutes (nhm reap --idle);
	// default 60.
	ParamIdle = "idle_minutes"
	// ParamOnly is reap's reviewed set, binding@generation,… (OnlyParam).
	ParamOnly = "only"
	// ParamLabel and ParamProject are new's workspace label and project
	// directory (default: the context's directory).
	ParamLabel   = "label"
	ParamProject = "project"
	// ParamWorkspace is the workspace focus focuses (its id).
	ParamWorkspace = "workspace"
)

// Errors of the workspace handlers.
var (
	// ErrConfirm: kill's typed label is not the workspace's.
	ErrConfirm = errors.New("actions: the typed label is not the workspace's")
	// ErrNothingToReap: a reap confirm with no reviewed agent.
	ErrNothingToReap = errors.New("actions: the review would retire no agent: nothing to reap")
)

// DefaultIdle is reap's idle limit when the form gives none.
const DefaultIdle = 60 * time.Minute

// KillLabel is what the operator types to confirm a kill of c's
// workspace: its label, else its id.
func KillLabel(c ui.Context) string {
	if l := strings.TrimSpace(c.WorkspaceLabel); l != "" {
		return l
	}
	return c.WorkspaceID
}

// runKill is kill's review (ParamDryRun: the checks) and kill (the typed
// label in ParamConfirm, then the kill as one operation under the
// review's id).
func runKill(ctx context.Context, core Core, req Request) (Result, error) {
	review := on(req.Params, ParamDryRun)
	kreq := lifecycle.KillRequest{WorkspaceID: req.Context.WorkspaceID, NoBeads: on(req.Params, ParamNoBeads), Yes: !review}
	if !review {
		if strings.TrimSpace(req.Params[ParamConfirm]) != KillLabel(req.Context) {
			return Result{Status: "refused"}, fmt.Errorf("%w: type %q to kill it", ErrConfirm, KillLabel(req.Context))
		}
		kreq.OperationID = req.OperationID
	}
	res, err := core.Kill(ctx, kreq)
	out := Result{OperationID: res.OperationID, Lines: res.Refusals}
	for _, a := range res.Agents {
		tr := TargetResult{Target: ui.Target{BindingID: a.BindingID, PaneID: a.PaneID}, Name: a.MailName}
		switch {
		case len(a.Refusals) > 0:
			tr.Status, tr.Reason = "refused", strings.Join(a.Refusals, "; ")
		case a.DryRun:
			tr.Status, tr.Reason = "checked", strings.Join(a.Actions, "; ")
		case a.Done:
			tr.Status = "retired"
		default:
			tr.Status = "not retired"
		}
		out.Targets = append(out.Targets, tr)
	}
	switch {
	case len(res.Refusals) > 0:
		out.Status = "refused"
	case review && (err == nil || errors.Is(err, lifecycle.ErrRefused)):
		// Without Yes, kill refuses only for the yes: the checks passed.
		out.Status, err = "checked", nil
	case res.Closed:
		out.Status = "closed"
	default:
		out.Status = "failed"
	}
	return out, err
}

// OnlyParam is ParamOnly's value for a reviewed set.
func OnlyParam(ts []lifecycle.ReapTarget) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.BindingID + "@" + strconv.FormatInt(t.Generation, 10)
	}
	return strings.Join(parts, ",")
}

// parseOnly reads ParamOnly; every entry is binding@generation.
func parseOnly(s string) ([]lifecycle.ReapTarget, error) {
	out := []lifecycle.ReapTarget{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		id, gen, ok := strings.Cut(strings.TrimSpace(part), "@")
		g, err := strconv.ParseInt(gen, 10, 64)
		if !ok || id == "" || err != nil || g < 1 {
			return nil, fmt.Errorf("%w: reap's reviewed set: %q is not binding@generation", ErrTargets, part)
		}
		out = append(out, lifecycle.ReapTarget{BindingID: id, Generation: g})
	}
	return out, nil
}

// runReap is reap's review (ParamDryRun: the dry run) and reap (exactly
// the reviewed set of ParamOnly, by binding and generation). Each item's
// status is reap's action: would_retire, retired, kept or refused.
func runReap(ctx context.Context, core Core, req Request) (Result, error) {
	idle := DefaultIdle
	if s := req.Params[ParamIdle]; s != "" {
		m, err := strconv.Atoi(s)
		if err != nil || m < 0 {
			return Result{}, fmt.Errorf("actions: reap's idle limit %q is not a number of minutes", s)
		}
		idle = time.Duration(m) * time.Minute
	}
	review := on(req.Params, ParamDryRun)
	rreq := lifecycle.ReapRequest{Idle: idle, WorkspaceID: req.Context.WorkspaceID, DryRun: review}
	if !review {
		only, err := parseOnly(req.Params[ParamOnly])
		if err != nil {
			return Result{}, err
		}
		if len(only) == 0 {
			return Result{Status: "refused"}, ErrNothingToReap
		}
		rreq.Only = only
	}
	items, err := core.Reap(ctx, rreq, req.OperationID)
	out := Result{OperationID: req.OperationID}
	retired, other := 0, 0
	for _, it := range items {
		out.Targets = append(out.Targets, TargetResult{Target: ui.Target{BindingID: it.BindingID, Generation: it.Generation,
			PaneID: it.PaneID}, Name: it.MailName, Status: it.Action, Reason: it.Reason})
		if it.Action == "retired" {
			retired++
		} else {
			other++
		}
	}
	switch {
	case review:
		out.Status = "reviewed"
	case err != nil:
		out.Status = "failed"
	case other == 0:
		out.Status = "done"
	case retired == 0:
		out.Status = "refused"
	default:
		out.Status = "partial"
	}
	return out, err
}

// runWake runs one manual wake pass for the picked agents: each agent's
// decision, with its reason.
func runWake(ctx context.Context, core Core, req Request) (Result, error) {
	rep, err := core.Wake(ctx, req.Targets, req.OperationID)
	out := Result{OperationID: req.OperationID, Warnings: rep.Warnings}
	nudged := 0
	for _, d := range rep.Decisions {
		status := d.Action
		if d.Outcome != "" {
			status += " (" + strings.ReplaceAll(d.Outcome, "_", " ") + ")"
		}
		if d.Action == wake.ActionNudged {
			nudged++
		}
		out.Targets = append(out.Targets, TargetResult{Target: targetOf(req.Targets, d.BindingID, d.PaneID), Name: d.MailName,
			Status: status, Reason: d.Reason})
	}
	for _, name := range rep.Unbound {
		out.Lines = append(out.Lines, name+": no active binding; nhm cannot nudge it")
	}
	switch {
	case err != nil:
		out.Status = "failed"
	case len(rep.Decisions) == 0:
		out.Status = "no pending mail"
	case nudged == 0:
		out.Status = "skipped"
	default:
		out.Status = "nudged"
	}
	return out, err
}

// runZoom focuses the picked agent's workspace and zooms its pane. It
// changes no agent, so it needs no review.
func runZoom(ctx context.Context, core Core, req Request) (Result, error) {
	t := req.Targets[0]
	pane, err := core.Zoom(ctx, t)
	if err != nil {
		return Result{Status: "refused", Targets: []TargetResult{{Target: t, Status: "refused", Reason: err.Error()}}}, err
	}
	return Result{Status: "zoomed", Targets: []TargetResult{{Target: t, Name: pane, Status: "zoomed"}},
		Lines: []string{"zoomed " + pane + "; close the menu to see it"}}, nil
}

// runAttach focuses the workspace of ParamWorkspace. It changes no agent.
func runAttach(ctx context.Context, core Core, req Request) (Result, error) {
	ws := strings.TrimSpace(req.Params[ParamWorkspace])
	if ws == "" {
		return Result{Status: "refused"}, errors.New("actions: pick the workspace to focus")
	}
	if err := core.Focus(ctx, ws); err != nil {
		return Result{Status: "failed"}, err
	}
	return Result{Status: "focused", Lines: []string{"focused " + ws + "; close the menu to see it"}}, nil
}

// ValidLabel checks a workspace label for new and spawn: not empty, at
// most 128 characters, not a flag, no control character.
func ValidLabel(label string) error {
	label = strings.TrimSpace(label)
	switch {
	case label == "":
		return errors.New("give the workspace a label")
	case utf8.RuneCountInString(label) > 128:
		return errors.New("the label is longer than 128 characters")
	case strings.HasPrefix(label, "-"):
		return errors.New("a label must not start with '-'")
	case strings.IndexFunc(label, unicode.IsControl) >= 0:
		return errors.New("the label holds a control character")
	}
	return nil
}

// runNew creates a workspace with ParamLabel, its cwd ParamProject (the
// context's directory when not given).
func runNew(ctx context.Context, core Core, req Request) (Result, error) {
	label, project := strings.TrimSpace(req.Params[ParamLabel]), strings.TrimSpace(req.Params[ParamProject])
	if project == "" {
		project = req.Context.Cwd
	}
	if err := ValidLabel(label); err != nil {
		return Result{Status: "refused"}, err
	}
	if !filepath.IsAbs(project) {
		return Result{Status: "refused"}, errors.New("give the project as an absolute directory")
	}
	project = filepath.Clean(project)
	ws, err := core.New(ctx, label, project, req.OperationID)
	if err != nil {
		return Result{OperationID: req.OperationID, Status: "failed"}, err
	}
	return Result{OperationID: req.OperationID, Status: "created",
		Lines: []string{fmt.Sprintf("created workspace %s (%s) in %s", label, ws, project)}}, nil
}
