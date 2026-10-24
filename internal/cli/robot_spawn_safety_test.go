package cli

import (
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

// --robot-spawn --spawn-safety refuses a session that exists, with ntm's
// answer: INVALID_FLAG, its sentence (nhm kill in place of ntm kill, and
// no "spawn safety:" of nhm's own) and its hint. A new session goes on
// to admission (nhm-lrx).
func TestRobotSpawnSafety(t *testing.T) {
	s, _, _ := newSendInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	args := func(session string) []string {
		return []string{"--robot-spawn=" + session, "--spawn-dir=" + project, "--spawn-cc=1", "--spawn-safety",
			"--robot-format=json", "--dry-run", "--socket=" + s.SocketPath}
	}
	out, code := robotJSON(t, args("swarm")...)
	if code != 1 || out["success"] != false || out["error_code"] != "INVALID_FLAG" ||
		out["error"] != "session 'swarm' already exists (--spawn-safety mode prevents reuse; use 'nhm kill swarm' first)" ||
		out["hint"] != "Choose a new session name or disable --spawn-safety" {
		t.Fatalf("an existing session = %d %v", code, out)
	}
	if out, _ := robotJSON(t, args("fresh")...); out["error_code"] != "ADMISSION_REFUSED" {
		t.Fatalf("a new session = %v", out)
	}
}

// The ready wait is --spawn-timeout (or its deprecated alias
// --ready-timeout) when given, else --timeout when given, as ntm takes it
// (resolveRobotSpawnTimeout), else 60 s (nhm-lrx). A bare number is
// seconds, and a bad duration is ntm's refusal (nhm-2p6.4.2).
func TestRobotReadyWait(t *testing.T) {
	for _, c := range []struct {
		args []string
		want time.Duration
	}{
		{nil, 60 * time.Second},
		{[]string{"--timeout=90s"}, 90 * time.Second},
		{[]string{"--ready-timeout=20s"}, 20 * time.Second},
		{[]string{"--ready-timeout=20s", "--timeout=90s"}, 20 * time.Second},
		{[]string{"--spawn-timeout=15s", "--timeout=90s"}, 15 * time.Second},
		{[]string{"--timeout=45"}, 45 * time.Second},
		{[]string{"--timeout=1d"}, 24 * time.Hour},
	} {
		fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		rf := addRobotSpawnFlags(fs)
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if got, err := robotReadyWait(fs, rf); err != nil || got != c.want {
			t.Errorf("%s: ready wait %s %v, want %s", strings.Join(c.args, " "), got, err, c.want)
		}
	}
	out, code := robotJSON(t, "--robot-spawn=s", "--spawn-dir=/p", "--spawn-cc=1", "--timeout=soon")
	if code != robotError || out["error_code"] != "INVALID_FLAG" || out["error"] != "invalid duration: soon (use units like 30s, 5m, 1h)" ||
		out["hint"] != "Use a duration such as 30s or 500ms" {
		t.Fatalf("a bad duration = %d %v", code, out)
	}
}
