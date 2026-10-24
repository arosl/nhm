package cli

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// --robot-spawn takes nhm's --trust-folder and passes it to the spawn, as
// spawn --trust-folder does; without it nothing is trusted. The rest of
// the request stays the robot form's (nhm-cvg).
func TestRobotSpawnTrustFolder(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--trust-folder"}, true},
		{[]string{"--trust-folder=false"}, false},
		{[]string{"--trust-folder", "--spawn-assign-work", "--spawn-prompt=Read AGENTS.md."}, true},
	} {
		fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		rf := addRobotSpawnFlags(fs)
		args := append([]string{"--robot-spawn=fresh", "--spawn-dir=/p", "--spawn-cc=1", "--spawn-safety", "--robot-format=json"}, c.args...)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		req, _, refusal := robotSpawnRequest(fs, rf)
		if refusal != nil {
			t.Fatalf("%s: %+v", strings.Join(c.args, " "), refusal)
		}
		if req.TrustFolder != c.want {
			t.Errorf("%s: TrustFolder %v, want %v", strings.Join(c.args, " "), req.TrustFolder, c.want)
		}
		if req.Session != "fresh" || !req.Safety || !req.RequireObservations || !req.NoKickoff || req.Prompt != "" ||
			len(req.Agents) != 1 || req.Agents[0].Kind != "claude" || req.Agents[0].Count != 1 {
			t.Errorf("%s: request %+v", strings.Join(c.args, " "), req)
		}
	}

	// The runner takes the flag: a dry run goes on to admission.
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	out, code := robotJSON(t, "--robot-spawn=fresh", "--spawn-dir="+t.TempDir(), "--spawn-cc=1", "--spawn-safety",
		"--trust-folder", "--robot-format=json", "--dry-run", "--socket="+s.SocketPath)
	if code != 1 || out["error_code"] != "ADMISSION_REFUSED" {
		t.Fatalf("dry run with --trust-folder = %d %v", code, out)
	}
}
