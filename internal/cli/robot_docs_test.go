package cli

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// --robot-docs is ntm's: a topic's content, the topic index for an empty
// value, a bare flag taking the next word as its topic, INVALID_FLAG (exit
// 1) for an unknown topic. It needs no herdr (nhm-2p6.2.7).
func TestRobotDocsTopics(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	env, code := robotRun(t, "--robot-docs=")
	if code != 0 || len(env.Raw["topics"].([]any)) != 4 || env.Raw["topic"] != "" || env.Raw["version"] != Version {
		t.Fatalf("index = %d %v", code, env.Raw)
	}
	for _, args := range [][]string{{"--robot-docs=quickstart"}, {"--robot-docs", "quickstart"}} {
		env, code := robotRun(t, args...)
		if code != 0 || env.Raw["topic"] != "quickstart" || env.Raw["content"] == nil {
			t.Fatalf("%v = %d %v", args, code, env.Raw)
		}
	}
	env, code = robotRun(t, "--robot-docs=nope")
	if code != robotError || env.ErrorCode != "INVALID_FLAG" || env.Raw["topic"] != "nope" ||
		!strings.HasPrefix(env.Hint, "unknown topic: nope.") {
		t.Fatalf("unknown topic = %d %v", code, env.Raw)
	}
}

// The commands topic lists only the robot flags that --robot-capabilities
// offers, so it never names one that nhm refuses; the exit codes topic
// states nhm's rule.
func TestRobotDocsNameOnlyAnsweredFlags(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	env, code := robotRun(t, "--robot-docs=commands")
	if code != 0 {
		t.Fatalf("commands = %d %v", code, env.Raw)
	}
	available := map[string]bool{}
	for _, c := range robotCapabilities("auto", time.Now()).Commands {
		if !c.Unavailable {
			available[c.Flag] = true
		}
	}
	sections := env.Raw["content"].(map[string]any)["sections"].([]any)
	listed := 0
	for _, s := range sections {
		for _, line := range strings.Split(s.(map[string]any)["body"].(string), "\n") {
			flag := regexp.MustCompile(`^--robot-[a-z0-9-]*[a-z0-9]`).FindString(line)
			if !available[flag] {
				t.Errorf("the commands topic lists %q, which nhm does not answer", line)
			}
			listed++
		}
	}
	if listed != len(available) {
		t.Errorf("the commands topic lists %d flags, --robot-capabilities offers %d", listed, len(available))
	}
	env, _ = robotRun(t, "--robot-docs=exit-codes")
	codes := env.Raw["content"].(map[string]any)["exit_codes"].([]any)
	for i, want := range []int{robotOK, robotError, robotNotImplemented} {
		if got := codes[i].(map[string]any)["code"].(float64); int(got) != want {
			t.Errorf("exit code %d = %v, want %d", i, got, want)
		}
	}
}
