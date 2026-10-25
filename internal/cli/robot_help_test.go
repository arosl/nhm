package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// --robot-help is ntm's plain-text guide, not JSON, whatever the format;
// it needs no herdr (nhm-2p6.2.6).
func TestRobotHelpIsPlainText(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	for _, args := range [][]string{{"--robot-help"}, {"--robot-help", "--robot-format=json"}, {"--json", "--robot-help"}} {
		code, out, errOut := run(args...)
		if code != 0 || errOut != "" || !strings.HasPrefix(out, "nhm AI Agent Interface\n") || strings.HasPrefix(out, "{") {
			t.Fatalf("%v = %d %q %q", args, code, out[:min(len(out), 80)], errOut)
		}
		for _, heading := range []string{"Core Commands:", "Session Operations:", "Output Formats:", "Quick Start:"} {
			if !strings.Contains(out, heading) {
				t.Errorf("%v: no %q", args, heading)
			}
		}
	}
}

// The guide names only robot flags that this nhm answers: each one it
// lists or mentions is available in --robot-capabilities, so it never
// offers a flag nhm refuses.
func TestRobotHelpNamesOnlyAnsweredFlags(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	_, out, _ := run("--robot-help")
	available := map[string]bool{}
	for _, c := range robotCapabilities("auto", time.Now()).Commands {
		if !c.Unavailable {
			available[c.Flag] = true
		}
	}
	mentioned := regexp.MustCompile(`--robot-[a-z0-9-]*[a-z0-9]`).FindAllString(out, -1)
	if len(mentioned) == 0 {
		t.Fatal("the guide names no robot flag")
	}
	for _, flag := range mentioned {
		if !available[flag] {
			t.Errorf("the guide names %s, which nhm does not answer", flag)
		}
	}
	// A section line per listed command, in ntm's layout.
	if !strings.Contains(out, "\n--robot-tail=SESSION     Capture recent output from panes.\n") ||
		!strings.Contains(out, "\n--robot-send=SESSION     Send message to panes atomically. Requires --msg=MSG.\n") {
		t.Errorf("the section lines are not ntm's layout:\n%s", out)
	}
}

// Only a bad --robot-format or --robot-verbosity fails it first, as a JSON
// INVALID_FLAG, as in ntm.
func TestRobotHelpBadFormatFailsFirst(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("NHM_ROBOT_FORMAT", "")
	t.Setenv("NHM_ROBOT_VERBOSITY", "")
	for _, args := range [][]string{{"--robot-help", "--robot-format=xml"}, {"--robot-help", "--robot-verbosity=loud"}} {
		env, code := robotRun(t, args...)
		if code != robotError || env.ErrorCode != "INVALID_FLAG" {
			t.Errorf("%v = %d %+v", args, code, env)
		}
	}
}
