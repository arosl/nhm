// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/util/duration.go at commit 9d5ed977: ParseDuration and
// ParseDurationWithDefault; and internal/cli/spawn.go at the same commit:
// optionalDurationValue; see LICENSE in this directory and PROVENANCE.md.
// Local changes (nhm-2p6.4.2, nhm-2p6.4.14): this header; package and
// imports; unexported names; a bare number logs no deprecation warning,
// as ntm's robot calls discard their diagnostics (suppressRobotDiagnostics);
// the stagger bound is core/spawn's MaxStaggerDelay.
package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/arosl/nhm/internal/core/spawn"
)

// optionalDurationValue is a duration flag that may stand alone: spawn's
// --stagger enables the default (90s), --stagger=2m a duration, and
// --stagger=0 turns it off.
type optionalDurationValue struct {
	defaultDuration time.Duration
	duration        *time.Duration
	enabled         *bool
}

func newOptionalDurationValue(defaultDur time.Duration, dur *time.Duration, enabled *bool) *optionalDurationValue {
	*dur = defaultDur // Set default
	return &optionalDurationValue{
		defaultDuration: defaultDur,
		duration:        dur,
		enabled:         enabled,
	}
}

func (v *optionalDurationValue) String() string {
	if v.duration != nil && *v.enabled {
		return v.duration.String()
	}
	return ""
}

func (v *optionalDurationValue) Set(s string) error {
	*v.enabled = true
	if s == "" {
		*v.duration = v.defaultDuration
		return nil
	}
	// Handle "0" as disable
	if s == "0" {
		*v.enabled = false
		*v.duration = 0
		return nil
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	if dur < 0 {
		return fmt.Errorf("stagger duration cannot be negative")
	}
	if dur > spawn.MaxStaggerDelay {
		return fmt.Errorf("stagger duration cannot exceed %s", spawn.MaxStaggerDelay)
	}
	*v.duration = dur
	return nil
}

func (v *optionalDurationValue) Type() string {
	return "duration"
}

// IsBoolFlag allows --stagger without =value
func (v *optionalDurationValue) IsBoolFlag() bool {
	return true
}

// NoOptDefVal is the default when --stagger is used without a value
func (v *optionalDurationValue) NoOptDefVal() string {
	return v.defaultDuration.String()
}

// parseNTMDuration parses human-friendly duration strings.
// Supports: 30s, 5m, 1h, 1d, 1w and standard Go durations (e.g., 1h30m).
func parseNTMDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}

	unit := s[len(s)-1]
	value, err := strconv.Atoi(s[:len(s)-1])
	if err != nil {
		// Not a simple unit, try standard Go duration
		return time.ParseDuration(s)
	}

	switch unit {
	case 's':
		return time.Duration(value) * time.Second, nil
	case 'm':
		return time.Duration(value) * time.Minute, nil
	case 'h':
		return time.Duration(value) * time.Hour, nil
	case 'd':
		return time.Duration(value) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	default:
		// Try standard Go duration as fallback
		return time.ParseDuration(s)
	}
}

// parseNTMDurationWithDefault parses a duration string, handling bare
// numbers with a default unit for backward compatibility.
func parseNTMDurationWithDefault(s string, defaultUnit time.Duration) (time.Duration, error) {
	// Try standard parse first
	if d, err := parseNTMDuration(s); err == nil {
		return d, nil
	}

	// Try bare number
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s (use units like 30s, 5m, 1h)", s)
	}
	return time.Duration(n) * defaultUnit, nil
}
