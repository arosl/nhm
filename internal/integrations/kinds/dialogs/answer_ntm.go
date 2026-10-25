// Ported from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/dialogs.go (optionChoicePattern, dialogOptionKeys,
// resolveDialogAnswer, verifyCursorDialogSelection and
// adaptDialogKeysForAgent) at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: the package; the names that
// callers use are exported (ResolveAnswer, VerifyCursorSelection,
// AdaptKeys). The keys are ntm's tmux key names, which the answer reports;
// HerdrKey gives herdr's name for each.

package dialogs

import (
	"fmt"
	"regexp"
	"strings"
)

// The choices of --robot-answer-dialog, besides option-K.
const (
	ChoiceDecline    = "decline"
	ChoiceExtraUsage = "extra-usage"
	ChoiceDismiss    = "dismiss"
)

var optionChoicePattern = regexp.MustCompile(`^option-(\d+)$`)

// optionKeys translates a displayed option into the provider's input
// protocol. Never guess a cursor's initial position or follow an immediate
// hotkey with Enter: that Enter could act on an entirely different screen.
func optionKeys(state State, number int) ([]string, error) {
	picked, selected := -1, -1
	for i, option := range state.Options {
		if option.Number == number {
			picked = i
		}
		if option.Selected {
			if selected != -1 {
				return nil, fmt.Errorf("ambiguous dialog selection; re-inspect the pane")
			}
			selected = i
		}
	}
	if picked == -1 {
		return nil, fmt.Errorf("dialog has no option %d (options: %s)", number, DescribeOptions(state.Options))
	}
	option := state.Options[picked]
	if state.Class == DestructiveConfirm && !declineSideLabel(option.Label) {
		return nil, fmt.Errorf("POLICY_REFUSED: refusing option %d: %q on a destructive confirm; only decline-side answers are allowed", number, option.Label)
	}
	switch state.InputMode {
	case "", "numbered":
		return []string{fmt.Sprintf("%d", number), "Enter"}, nil
	case "hotkey":
		if len(option.Key) != 1 || !strings.ContainsAny(option.Key, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return nil, fmt.Errorf("option %d has no usable displayed hotkey", number)
		}
		for i, other := range state.Options {
			if i != picked && strings.EqualFold(other.Key, option.Key) {
				return nil, fmt.Errorf("ambiguous dialog hotkey %q", option.Key)
			}
		}
		return []string{option.Key}, nil
	case "cursor":
		if selected == -1 {
			return nil, fmt.Errorf("dialog cursor is not visible; re-inspect before answering")
		}
		var keys []string
		for selected > picked {
			keys = append(keys, "Up")
			selected--
		}
		for selected < picked {
			keys = append(keys, "Down")
			selected++
		}
		return append(keys, "Enter"), nil
	default:
		return nil, fmt.Errorf("unsupported dialog input mode %q", state.InputMode)
	}
}

// ResolveAnswer maps a choice to concrete keys given the classified
// dialog, enforcing the destructive-confirm policy. Pure for testing.
func ResolveAnswer(state State, choice string) ([]string, error) {
	choice = strings.TrimSpace(strings.ToLower(choice))
	if state.Class == None {
		return nil, fmt.Errorf("no dialog detected on the pane")
	}
	if m := optionChoicePattern.FindStringSubmatch(choice); m != nil {
		num := 0
		fmt.Sscanf(m[1], "%d", &num)
		return optionKeys(state, num)
	}
	switch choice {
	case ChoiceDecline:
		for _, opt := range state.Options {
			if declineSideLabel(opt.Label) {
				return optionKeys(state, opt.Number)
			}
		}
		if state.Class == UsageOverlay || state.Class == PasteLimbo {
			return []string{"Escape"}, nil
		}
		return nil, fmt.Errorf("no decline-side option found (options: %s)", DescribeOptions(state.Options))
	case ChoiceExtraUsage:
		if state.Class != RateLimitOptions {
			return nil, fmt.Errorf("extra-usage only applies to rate_limit_options dialogs (found %s)", state.Class)
		}
		for _, opt := range state.Options {
			if strings.Contains(strings.ToLower(opt.Label), "extra") {
				return optionKeys(state, opt.Number)
			}
		}
		return nil, fmt.Errorf("no extra-usage option found (options: %s)", DescribeOptions(state.Options))
	case ChoiceDismiss:
		// Escape is the universal dismiss; never selects an option.
		return []string{"Escape"}, nil
	default:
		return nil, fmt.Errorf("unknown choice %q; use decline, extra-usage, dismiss, or option-K", choice)
	}
}

// VerifyCursorSelection is the final precondition for Enter on a cursor
// menu. Navigation keys can be dropped or a different modal can replace the
// original; neither case permits accepting whatever happens to be selected.
func VerifyCursorSelection(before, current State, choice string) error {
	if before.Class != current.Class || before.InputMode != current.InputMode ||
		before.Evidence != current.Evidence || len(before.Options) != len(current.Options) {
		return fmt.Errorf("dialog changed while navigating; re-inspect before answering")
	}
	for i, option := range before.Options {
		other := current.Options[i]
		if option.Number != other.Number || option.Label != other.Label || option.Key != other.Key {
			return fmt.Errorf("dialog options changed while navigating; re-inspect before answering")
		}
	}
	keys, err := ResolveAnswer(current, choice)
	if err != nil {
		return err
	}
	if len(keys) != 1 || keys[0] != "Enter" {
		return fmt.Errorf("requested option is not selected; refusing Enter on an unverified cursor")
	}
	return nil
}

// AdaptKeys rewrites a resolved answer for TUIs whose keys differ from
// the shared vocabulary. omp does not drop a staged paste on Escape (a
// single Escape with a draft is a verified no-op); one Ctrl+C clears the
// whole draft, paste token included.
func AdaptKeys(keys []string, state State, agentType string) []string {
	if agentType == "omp" && state.Class == PasteLimbo && len(keys) == 1 && keys[0] == "Escape" {
		return []string{"C-c"}
	}
	return keys
}

// HerdrKey is herdr's name for one of the answer's keys: ntm's tmux names
// Escape, Enter, Up, Down and C-c are herdr's esc, enter, up, down and
// ctrl+c; a digit or a hotkey letter is itself.
func HerdrKey(key string) string {
	switch key {
	case "Escape":
		return "esc"
	case "Enter":
		return "enter"
	case "Up":
		return "up"
	case "Down":
		return "down"
	case "C-c":
		return "ctrl+c"
	}
	return key
}
