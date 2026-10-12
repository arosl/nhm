package cli

import (
	"regexp"
	"testing"
	"time"
)

// Every robot flag that --robot-capabilities reports as available is in
// nhm help, so the two cannot drift (acfs-9hkh.57).
func TestHelpListsEveryAvailableRobotFlag(t *testing.T) {
	code, out, _ := run("help")
	if code != 0 {
		t.Fatalf("help = %d", code)
	}
	// Whole flags only: --robot-send must not pass on --robot-send-receipt
	// (acfs-9hkh.69).
	listed := map[string]bool{}
	for _, f := range regexp.MustCompile(`--robot-[a-z0-9-]*[a-z0-9]`).FindAllString(out, -1) {
		listed[f] = true
	}
	caps := robotCapabilities("json", time.Now())
	available := 0
	for _, c := range caps.Commands {
		if c.Unavailable {
			continue
		}
		available++
		if !listed[c.Flag] {
			t.Errorf("nhm help does not list %s, which --robot-capabilities offers", c.Flag)
		}
	}
	if available == 0 {
		t.Fatal("--robot-capabilities lists no available robot flag")
	}
}
