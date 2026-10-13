package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// ru's spawn (nhm-hir): `spawn S --cc=1 --no-user`. ntm's --no-user keeps
// no pane for the user; nhm keeps none, so the flag parses and changes
// nothing.
func TestSpawnNoUser(t *testing.T) {
	s, _, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	spawnAgentMail(t, "GreenPike")
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAll(t, filepath.Join(home, ".acfs/onboard/docs/ntm/command_palette.md"),
		"## A\n### default_new_agent | Default\nRead AGENTS.md.\n")
	t.Setenv("PATH", t.TempDir())
	code, out, errOut := run("spawn", "swarm", "--cc=1", "--no-user", "--project", t.TempDir(), "--allow-unguarded", "--dry-run", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "would start 1 agents in swarm (w1)") {
		t.Fatalf("--no-user = %d %s %s", code, out, errOut)
	}
}
