// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/session/resolve.go (ResolveExplicitSessionNameError,
// ResolveExplicitSessionName, sessionNames), internal/cli/util.go
// (SessionResolution, ExplainIfInferredForOutput,
// ResolveSessionWithOptionsContext, inferSessionFromCWD,
// orderSessionsForSelection), internal/tmux/session.go
// (ValidateSessionName) and internal/config/label.go (HasLabel); see
// LICENSE in this directory and PROVENANCE.md. Local changes: a session
// is a herdr workspace, named by its label (or its id without one); an
// exact label or workspace id comes before ntm's name check, so a
// workspace that herdr labelled with a dot is still found, and a label
// that two workspaces share matches neither; the current session is the
// calling pane's workspace; a session's project directories are its bound
// agents' project roots, not ntm's projects_base/<session>; the session
// list, the cwd, the terminal check and the picker are the caller's;
// "tmux" and "ntm" in the texts read "herdr" and "nhm".
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Session is one session as ntm resolves it: a herdr workspace.
type Session struct {
	// Name is the workspace's label, or its id when it has none.
	Name        string
	WorkspaceID string
	// Attached is herdr's focused workspace, ntm's attached session.
	Attached bool
	// Tabs is the workspace's tab count, which ntm's picker shows as the
	// session's window count.
	Tabs int
	// ProjectDirs are the project roots of the session's bound agents.
	ProjectDirs []string
}

// Resolution is a resolved session and how it was found.
type Resolution struct {
	Session     string
	WorkspaceID string
	Reason      string
	Inferred    bool // no session was named: nhm found one, or the user picked one
	Prompted    bool // the user picked from the picker (and may have cancelled)
}

// ExplainIfInferred reports an inferred session, for human output only:
// a JSON call stays quiet.
func (r Resolution) ExplainIfInferred(w io.Writer, machineJSON bool) {
	if !r.Inferred || r.Session == "" || machineJSON {
		return
	}
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "Using session %q (%s)\n", r.Session, r.Reason)
}

// Options are the call's context.
type Options struct {
	// JSON turns off the prefix match and the picker (ntm: --json, or a
	// verb's own JSON format).
	JSON bool
	// Current is the calling pane's workspace id, ntm's current tmux
	// session; empty outside herdr.
	Current string
	// Cwd is the caller's directory.
	Cwd string
	// Interactive is true when stdin and stdout are both terminals.
	Interactive bool
	// Picker shows the sessions and returns the one the user chose; ok is
	// false when the user cancelled. Without one there is no picker.
	Picker func(ctx context.Context, sessions []Session) (s Session, ok bool, err error)
}

// Lister returns the live sessions.
type Lister func(ctx context.Context) ([]Session, error)

// Resolve resolves an optional session name, as ntm's
// ResolveSessionWithOptionsContext does. A name matches exactly, else by a
// unique prefix (not with JSON). Without a name: the current workspace,
// then the cwd, then the only session, then the picker. A cancelled picker
// returns an empty Session and no error: the verb does nothing.
func Resolve(ctx context.Context, name string, list Lister, o Options) (Resolution, error) {
	if ctx == nil {
		return Resolution{}, errors.New("session resolution context is required")
	}
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	sessions, err := list(ctx)
	if err != nil {
		return Resolution{}, err
	}
	if name != "" {
		s, reason, err := resolveExplicit(name, sessions, !o.JSON)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{Session: s.Name, WorkspaceID: s.WorkspaceID, Reason: reason}, nil
	}

	// The calling pane's workspace is the most certain signal.
	if o.Current != "" {
		for _, s := range sessions {
			if s.WorkspaceID == o.Current {
				return Resolution{Session: s.Name, WorkspaceID: s.WorkspaceID, Reason: "current herdr workspace", Inferred: true}, nil
			}
		}
	}

	if len(sessions) == 0 {
		return Resolution{}, errors.New("no herdr sessions found. Create one with: nhm spawn <name>")
	}

	if s, reason, ok := inferFromCwd(sessions, o.Cwd); ok {
		return Resolution{Session: s.Name, WorkspaceID: s.WorkspaceID, Reason: reason, Inferred: true}, nil
	}

	if len(sessions) == 1 {
		return Resolution{Session: sessions[0].Name, WorkspaceID: sessions[0].WorkspaceID, Reason: "only running session", Inferred: true}, nil
	}

	// The picker reads keys from stdin and draws on stdout, so it runs only
	// when both are terminals. Otherwise the error names the sessions.
	if o.JSON || !o.Interactive || o.Picker == nil {
		names := sessionNames(sessions)
		// A name that starts with "_" is usually internal or a test
		// fixture: a poor example to copy.
		example := names[0]
		for _, n := range names {
			if !strings.HasPrefix(n, "_") {
				example = n
				break
			}
		}
		return Resolution{}, fmt.Errorf(
			"session name required: %d sessions are running and there's no interactive terminal to show a selector — specify one explicitly (e.g. `%s`). Available: %s",
			len(names), example, strings.Join(names, ", "))
	}

	s, ok, err := o.Picker(ctx, OrderForSelection(sessions))
	if err != nil {
		return Resolution{}, err
	}
	if !ok {
		return Resolution{Reason: "cancelled", Inferred: true, Prompted: true}, nil
	}
	return Resolution{Session: s.Name, WorkspaceID: s.WorkspaceID, Reason: "selected from list", Inferred: true, Prompted: true}, nil
}

