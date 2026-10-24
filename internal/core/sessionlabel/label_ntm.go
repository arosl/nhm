// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/config/label.go at commit 9d5ed977: labelRegex,
// SessionLabelSeparator, ParseSessionLabel, FormatSessionName, HasLabel,
// SessionBase, ValidateLabel, ValidateProjectName and SessionLabel; see
// LICENSE in this directory and PROVENANCE.md. Local changes
// (nhm-2p6.4.2): this header; the package; the pane-title helpers are not
// taken.

// Package sessionlabel is ntm's session-label grammar: a session is a
// project name, or "<project>--<label>" for one more session of the same
// project (spawn -l, --robot-spawn --spawn-label, add -l).
package sessionlabel

import (
	"fmt"
	"regexp"
	"strings"
)

// labelRegex validates label strings: must start with alphanumeric,
// then alphanumeric, dash, or underscore.
var labelRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// SessionLabelSeparator is the delimiter between a session's base project
// name and its optional label.
const SessionLabelSeparator = "--"

// ParseSessionLabel splits a session name into base project name and
// optional label. The separator is the LAST occurrence of "--": labels are
// validated to never contain "--" (ValidateLabel), while base project names
// come from directory names that may contain it, so FormatSessionName(base,
// label) round-trips for every valid label.
//
// Returns (base, label) where label is empty if no "--" present.
//
// Examples:
//
//	ParseSessionLabel("myproject")             -> ("myproject", "")
//	ParseSessionLabel("my-project--frontend")  -> ("my-project", "frontend")
//	ParseSessionLabel("foo--bar--baz")         -> ("foo--bar", "baz")
func ParseSessionLabel(sessionName string) (base, label string) {
	idx := strings.LastIndex(sessionName, SessionLabelSeparator)
	if idx < 0 {
		return sessionName, ""
	}
	return sessionName[:idx], sessionName[idx+len(SessionLabelSeparator):]
}

// FormatSessionName constructs a labeled session name from base and label.
// If label is empty, returns base unchanged.
func FormatSessionName(base, label string) string {
	if label == "" {
		return base
	}
	return base + SessionLabelSeparator + label
}

// HasLabel returns true if the session name contains a label separator.
func HasLabel(sessionName string) bool {
	return strings.Contains(sessionName, SessionLabelSeparator)
}

// SessionBase returns just the base project name, stripping any label.
//
// Examples:
//
//	SessionBase("myproject")            -> "myproject"
//	SessionBase("myproject--frontend")  -> "myproject"
//	SessionBase("my-project--frontend") -> "my-project"
func SessionBase(sessionName string) string {
	base, _ := ParseSessionLabel(sessionName)
	return base
}

// ValidateLabel checks if a label string is valid.
// Rules:
//   - Must start with alphanumeric character
//   - Can contain alphanumeric, dash, underscore
//   - Must not be empty
//   - Must not exceed 50 characters
//   - Must not itself contain "--" (the separator)
func ValidateLabel(label string) error {
	if label == "" {
		return fmt.Errorf("label must not be empty")
	}
	if len(label) > 50 {
		return fmt.Errorf("label must not exceed 50 characters (got %d)", len(label))
	}
	if strings.Contains(label, SessionLabelSeparator) {
		return fmt.Errorf("label must not contain '--' (reserved as separator)")
	}
	if !labelRegex.MatchString(label) {
		return fmt.Errorf("label must start with alphanumeric and contain only alphanumeric, dash, underscore")
	}
	return nil
}

// ValidateProjectName checks that a project name does not contain the
// reserved label separator "--". Project names with "--" would be ambiguous
// when combined with labels (e.g., "my--project--frontend" is unparseable).
func ValidateProjectName(project string) error {
	if strings.Contains(project, SessionLabelSeparator) {
		return fmt.Errorf("project name %q contains '--' which is reserved as the session-label separator", project)
	}
	return nil
}

// SessionLabel extracts the label from a session name, or empty string if
// unlabeled.
//
// Examples:
//
//	SessionLabel("myproject")            -> ""
//	SessionLabel("myproject--frontend")  -> "frontend"
func SessionLabel(sessionName string) string {
	_, label := ParseSessionLabel(sessionName)
	return label
}
