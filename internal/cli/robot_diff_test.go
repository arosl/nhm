package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/robot"
)

// errFakeGit is git's failure outside a repo, for fakeGitStatus.
var errFakeGit = errors.New("not a git repository")

// fakeGitStatus makes diffGitStatus answer status (or fail with err) and
// records the folders it was asked about.
func fakeGitStatus(t *testing.T, status string, err error) *[]string {
	t.Helper()
	var dirs []string
	old := diffGitStatus
	t.Cleanup(func() { diffGitStatus = old })
	diffGitStatus = func(_ context.Context, dir string) (string, error) {
		dirs = append(dirs, dir)
		return status, err
	}
	return &dirs
}

// --robot-diff lists each pane's activity and the files git reports in the
// working directory; a file changed in the window while several panes
// were active may conflict, and so does one with no tracked change.
func TestRobotDiff(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	dir := t.TempDir()
	t.Chdir(dir)
	now := time.Now()
	for name, mtime := range map[string]time.Time{"a.go": now, "b.go": now.Add(-2 * time.Hour)} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, name), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	dirs := fakeGitStatus(t, " M a.go\n?? b.go\n", nil)
	code, out, _ := run("--robot-diff=swarm", "--socket", s.SocketPath)
	var got robot.DiffOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Success || got.Session != "swarm" {
		t.Fatalf("--robot-diff = %d %s", code, out)
	}
	if wd, _ := os.Getwd(); len(*dirs) != 1 || (*dirs)[0] != wd {
		t.Errorf("git status in %q, want the working directory %q", *dirs, wd)
	}
	tf := got.Timeframe
	since, err1 := time.Parse(time.RFC3339, tf.SinceTS)
	at, err2 := time.Parse(time.RFC3339, tf.CapturedAt)
	if tf.Since != "15m0s" || err1 != nil || err2 != nil || at.Sub(since) != 15*time.Minute || !strings.HasSuffix(tf.CapturedAt, "Z") {
		t.Errorf("timeframe = %+v", tf)
	}
	want := []robot.DiffAgentInfo{
		{Pane: "user@host:/p", AgentType: "user", State: "idle", OutputLines: 0},
		{Pane: "loose", AgentType: "claude", State: "idle", OutputLines: 1},
		{Pane: "IcyKnoll opus", AgentType: "claude", State: "idle", OutputLines: 6},
		{Pane: "BlueLake gpt", AgentType: "codex", State: "idle", OutputLines: 2},
	}
	if !slices.Equal(got.AgentActivity, want) {
		t.Errorf("activity = %+v, want %+v", got.AgentActivity, want)
	}
	f := got.Files
	if !slices.Equal(f.Modified, []string{"a.go", "b.go"}) || f.Clean == nil || len(f.Clean) != 0 || len(f.PotentialConflicts) != 2 {
		t.Fatalf("files = %+v", f)
	}
	if c := f.PotentialConflicts[0]; c.File != "a.go" || c.Reason != "concurrent_activity" || c.Confidence != 0.9 ||
		!slices.Equal(c.LikelyModifiers, []string{"w1:p3", "w1:p4", "w1:p1", "w1:p2"}) {
		t.Errorf("a.go = %+v", c)
	}
	if c := f.PotentialConflicts[1]; c.File != "b.go" || c.Reason != "unclaimed_modification" || c.Confidence != 0.6 {
		t.Errorf("b.go = %+v", c)
	}
	if !strings.Contains(out, `"likely_modifiers": []`) {
		t.Errorf("b.go's modifiers are not []: %s", out)
	}
	h := got.AgentHints
	if h == nil || h.Summary != "swarm activity: 2 files modified, 4 agents" ||
		!slices.Equal(h.ConflictWarnings, []string{"2 potential conflict(s) detected"}) ||
		!slices.Equal(h.SuggestedActions, []string{"Review conflicts before committing"}) {
		t.Errorf("hints = %+v", h)
	}
	if r := reads(); len(r) != 4 || r[0].lines != 100 {
		t.Errorf("reads = %v, want 100 lines of each pane", r)
	}
}

// Outside a git repo, nhm says so in the hints, as ntm does; the window
// is --diff-since, else --since, as a duration, a number of minutes or a
// past RFC 3339 time.
func TestRobotDiffWindowAndGitFailure(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	fakeGitStatus(t, "", errFakeGit)
	for _, tc := range []struct {
		args  []string
		since string
	}{
		{[]string{"--since=10"}, "10m0s"},
		{[]string{"--since=1h"}, "1h0m0s"},
		{[]string{"--diff-since=2d", "--since=5m"}, "48h0m0s"},
		{[]string{"--since=0"}, "15m0s"},
	} {
		code, out, _ := run(append([]string{"--robot-diff=swarm", "--socket", s.SocketPath}, tc.args...)...)
		var got robot.DiffOutput
		if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Timeframe.Since != tc.since {
			t.Errorf("%v = %d %s, want since %s", tc.args, code, out, tc.since)
			continue
		}
		h := got.AgentHints
		if !slices.Equal(h.ConflictWarnings, []string{"Could not get git status", "Conflict detection incomplete"}) ||
			!slices.Equal(h.SuggestedActions, []string{"No file changes in the last " + tc.since}) ||
			len(got.Files.Modified) != 0 || got.Files.Modified == nil {
			t.Errorf("%v hints = %+v files = %+v", tc.args, h, got.Files)
		}
	}
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	code, out, _ := run("--robot-diff=swarm", "--since="+past, "--socket", s.SocketPath)
	var got robot.DiffOutput
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !strings.HasPrefix(got.Timeframe.Since, "1h0m") {
		t.Errorf("--since=%s = %d %s", past, code, out)
	}
}

// A bad or future window is INVALID_FLAG; a session nhm cannot find is
// SESSION_NOT_FOUND, before the window is read, as in ntm.
func TestRobotDiffRefusals(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	fakeGitStatus(t, "", nil)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		args       []string
		code, want string
	}{
		{[]string{"--robot-diff=swarm", "--since=soon"}, robot.ErrCodeInvalidFlag, `invalid time \"soon\": use duration like 10m or RFC3339 timestamp`},
		{[]string{"--robot-diff=swarm", "--since=" + future}, robot.ErrCodeInvalidFlag, "is in the future"},
		{[]string{"--robot-diff=nosuch", "--since=soon"}, robot.ErrCodeSessionNotFound, "session 'nosuch' not found"},
	} {
		code, out, _ := run(append(tc.args, "--socket", s.SocketPath)...)
		if code != 1 || !strings.Contains(out, `"error_code":"`+tc.code+`"`) || !strings.Contains(out, tc.want) {
			t.Errorf("%v = %d %s", tc.args, code, out)
		}
	}
}
