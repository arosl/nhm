// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/session/resolve_test.go (TestResolveExplicitSessionName) and
// internal/cli/util_test.go (TestOrderSessionsForSelection,
// TestInferSessionFromCWD_LabelDisambiguation); see LICENSE in this
// directory and PROVENANCE.md. Local changes: sessions are nhm's Session
// values; the cwd and the project directories are arguments, not ntm's
// config and a chdir, so the remote case is gone; plus nhm's tests of
// Resolve.
package session

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func named(names ...string) []Session {
	out := make([]Session, 0, len(names))
	for i, n := range names {
		out = append(out, Session{Name: n, WorkspaceID: "w" + string(rune('1'+i))})
	}
	return out
}

func TestResolveExplicitSessionName(t *testing.T) {
	sessions := named("alpha", "beta", "my_project", "my_test")

	cases := []struct {
		name        string
		input       string
		allowPrefix bool
		sessions    []Session
		want        string
		wantReason  string
		wantErr     ErrorKind
		errContains []string
	}{
		{
			name:        "exact match",
			input:       "my_project",
			allowPrefix: true,
			sessions:    sessions,
			want:        "my_project",
			wantReason:  "exact match",
		},
		{
			name:        "exact match wins over prefix",
			input:       "my",
			allowPrefix: false,
			sessions:    named("my", "my_project"),
			want:        "my",
			wantReason:  "exact match",
		},
		{
			name:        "unique prefix",
			input:       "alp",
			allowPrefix: true,
			sessions:    sessions,
			want:        "alpha",
			wantReason:  "prefix match",
		},
		{
			name:        "ambiguous prefix",
			input:       "my",
			allowPrefix: true,
			sessions:    sessions,
			wantErr:     ErrorAmbiguous,
			errContains: []string{"matches multiple sessions", "my_project", "my_test"},
		},
		{
			name:        "prefix disabled",
			input:       "alp",
			allowPrefix: false,
			sessions:    sessions,
			wantErr:     ErrorNotFound,
			errContains: []string{"not found", "available"},
		},
		{
			name:        "no match",
			input:       "zzz",
			allowPrefix: true,
			sessions:    sessions,
			wantErr:     ErrorNotFound,
			errContains: []string{"not found", "available", "alpha", "beta"},
		},
		{
			name:        "no sessions",
			input:       "anything",
			allowPrefix: true,
			sessions:    nil,
			wantErr:     ErrorNoSessions,
			errContains: []string{"no herdr sessions running"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolved, reason, err := ResolveExplicitSessionName(tc.input, tc.sessions, tc.allowPrefix)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				var re *ResolveExplicitSessionNameError
				if !errors.As(err, &re) {
					t.Fatalf("expected ResolveExplicitSessionNameError, got %T", err)
				}
				if re.Kind != tc.wantErr {
					t.Fatalf("kind %q, want %q", re.Kind, tc.wantErr)
				}
				for _, substr := range tc.errContains {
					if !strings.Contains(err.Error(), substr) {
						t.Fatalf("expected error to contain %q, got %q", substr, err.Error())
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != tc.want {
				t.Fatalf("resolved %q, want %q", resolved, tc.want)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestOrderSessionsForSelection(t *testing.T) {
	sessions := []Session{
		{Name: "beta", Attached: false},
		{Name: "alpha", Attached: true},
		{Name: "gamma", Attached: false},
	}

	ordered := OrderForSelection(sessions)

	// Attached sessions come first.
	if !ordered[0].Attached {
		t.Errorf("expected attached session first, got %q (attached=%v)", ordered[0].Name, ordered[0].Attached)
	}
	if ordered[0].Name != "alpha" {
		t.Errorf("expected 'alpha' first (attached), got %q", ordered[0].Name)
	}

	// The others are sorted by name.
	if ordered[1].Name != "beta" || ordered[2].Name != "gamma" {
		t.Errorf("expected non-attached sorted: beta, gamma; got %q, %q", ordered[1].Name, ordered[2].Name)
	}

	// The argument is not changed.
	if sessions[0].Name != "beta" {
		t.Errorf("original slice was mutated: sessions[0].Name = %q, want 'beta'", sessions[0].Name)
	}
}

func TestInferSessionFromCWD_LabelDisambiguation(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "myproject")
	at := func(names ...string) []Session {
		out := named(names...)
		for i := range out {
			if strings.HasPrefix(out[i].Name, "myproject") {
				out[i].ProjectDirs = []string{projectDir}
			}
		}
		return out
	}

	t.Run("single_match_returns_it", func(t *testing.T) {
		got, reason, _ := inferFromCwd(at("myproject", "other"), projectDir)
		if got.Name != "myproject" {
			t.Errorf("got %q, want %q", got.Name, "myproject")
		}
		if reason != "current directory" {
			t.Errorf("reason = %q, want %q", reason, "current directory")
		}
	})

	t.Run("single_labeled_match_returns_it", func(t *testing.T) {
		got, reason, _ := inferFromCwd(at("myproject--frontend", "other"), projectDir)
		if got.Name != "myproject--frontend" {
			t.Errorf("got %q, want %q", got.Name, "myproject--frontend")
		}
		if reason != "current directory" {
			t.Errorf("reason = %q, want %q", reason, "current directory")
		}
	})

	t.Run("tier1_unlabeled_preferred_over_labeled", func(t *testing.T) {
		got, reason, _ := inferFromCwd(at("myproject--frontend", "myproject", "myproject--backend"), projectDir)
		if got.Name != "myproject" {
			t.Errorf("got %q, want %q (unlabeled should be preferred)", got.Name, "myproject")
		}
		if !strings.Contains(reason, "base session preferred") {
			t.Errorf("reason = %q, want containing %q", reason, "base session preferred")
		}
	})

	t.Run("tier2_all_labeled_picks_first_alphabetically", func(t *testing.T) {
		got, reason, _ := inferFromCwd(at("myproject--frontend", "myproject--backend", "myproject--api"), projectDir)
		if got.Name != "myproject--api" {
			t.Errorf("got %q, want %q (first alphabetically)", got.Name, "myproject--api")
		}
		if !strings.Contains(reason, "first labeled session") {
			t.Errorf("reason = %q, want containing %q", reason, "first labeled session")
		}
	})

	t.Run("no_matching_sessions", func(t *testing.T) {
		if got, _, ok := inferFromCwd(at("other-project", "something-else"), projectDir); ok {
			t.Errorf("got %q, want no match", got.Name)
		}
	})
}

// list is a Lister over fixed sessions.
func list(sessions ...Session) Lister {
	return func(context.Context) ([]Session, error) { return sessions, nil }
}

func TestResolveSessionPrefix(t *testing.T) {
	ss := named("alpha", "beta")
	r, err := Resolve(context.Background(), "al", list(ss...), Options{})
	if err != nil || r.Session != "alpha" || r.WorkspaceID != "w1" || r.Reason != "prefix match" || r.Inferred {
		t.Fatalf("prefix: %+v, %v", r, err)
	}
	// JSON takes no prefix, as ntm's --json does.
	if _, err := Resolve(context.Background(), "al", list(ss...), Options{JSON: true}); err == nil ||
		err.Error() != `session "al" not found (available: alpha, beta)` {
		t.Fatalf("JSON prefix: %v", err)
	}
	// A workspace id matches exactly.
	if r, err := Resolve(context.Background(), "w2", list(ss...), Options{JSON: true}); err != nil || r.Session != "beta" {
		t.Fatalf("workspace id: %+v, %v", r, err)
	}
}

// ntm's name check applies, but an exact label comes first: a workspace
// that herdr labelled with a dot is still found by its label.
func TestResolveSessionNameCheck(t *testing.T) {
	ss := []Session{{Name: "my.proj", WorkspaceID: "w1"}, {Name: "beta", WorkspaceID: "w2"}}
	if r, err := Resolve(context.Background(), "my.proj", list(ss...), Options{}); err != nil || r.WorkspaceID != "w1" {
		t.Fatalf("exact dotted label: %+v, %v", r, err)
	}
	if _, err := Resolve(context.Background(), "my.p", list(ss...), Options{}); err == nil ||
		err.Error() != "invalid session name: session name cannot contain '.'" {
		t.Fatalf("dotted prefix: %v", err)
	}
	if _, err := Resolve(context.Background(), "a b", list(ss...), Options{}); err == nil ||
		!strings.Contains(err.Error(), "contains invalid characters") {
		t.Fatalf("space: %v", err)
	}
}

// A label that two workspaces share matches neither; the error names the
// workspace ids, which do match.
func TestResolveSessionSharedLabel(t *testing.T) {
	ss := []Session{{Name: "swarm", WorkspaceID: "w2"}, {Name: "swarm", WorkspaceID: "w1"}, {Name: "other", WorkspaceID: "w3"}}
	for _, input := range []string{"swarm", "sw"} {
		_, err := Resolve(context.Background(), input, list(ss...), Options{})
		var re *ResolveExplicitSessionNameError
		if !errors.As(err, &re) || re.Kind != ErrorAmbiguous || !strings.Contains(err.Error(), "w1, w2") {
			t.Errorf("%s: %v", input, err)
		}
	}
}

func TestResolveSessionCurrent(t *testing.T) {
	ss := named("alpha", "beta")
	r, err := Resolve(context.Background(), "", list(ss...), Options{Current: "w2"})
	if err != nil || r.Session != "beta" || r.Reason != "current herdr workspace" || !r.Inferred {
		t.Fatalf("%+v, %v", r, err)
	}
	// A current workspace that is gone falls through, as ntm's does.
	if _, err := Resolve(context.Background(), "", list(ss...), Options{Current: "w9"}); err == nil {
		t.Fatal("a gone current workspace with two sessions resolved one")
	}
}

func TestResolveSessionFromCwd(t *testing.T) {
	root := t.TempDir()
	ss := named("alpha", "beta")
	ss[1].ProjectDirs = []string{filepath.Join(root, "x"), filepath.Join(root, "proj")}
	r, err := Resolve(context.Background(), "", list(ss...), Options{Cwd: filepath.Join(root, "proj", "sub")})
	if err != nil || r.Session != "beta" || r.Reason != "current directory" || !r.Inferred {
		t.Fatalf("project dir: %+v, %v", r, err)
	}
	// The cwd's base name, when no project holds it.
	r, err = Resolve(context.Background(), "", list(ss...), Options{Cwd: filepath.Join(root, "alpha")})
	if err != nil || r.Session != "alpha" || r.Reason != "current directory name" {
		t.Fatalf("base name: %+v, %v", r, err)
	}
}

func TestResolveSessionOnlyOne(t *testing.T) {
	r, err := Resolve(context.Background(), "", list(named("alpha")...), Options{Cwd: t.TempDir()})
	if err != nil || r.Session != "alpha" || r.Reason != "only running session" || !r.Inferred {
		t.Fatalf("%+v, %v", r, err)
	}
	if _, err := Resolve(context.Background(), "", list(), Options{}); err == nil ||
		err.Error() != "no herdr sessions found. Create one with: nhm spawn <name>" {
		t.Fatalf("none: %v", err)
	}
}

func TestResolveSessionErrorListsSessions(t *testing.T) {
	ss := named("_fixture", "beta", "alpha")
	picker := func(context.Context, []Session) (Session, bool, error) {
		t.Fatal("the picker ran")
		return Session{}, false, nil
	}
	want := "session name required: 3 sessions are running and there's no interactive terminal to show a selector — specify one explicitly (e.g. `alpha`). Available: _fixture, alpha, beta"
	for _, o := range []Options{{Picker: picker}, {Interactive: true, JSON: true, Picker: picker}, {Interactive: true}} {
		if _, err := Resolve(context.Background(), "", list(ss...), o); err == nil || err.Error() != want {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestResolveSessionPicker(t *testing.T) {
	ss := named("beta", "alpha")
	ss[0].Attached = true
	var shown []string
	picker := func(_ context.Context, offered []Session) (Session, bool, error) {
		for _, s := range offered {
			shown = append(shown, s.Name)
		}
		return offered[1], true, nil
	}
	r, err := Resolve(context.Background(), "", list(ss...), Options{Interactive: true, Picker: picker})
	if err != nil || r.Session != "alpha" || r.WorkspaceID != "w2" || r.Reason != "selected from list" || !r.Prompted {
		t.Fatalf("%+v, %v", r, err)
	}
	if strings.Join(shown, ",") != "beta,alpha" {
		t.Errorf("the picker showed %v; the attached session goes first", shown)
	}
}

// A cancelled picker resolves no session and no error: the verb does
// nothing and exits 0.
func TestResolveSessionPickerCancel(t *testing.T) {
	picker := func(context.Context, []Session) (Session, bool, error) { return Session{}, false, nil }
	r, err := Resolve(context.Background(), "", list(named("alpha", "beta")...), Options{Interactive: true, Picker: picker})
	if err != nil || r.Session != "" || r.Reason != "cancelled" || !r.Prompted {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestExplainIfInferred(t *testing.T) {
	var b bytes.Buffer
	Resolution{Session: "alpha", Reason: "only running session", Inferred: true}.ExplainIfInferred(&b, false)
	if b.String() != "Using session \"alpha\" (only running session)\n" {
		t.Errorf("text: %q", b.String())
	}
	b.Reset()
	Resolution{Session: "alpha", Reason: "only running session", Inferred: true}.ExplainIfInferred(&b, true)
	Resolution{Session: "alpha", Reason: "exact match"}.ExplainIfInferred(&b, false)
	if b.Len() != 0 {
		t.Errorf("JSON or a named session wrote %q", b.String())
	}
}
