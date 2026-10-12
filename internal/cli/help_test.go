package cli

import (
	"strings"
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
	caps := robotCapabilities("json", time.Now())
	available := 0
	for _, c := range caps.Commands {
		if c.Unavailable {
			continue
		}
		available++
		if !strings.Contains(out, c.Flag) {
			t.Errorf("nhm help does not list %s, which --robot-capabilities offers", c.Flag)
		}
	}
	if available == 0 {
		t.Fatal("--robot-capabilities lists no available robot flag")
	}
}
