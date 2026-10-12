package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeWorkspaces struct {
	ids    map[string]bool
	labels map[string][]string
	err    error
}

func (f fakeWorkspaces) WorkspaceExists(_ context.Context, id string) (bool, error) {
	return f.ids[id], f.err
}

func (f fakeWorkspaces) WorkspacesByLabel(_ context.Context, label string) ([]string, error) {
	return f.labels[label], f.err
}

func repo(t *testing.T) (root, sub string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub = filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

var ws = fakeWorkspaces{
	ids:    map[string]bool{"wH": true, "wA": true, "wB": true},
	labels: map[string][]string{"agentic_coding_flywheel_setup": {"wH"}, "twin": {"wA", "wB"}},
}

func TestProjectFromCwdAndFlag(t *testing.T) {
	ctx := context.Background()
	root, sub := repo(t)
	s, err := ResolveScope(ctx, ScopeRequest{InstanceID: "inst-1", Cwd: sub}, ws)
	if err != nil {
		t.Fatal(err)
	}
	if s.ProjectRoot != root || s.MailProjectKey != root || s.ProjectID != ProjectID(root) || s.InstanceID != "inst-1" || s.WorkspaceID != "" {
		t.Fatalf("scope = %+v", s)
	}
	// --project wins over cwd and need not be a git repository.
	other := t.TempDir()
	s, err = ResolveScope(ctx, ScopeRequest{ProjectRoot: other, Cwd: sub}, ws)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(other); s.ProjectRoot != want {
		t.Fatalf("ProjectRoot = %q, want %q", s.ProjectRoot, want)
	}
	// A git worktree has a .git file.
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s, err := ResolveScope(ctx, ScopeRequest{Cwd: wt}, nil); err != nil || s.ProjectRoot == "" {
		t.Fatalf("worktree scope = %+v, %v", s, err)
	}
}

func TestNoProject(t *testing.T) {
	ctx := context.Background()
	for _, req := range []ScopeRequest{{}, {Cwd: "/"}} {
		if _, err := ResolveScope(ctx, req, ws); !errors.Is(err, ErrNoProject) {
			t.Errorf("%+v: err = %v", req, err)
		}
	}
	if _, err := ResolveScope(ctx, ScopeRequest{ProjectRoot: "/nonexistent/x"}, ws); err == nil {
		t.Error("a missing --project was accepted")
	}
}

func TestProjectIDIsStable(t *testing.T) {
	if ProjectID("/a") != ProjectID("/a") || ProjectID("/a") == ProjectID("/b") || len(ProjectID("/a")) != len("proj-")+16 {
		t.Fatalf("ProjectID: %s %s", ProjectID("/a"), ProjectID("/b"))
	}
}

func TestWorkspaceResolution(t *testing.T) {
	ctx := context.Background()
	_, sub := repo(t)
	cases := []struct {
		name string
		req  ScopeRequest
		want string
		err  error
	}{
		{"id", ScopeRequest{Workspace: "wH"}, "wH", nil},
		{"unique label", ScopeRequest{Workspace: "agentic_coding_flywheel_setup"}, "wH", nil},
		{"ambiguous label", ScopeRequest{Workspace: "twin"}, "", ErrWorkspaceAmbiguous},
		{"unknown", ScopeRequest{Workspace: "nope"}, "", ErrWorkspaceNotFound},
		{"context", ScopeRequest{ContextWorkspaceID: "wA"}, "wA", nil},
		{"argument wins over context", ScopeRequest{Workspace: "wH", ContextWorkspaceID: "wA"}, "wH", nil},
		{"stale context", ScopeRequest{ContextWorkspaceID: "wGone"}, "", ErrWorkspaceNotFound},
	}
	for _, c := range cases {
		c.req.Cwd = sub
		s, err := ResolveScope(ctx, c.req, ws)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("%s: err = %v, want %v", c.name, err, c.err)
			}
			continue
		}
		if err != nil || s.WorkspaceID != c.want {
			t.Errorf("%s: scope = %+v, %v", c.name, s, err)
		}
	}
	if _, err := ResolveScope(ctx, ScopeRequest{Cwd: sub, Workspace: "wH"}, nil); err == nil {
		t.Error("a workspace without a resolver was accepted")
	}
	broken := fakeWorkspaces{err: errors.New("socket gone")}
	if _, err := ResolveScope(ctx, ScopeRequest{Cwd: sub, Workspace: "wH"}, broken); err == nil {
		t.Error("a resolver error was swallowed")
	}
}