// resolveExplicit resolves a named session. nhm's step comes first: an
// exact workspace id, or a label that one workspace has. Then ntm's name
// check and ResolveExplicitSessionName.
func resolveExplicit(input string, sessions []Session, allowPrefix bool) (Session, string, error) {
	var byLabel []Session
	for _, s := range sessions {
		if s.WorkspaceID == input {
			return s, "exact match", nil
		}
		if s.Name == input {
			byLabel = append(byLabel, s)
		}
	}
	switch len(byLabel) {
	case 0:
	case 1:
		return byLabel[0], "exact match", nil
	default:
		return Session{}, "", &ResolveExplicitSessionNameError{Kind: ErrorAmbiguous, Input: input, Matches: workspaceIDs(byLabel)}
	}
	if err := ValidateName(input); err != nil {
		return Session{}, "", fmt.Errorf("invalid session name: %w", err)
	}
	name, reason, err := ResolveExplicitSessionName(input, sessions, allowPrefix)
	if err != nil {
		return Session{}, "", err
	}
	var matched []Session
	for _, s := range sessions {
		if s.Name == name {
			matched = append(matched, s)
		}
	}
	if len(matched) > 1 {
		return Session{}, "", &ResolveExplicitSessionNameError{Kind: ErrorAmbiguous, Input: input, Matches: workspaceIDs(matched)}
	}
	return matched[0], reason, nil
}

func workspaceIDs(sessions []Session) []string {
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.WorkspaceID)
	}
	sort.Strings(ids)
	return ids
}

// ErrorKind is the kind of a ResolveExplicitSessionNameError.
type ErrorKind string

const (
	ErrorNoSessions ErrorKind = "no_sessions"
	ErrorNotFound   ErrorKind = "not_found"
	ErrorAmbiguous  ErrorKind = "ambiguous"
)

// ResolveExplicitSessionNameError is ntm's error for a named session that
// matches none, or more than one.
type ResolveExplicitSessionNameError struct {
	Kind      ErrorKind
	Input     string
	Matches   []string // ambiguous
	Available []string // not_found
}

func (e *ResolveExplicitSessionNameError) Error() string {
	switch e.Kind {
	case ErrorNoSessions:
		return fmt.Sprintf("session %q not found (no herdr sessions running)", e.Input)
	case ErrorAmbiguous:
		return fmt.Sprintf(
			"session %q matches multiple sessions: %s (please be more specific)",
			e.Input,
			strings.Join(e.Matches, ", "),
		)
	default:
		return fmt.Sprintf("session %q not found (available: %s)", e.Input, strings.Join(e.Available, ", "))
	}
}

