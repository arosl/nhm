// Package core holds nhm's behaviour: scope, selection, admission, send
// and lifecycle. The CLI, hooks and UI actions call it; none of them has
// a second path.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scope is what every operation resolves before it acts (plan-v8 section
// 1): which herdr instance, which project, and which workspace.
type Scope struct {
	InstanceID     string `json:"instance_id"`
	ProjectID      string `json:"project_id"`
	ProjectRoot    string `json:"project_root"`
	MailProjectKey string `json:"mail_project_key"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
}

// ScopeRequest holds the explicit arguments and the invocation context.
// Explicit arguments win. ACFS's rule: the Agent Mail project key is the
// project root's absolute path.
type ScopeRequest struct {
	InstanceID string

	// ProjectRoot is --project; Cwd is the caller's directory (for a
	// plugin action, the captured context's cwd, never nhm's own).
	ProjectRoot string
	Cwd         string

	// Workspace is a workspace id or label given as an argument.
	// ContextWorkspaceID is HERDR_WORKSPACE_ID or the captured context.
	Workspace          string
	ContextWorkspaceID string
}

// WorkspaceResolver answers the two workspace questions scope needs;
// the herdr client implements it.
type WorkspaceResolver interface {
	// WorkspaceExists reports whether id is a live workspace id.
	WorkspaceExists(ctx context.Context, id string) (bool, error)
	// WorkspacesByLabel returns the ids of the workspaces with this label.
	WorkspacesByLabel(ctx context.Context, label string) ([]string, error)
}

// Scope errors.
var (
	ErrNoProject          = errors.New("no project: give --project or run inside a git repository")
	ErrWorkspaceNotFound  = errors.New("workspace not found")
	ErrWorkspaceAmbiguous = errors.New("workspace label is ambiguous; give the workspace id")
)

// ResolveScope resolves a request to a Scope. A workspace label that
// matches more than one workspace never selects one. A workspace is not
// required: WorkspaceID stays empty when neither an argument nor the
// context names one.
func ResolveScope(ctx context.Context, req ScopeRequest, ws WorkspaceResolver) (Scope, error) {
	root, err := projectRoot(req)
	if err != nil {
		return Scope{}, err
	}
	s := Scope{
		InstanceID:     req.InstanceID,
		ProjectID:      ProjectID(root),
		ProjectRoot:    root,
		MailProjectKey: root,
	}
	if ws == nil && (req.Workspace != "" || req.ContextWorkspaceID != "") {
		return Scope{}, errors.New("core: a workspace was named but there is no herdr connection to check it")
	}
	switch {
	case req.Workspace != "":
		if s.WorkspaceID, err = resolveWorkspace(ctx, req.Workspace, ws); err != nil {
			return Scope{}, err
		}
	case req.ContextWorkspaceID != "":
		ok, err := ws.WorkspaceExists(ctx, req.ContextWorkspaceID)
		if err != nil {
			return Scope{}, fmt.Errorf("core: workspace %s: %w", req.ContextWorkspaceID, err)
		}
		if !ok {
			return Scope{}, fmt.Errorf("%w: %s (from the invocation context)", ErrWorkspaceNotFound, req.ContextWorkspaceID)
		}
		s.WorkspaceID = req.ContextWorkspaceID
	}
	return s, nil
}

func resolveWorkspace(ctx context.Context, arg string, ws WorkspaceResolver) (string, error) {
	ok, err := ws.WorkspaceExists(ctx, arg)
	if err != nil {
		return "", fmt.Errorf("core: workspace %s: %w", arg, err)
	}
	if ok {
		return arg, nil
	}
	ids, err := ws.WorkspacesByLabel(ctx, arg)
	if err != nil {
		return "", fmt.Errorf("core: workspace %s: %w", arg, err)
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("%w: %s", ErrWorkspaceNotFound, arg)
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("%w: %q matches %s", ErrWorkspaceAmbiguous, arg, strings.Join(ids, ", "))
	}
}

// projectRoot is --project, or the git work tree that contains Cwd.
func projectRoot(req ScopeRequest) (string, error) {
	if req.ProjectRoot != "" {
		root, err := canonicalDir(req.ProjectRoot)
		if err != nil {
			return "", fmt.Errorf("core: --project %s: %w", req.ProjectRoot, err)
		}
		return root, nil
	}
	if req.Cwd == "" {
		return "", ErrNoProject
	}
	dir, err := canonicalDir(req.Cwd)
	if err != nil {
		return "", fmt.Errorf("core: %s: %w", req.Cwd, err)
	}
	for {
		// .git is a directory in a clone and a file in a worktree.
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w (from %s)", ErrNoProject, req.Cwd)
		}
		dir = parent
	}
}

func canonicalDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", real)
	}
	return real, nil
}

// ProjectID is a stable id for a project root: "proj-" and the first 16
// hex digits of the SHA-256 of its canonical path.
func ProjectID(root string) string {
	sum := sha256.Sum256([]byte(root))
	return "proj-" + hex.EncodeToString(sum[:8])
}
