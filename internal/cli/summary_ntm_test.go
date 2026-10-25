package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/summary"
)

// summaryProject makes a project folder (anchored by .nhm/config.toml),
// moves the test into it, and points the archive at an empty folder.
func summaryProject(t *testing.T) (project, archive string) {
	t.Helper()
	project = t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".nhm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".nhm", "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	archive = t.TempDir()
	old := summaryArchiveDir
	t.Cleanup(func() { summaryArchiveDir = old })
	summaryArchiveDir = func() (string, error) { return archive, nil }
	return project, archive
}

// saveSummary writes a saved summary file of session at ts.
func saveSummary(t *testing.T, project, session, ts string, s summary.SessionSummary) string {
	t.Helper()
	dir := filepath.Join(project, ".nhm", "summaries")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, session+"-"+ts+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// summary reads 500 lines of each agent pane (no shell) and prints ntm's
// brief summary; --format json and --json print the summary as JSON.
func TestSummaryLive(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	summaryProject(t)
	code, stdout, stderr := run("summary", "swarm", "--socket", s.SocketPath)
	if code != 0 || stderr != "" || stdout != "Session swarm summary\nErrors: ERROR icy; ERROR blue\n" {
		t.Fatalf("summary = %d %q %q", code, stdout, stderr)
	}
	r := reads()
	if len(r) != 3 || r[0] != (paneRead{"w1:p4", 500}) || r[1].pane != "w1:p1" || r[2].pane != "w1:p2" {
		t.Errorf("reads = %v, want 500 lines of each agent pane", r)
	}
	for _, args := range [][]string{{"--format", "json"}, {"--json"}} {
		code, stdout, _ = run(append([]string{"summary", "swarm", "--socket", s.SocketPath}, args...)...)
		var got summary.SessionSummary
		if code != 0 || json.Unmarshal([]byte(stdout), &got) != nil || got.Session != "swarm" || got.Format != "brief" ||
			len(got.Errors) != 2 || got.Text != "Session swarm summary\nErrors: ERROR icy; ERROR blue" ||
			got.GeneratedAt.Location() != time.UTC || !strings.Contains(stdout, "\n  \"session\"") {
			t.Errorf("%v = %d %s", args, code, stdout)
		}
	}
	code, stdout, _ = run("summary", "swarm", "--format", "md", "--socket", s.SocketPath)
	if code != 0 || stdout != "## Session Summary: swarm\n\n## Errors\n- ERROR icy\n- ERROR blue\n" {
		t.Errorf("markdown = %d %q", code, stdout)
	}
	code, stdout, _ = run("summary", "swarm", "--format", "handoff", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(stdout, "goal: Session summary\n") || !strings.Contains(stdout, "status: blocked\n") ||
		!strings.Contains(stdout, "outcome: PARTIAL_MINUS\n") {
		t.Errorf("handoff = %d %q", code, stdout)
	}
}

// The session archive's last record from before --since marks where the
// window starts in a pane's read text, as in ntm.
func TestSummarySinceTrimsByArchive(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	_, archive := summaryProject(t)
	rec := func(pane, content string, at time.Time) string {
		b, _ := json.Marshal(map[string]any{"session": "swarm", "pane": pane, "agent": "cc", "timestamp": at, "content": content})
		return string(b) + "\n"
	}
	old := time.Now().Add(-2 * time.Hour)
	data := rec("cc_1", "icy one\nERROR icy\n", old) + rec("cc_1", "later\n", time.Now())
	if err := os.WriteFile(filepath.Join(archive, "swarm_2026-10-25.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := run("summary", "swarm", "--since", "1h", "--socket", s.SocketPath)
	if code != 0 || stdout != "Session swarm summary\nErrors: ERROR blue\n" {
		t.Errorf("trimmed = %d %q", code, stdout)
	}
}

// Saved summaries: a named session with one shows it; --recent shows the
// newest; --all lists them (newest first); with no herdr, a call that
// names no session shows the newest.
func TestSummarySaved(t *testing.T) {
	project, _ := summaryProject(t)
	t.Setenv("HERDR_SOCKET_PATH", "")
	code, stdout, _ := run("summary", "--all")
	if code != 0 || stdout != "No summaries found.\n" {
		t.Errorf("--all empty = %d %q", code, stdout)
	}
	if code, stdout, _ = run("summary", "--all", "--json"); code != 0 || stdout != "[]\n" {
		t.Errorf("--all --json empty = %d %q", code, stdout)
	}
	if code, _, stderr := run("summary", "--recent"); code != 1 || stderr != "Error: no summaries found\n" {
		t.Errorf("--recent empty = %d %q", code, stderr)
	}
	older := saveSummary(t, project, "swarm", "20261025-010203", summary.SessionSummary{Session: "swarm", Format: "brief",
		Accomplishments: []string{"Fixed the parser"}})
	newer := saveSummary(t, project, "other", "20261025-020000", summary.SessionSummary{Session: "other", Format: "brief",
		Text: "kept text"})
	code, stdout, _ = run("summary", "--all")
	want := "Available summaries (brief):\n  other - 2026-10-25T02:00:00Z (" + newer + ")\n  swarm - 2026-10-25T01:02:03Z (" + older + ")\n"
	if code != 0 || stdout != want {
		t.Errorf("--all = %d %q, want %q", code, stdout, want)
	}
	if code, stdout, _ = run("summary", "swa"); code != 0 || stdout != "Session swarm summary\nAccomplishments: Fixed the parser\n" {
		t.Errorf("saved swarm = %d %q", code, stdout)
	}
	if code, stdout, _ = run("summary", "--recent"); code != 0 || stdout != "Session other summary\n" {
		t.Errorf("--recent = %d %q", code, stdout)
	}
	if code, stdout, _ = run("summary", "--recent", "swarm", "--format", "json"); code != 0 || !strings.Contains(stdout, `"accomplishments": [`) {
		t.Errorf("--recent swarm json = %d %q", code, stdout)
	}
	if code, stdout, _ = run("summary"); code != 0 || stdout != "Session other summary\n" {
		t.Errorf("no herdr fallback = %d %q", code, stdout)
	}
	if code, _, stderr := run("summary", "nosuch"); code != 1 || !strings.HasPrefix(stderr, "Error: no herdr socket") {
		t.Errorf("no herdr, unknown session = %d %q", code, stderr)
	}
}

// --regenerate builds a summary from the archive and saves it, named in
// UTC; with no archive it says so.
func TestSummaryRegenerate(t *testing.T) {
	project, archive := summaryProject(t)
	t.Setenv("HERDR_SOCKET_PATH", "")
	if code, _, stderr := run("summary", "--regenerate"); code != 1 || stderr != "Error: no archived output found\n" {
		t.Errorf("no archive = %d %q", code, stderr)
	}
	b, _ := json.Marshal(map[string]any{"session": "swarm", "pane": "cc_1", "agent": "cc", "timestamp": time.Now().Add(-3 * time.Hour),
		"content": "Implemented the summary verb\n"})
	if err := os.WriteFile(filepath.Join(archive, "swarm_2026-10-25.jsonl"), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run("summary", "--regenerate", "--since", "1h"); code != 1 ||
		stderr != "Error: no archived output found for session \"swarm\" within the --since window\n" {
		t.Errorf("--since window = %d %q", code, stderr)
	}
	code, stdout, _ := run("summary", "--regenerate")
	if code != 0 || stdout != "Session swarm summary\nAccomplishments: Implemented the summary verb\n" {
		t.Fatalf("--regenerate = %d %q", code, stdout)
	}
	entries, err := os.ReadDir(filepath.Join(project, ".nhm", "summaries"))
	stamp := regexp.MustCompile(`^swarm-(\d{8}-\d{6})\.json$`)
	if err != nil || len(entries) != 1 || !stamp.MatchString(entries[0].Name()) {
		t.Fatalf("saved = %v %v", entries, err)
	}
	at, err := time.Parse("20060102-150405", stamp.FindStringSubmatch(entries[0].Name())[1])
	if err != nil || time.Since(at).Abs() > time.Minute {
		t.Errorf("file time %v is not UTC now", at)
	}
}

// ntm's refusals: --recent with --regenerate, a bad --format, a bad
// --since.
func TestSummaryRefusals(t *testing.T) {
	summaryProject(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--recent", "--regenerate"}, "Error: --recent and --regenerate cannot be used together\n"},
		{[]string{"--format", "xml"}, "Error: invalid --format \"xml\" (expected text, json, markdown, detailed, or handoff)\n"},
		{[]string{"--since", "soon"}, "Error: invalid --since: invalid duration: soon (use units like 30s, 5m, 1h)\n"},
	} {
		if code, _, stderr := run(append([]string{"summary"}, tc.args...)...); code != 1 || stderr != tc.want {
			t.Errorf("%v = %d %q, want %q", tc.args, code, stderr, tc.want)
		}
	}
}