// ResolveExplicitSessionName is ntm's match of a name: exact, else a
// unique prefix when allowPrefix is set. It returns the session name.
func ResolveExplicitSessionName(input string, sessions []Session, allowPrefix bool) (string, string, error) {
	names := sessionNames(sessions)
	if len(names) == 0 {
		return "", "", &ResolveExplicitSessionNameError{
			Kind:  ErrorNoSessions,
			Input: input,
		}
	}

	for _, name := range names {
		if name == input {
			return name, "exact match", nil
		}
	}

	if !allowPrefix {
		return "", "", &ResolveExplicitSessionNameError{
			Kind:      ErrorNotFound,
			Input:     input,
			Available: names,
		}
	}

	var matches []string
	for _, name := range names {
		if strings.HasPrefix(name, input) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)

	if len(matches) == 1 {
		return matches[0], "prefix match", nil
	}

	if len(matches) > 1 {
		return "", "", &ResolveExplicitSessionNameError{
			Kind:    ErrorAmbiguous,
			Input:   input,
			Matches: matches,
		}
	}

	return "", "", &ResolveExplicitSessionNameError{
		Kind:      ErrorNotFound,
		Input:     input,
		Available: names,
	}
}

// sessionNames are the sessions' names, sorted, each once.
func sessionNames(sessions []Session) []string {
	names := make([]string, 0, len(sessions))
	seen := map[string]bool{}
	for _, s := range sessions {
		if s.Name != "" && !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

var sessionNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ValidateName is ntm's check of a session name.
func ValidateName(name string) error {
	if name == "" {
		return errors.New("session name cannot be empty")
	}
	// Targeted errors for the common confusions: ':' and '.' are tmux's
	// target separators, and '.' clashes with the pane form "0.1".
	if strings.Contains(name, ":") {
		return errors.New("session name cannot contain ':'")
	}
	if strings.Contains(name, ".") {
		return errors.New("session name cannot contain '.'")
	}
	if !sessionNameRegex.MatchString(name) {
		return fmt.Errorf("session name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", name)
	}
	return nil
}

// labelSeparator is ntm's separator of a session's base name and its
// label ("myproject--frontend").
const labelSeparator = "--"

// hasLabel is ntm's config.HasLabel.
func hasLabel(name string) bool {
	return strings.Contains(name, labelSeparator)
}

// inferFromCwd is ntm's inferSessionFromCWD: the session whose project
// directory holds the cwd, the longest match first; then the session
// named like the cwd's base name.
func inferFromCwd(sessions []Session, cwd string) (Session, string, bool) {
	if cwd == "" {
		return Session{}, "", false
	}
	cwd = filepath.Clean(cwd)

	// All the sessions that hold the cwd, at the longest match.
	var bestMatches []Session
	bestLen := 0
	for _, s := range sessions {
		dirLen := 0
		for _, d := range s.ProjectDirs {
			if d == "" {
				continue
			}
			d = filepath.Clean(d)
			if (cwd == d || strings.HasPrefix(cwd, d+string(os.PathSeparator))) && len(d) > dirLen {
				dirLen = len(d)
			}
		}
		if dirLen == 0 {
			continue
		}
		if dirLen > bestLen {
			bestMatches = []Session{s}
			bestLen = dirLen
		} else if dirLen == bestLen {
			bestMatches = append(bestMatches, s)
		}
	}
	if len(bestMatches) == 1 {
		return bestMatches[0], "current directory", true
	}
	if len(bestMatches) > 1 {
		// First, a session without a label.
		for _, m := range bestMatches {
			if !hasLabel(m.Name) {
				return m, "current directory (base session preferred)", true
			}
		}
		// All labelled: the first by name, so the answer is the same each
		// time.
		sort.Slice(bestMatches, func(i, j int) bool {
			return bestMatches[i].Name < bestMatches[j].Name
		})
		return bestMatches[0], "current directory (first labeled session)", true
	}

	// The fallback: a session named like the cwd's base name.
	base := filepath.Base(cwd)
	if base == "" || base == "." || base == string(os.PathSeparator) {
		return Session{}, "", false
	}
	matches := 0
	var match Session
	for _, s := range sessions {
		if s.Name == base {
			matches++
			match = s
		}
	}
	if matches == 1 {
		return match, "current directory name", true
	}

	return Session{}, "", false
}

// OrderForSelection is ntm's orderSessionsForSelection: attached sessions
// first, then by name. It does not change its argument.
func OrderForSelection(sessions []Session) []Session {
	ordered := make([]Session, len(sessions))
	copy(ordered, sessions)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Attached != ordered[j].Attached {
			return ordered[i].Attached
		}
		return ordered[i].Name < ordered[j].Name
	})
	return ordered
}
