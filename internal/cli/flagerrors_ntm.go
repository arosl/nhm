// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/root.go (classifyRobotExecuteError, unknownFlagFromError,
// flagMisGuessHints, curatedFlagHint, flagErrorGuidance, nearestFlagName,
// commonPrefixLen, levenshteinDistance, and the flag error function that
// init sets on the root); see LICENSE in this directory and PROVENANCE.md.
// Local changes: the hints name nhm, not ntm; the flag error function is
// flagError, which the root gets from newRootCmd; classifyRobotExecuteError
// is rootErrorCode, without ntm's --robot-status, CLI-input and
// cancellation cases, which nhm's root does not raise.
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/arosl/nhm/internal/robot"
)

// flagError gives a verb's flag error a did-you-mean over the verb's own
// flags, plus curated hints for the flags agents guess most often. A root
// flag error stays as pflag wrote it.
func flagError(cmd *cobra.Command, err error) error {
	if err == nil || cmd == nil || !cmd.HasParent() {
		return err
	}
	unknown := unknownFlagFromError(err.Error())
	if unknown == "" {
		return err
	}
	// Curated hints win over the edit-distance suggester: for the known
	// mis-guesses the nearest flag is usually a red herring (--session's
	// nearest match on send is --json). A real flag one edit away wins
	// over a fuzzy curated match (flagErrorGuidance).
	curated, suggestion := flagErrorGuidance(unknown, cmd.Flags(), cmd.InheritedFlags())
	if curated != "" {
		return fmt.Errorf("%w\nHint: %s", err, curated)
	}
	if suggestion != "" {
		return fmt.Errorf("%w (did you mean --%s?)", err, suggestion)
	}
	return err
}

// rootErrorCode is the robot error code and hint of an error that reaches
// the root under --json; flags are the root's.
func rootErrorCode(err error, flags *pflag.FlagSet) (string, string) {
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"unknown flag",
		"unknown shorthand flag",
		// cobra reports a stray positional argument as "unknown command":
		// caller input, not an internal fault.
		"unknown command",
		"flag needs an argument",
		"invalid argument",
		"required flag",
		"requires at least",
		"requires at most",
		"accepts ",
	} {
		if strings.Contains(message, fragment) {
			hint := "Use 'nhm --robot-help' or 'nhm --robot-capabilities' to inspect valid flags"
			if unknown := unknownFlagFromError(err.Error()); unknown != "" {
				if curated, suggestion := flagErrorGuidance(unknown, flags); curated != "" {
					hint = curated
				} else if suggestion != "" {
					hint = fmt.Sprintf("Did you mean --%s? Otherwise use 'nhm --robot-help' or 'nhm --robot-capabilities' to inspect valid flags", suggestion)
				}
			}
			return robot.ErrCodeInvalidFlag, hint
		}
	}
	return robot.ErrCodeInternalError, "Retry the command or inspect nhm diagnostics"
}

// unknownFlagFromError extracts the flag name from pflag's
// "unknown flag: --foo" error, or "" when the message has another shape.
func unknownFlagFromError(errMsg string) string {
	const marker = "unknown flag: --"
	idx := strings.Index(errMsg, marker)
	if idx < 0 {
		return ""
	}
	unknown := strings.TrimSpace(errMsg[idx+len(marker):])
	if cut := strings.IndexAny(unknown, " \t\n"); cut >= 0 {
		unknown = unknown[:cut]
	}
	return unknown
}

// flagMisGuessHints maps flag spellings agents often guess to a hint that
// names the real spelling. It is consulted before the edit-distance
// suggester, because the fix is often not the nearest flag (--session's
// fix is to give the session as the robot flag's value).
var flagMisGuessHints = map[string]string{
	"session":     "Pass the session as the robot flag value (e.g. --robot-send=SESSION) or as the command's positional argument (e.g. nhm send SESSION \"msg\")",
	"pane":        "Use --panes=N[,N...] to target panes (robot surfaces also accept singular --pane=N with --robot-send and --robot-history)",
	"project-dir": "Use --spawn-dir=PATH with --robot-spawn, or --project=PATH with ensemble/JFP robot commands",
	"timeout":     "--timeout is a robot-surface flag (e.g. nhm --robot-wait=SESSION --timeout=2m); this command does not accept it",
	"message":     "Use --msg (or --msg-file) for robot message payloads; 'nhm send' takes the prompt as a positional argument",
	"msg":         "Use --msg with robot surfaces (e.g. --robot-send=SESSION --msg='...'); 'nhm send' takes the prompt as a positional argument",
}

// curatedFlagHint returns the curated hint for an unknown flag, and
// accepts a typo one edit away from a curated key (--sesion, --messge).
// The keys are tried in sorted order, so a tie resolves the same way each
// time. A real flag one edit away must be tried first (flagErrorGuidance).
func curatedFlagHint(unknown string) (string, bool) {
	if hint, ok := flagMisGuessHints[unknown]; ok {
		return hint, true
	}
	keys := make([]string, 0, len(flagMisGuessHints))
	for key := range flagMisGuessHints {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if levenshteinDistance(unknown, key) == 1 {
			return flagMisGuessHints[key], true
		}
	}
	return "", false
}

// flagErrorGuidance finds the best guidance for an unknown flag, in this
// order:
//  1. the exact curated hint (a known mis-guess);
//  2. a flag of these sets one edit away (a typo of a real flag beats the
//     curated redirection: a verb with --session answers --sesion with it);
//  3. the curated hint of a key one edit away;
//  4. the general edit-distance suggester.
//
// One of hint and suggestion is set on a hit; both empty means no
// confident guidance.
func flagErrorGuidance(unknown string, flagSets ...*pflag.FlagSet) (hint, suggestion string) {
	if unknown == "" {
		return "", ""
	}
	if curated, ok := flagMisGuessHints[unknown]; ok {
		return curated, ""
	}
	nearest := nearestFlagName(unknown, flagSets...)
	if nearest != "" && levenshteinDistance(unknown, nearest) <= 1 {
		return "", nearest
	}
	if curated, ok := curatedFlagHint(unknown); ok {
		return curated, ""
	}
	return "", nearest
}

// nearestFlagName returns the closest flag name across the sets within a
// small edit distance, or "" when no match is close enough.
func nearestFlagName(unknown string, flagSets ...*pflag.FlagSet) string {
	if unknown == "" {
		return ""
	}
	best := ""
	bestDist := len(unknown)/3 + 2 // the bound grows with the length
	bestPrefix := -1
	for _, fs := range flagSets {
		if fs == nil {
			continue
		}
		fs.VisitAll(func(f *pflag.Flag) {
			d := levenshteinDistance(unknown, f.Name)
			if d > bestDist {
				return
			}
			// A tie goes to the name with the longest common prefix:
			// --robot-state suggests --robot-status, not --robot-save.
			p := commonPrefixLen(unknown, f.Name)
			if d < bestDist || p > bestPrefix {
				bestDist = d
				bestPrefix = p
				best = f.Name
			}
		})
	}
	return best
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// levenshteinDistance is a plain edit distance over bytes; flag names are
// short ASCII.
func levenshteinDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
