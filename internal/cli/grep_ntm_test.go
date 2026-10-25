package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// grep searches every pane but the shells, prints ntm's lines with the
// context around each match and "--" between panes, then ntm's total.
func TestGrepMatchesWithContext(t *testing.T) {
	s, _, reads := paneServer(t, copyText)
	code, out, errOut := run("grep", "ERROR", "swarm", "-C", "1", "--socket", s.SocketPath)
	want := "swarm/IcyKnoll opus:1- icy one\n" +
		"swarm/IcyKnoll opus:2: ERROR icy\n" +
		"swarm/IcyKnoll opus:3- ```go\n" +
		"--\n" +
		"swarm/BlueLake gpt:1- blue one\n" +
		"swarm/BlueLake gpt:2: ERROR blue\n" +
		"swarm/BlueLake gpt:3- \n" +
		"\n2 matches in 3 pane(s)\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("grep = %d %q\n%s\nwant\n%s", code, errOut, out, want)
	}
	got := reads()
	if len(got) != 3 || got[0] != (paneRead{"w1:p4", 1000}) || got[1].pane != "w1:p1" || got[2].pane != "w1:p2" {
		t.Errorf("reads = %v, want 1000 lines of each agent pane, not the shell", got)
	}
	// -A or -B sets -C to 0: their own sides only; -n sets the lines read.
	code, out, _ = run("grep", "ERROR icy", "swarm", "-C", "5", "-A", "1", "-n", "50", "--socket", s.SocketPath)
	if code != 0 || out != "swarm/IcyKnoll opus:2: ERROR icy\nswarm/IcyKnoll opus:3- ```go\n\n1 matches in 3 pane(s)\n" {
		t.Errorf("-C 5 -A 1 = %d\n%s", code, out)
	}
	if got := reads(); len(got) != 3 || got[0].lines != 50 {
		t.Errorf("-n 50 reads = %v", got)
	}
	code, out, _ = run("grep", "ERROR icy", "swarm", "-B1", "--socket", s.SocketPath)
	if code != 0 || out != "swarm/IcyKnoll opus:1- icy one\nswarm/IcyKnoll opus:2: ERROR icy\n\n1 matches in 3 pane(s)\n" {
		t.Errorf("-B1 = %d\n%s", code, out)
	}
	// -i ignores case; combined short flags parse; --cc keeps the claude
	// panes, the unbound one too.
	code, out, _ = run("grep", "error", "swarm", "-iC0", "--cc", "--socket", s.SocketPath)
	if code != 0 || out != "swarm/IcyKnoll opus:2: ERROR icy\n\n1 matches in 2 pane(s)\n" {
		t.Errorf("-iC0 --cc = %d\n%s", code, out)
	}
	// -v keeps the lines that do not match.
	code, out, _ = run("grep", ".", "swarm", "-v", "--cod", "--socket", s.SocketPath)
	if code != 0 || out != "swarm/BlueLake gpt:3: \n\n1 matches in 1 pane(s)\n" {
		t.Errorf("-v --cod = %d\n%s", code, out)
	}
}

// -l lists the matching panes, sorted, as text or as ntm's JSON.
func TestGrepFilesWithMatches(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	code, out, _ := run("grep", "ERROR", "swarm", "-l", "--socket", s.SocketPath)
	if code != 0 || out != "swarm/BlueLake gpt\nswarm/IcyKnoll opus\n" {
		t.Errorf("-l = %d %q", code, out)
	}
	code, out, _ = run("grep", "ERROR", "swarm", "-l", "--json", "--socket", s.SocketPath)
	if code != 0 || out != "{\n  \"count\": 2,\n  \"matching_panes\": [\n    \"swarm/BlueLake gpt\",\n    \"swarm/IcyKnoll opus\"\n  ]\n}\n" {
		t.Errorf("-l --json = %d %q", code, out)
	}
	code, out, _ = run("grep", "nothing", "swarm", "-l", "--socket", s.SocketPath)
	if code != 0 || out != "No matching panes found\n" {
		t.Errorf("-l, no match = %d %q", code, out)
	}
	code, out, _ = run("grep", "nothing", "swarm", "-l", "--json", "--socket", s.SocketPath)
	if code != 0 || out != "{\n  \"count\": 0,\n  \"matching_panes\": []\n}\n" {
		t.Errorf("-l --json, no match = %d %q", code, out)
	}
}

// --all searches every workspace, by session name, and names the session
// "<all>".
func TestGrepAllSessions(t *testing.T) {
	s, _, _ := paneServer(t, func(pane string) string {
		if pane == "w2:p1" {
			return "ERROR other\n"
		}
		return copyText(pane)
	})
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		snap := paneSnapshot()
		snap["workspaces"] = append(snap["workspaces"].([]any), map[string]any{"workspace_id": "w2", "number": 2, "label": "alpha",
			"focused": false, "pane_count": 1, "tab_count": 1, "active_tab_id": "w2:t1", "agent_status": "idle"})
		snap["panes"] = append(snap["panes"].([]any), testPane("w2:p1", "w2:t1", "claude", "Other", ""))
		return map[string]any{"type": "session_snapshot", "snapshot": snap}, nil
	})
	code, out, _ := run("grep", "^ERROR", "--all", "--json", "--socket", s.SocketPath)
	var r GrepResult
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("--all = %d %q", code, out)
	}
	if r.Session != "<all>" || r.MatchCount != 3 || r.PaneCount != 4 || len(r.Matches) != 3 ||
		r.Matches[0].Session != "alpha" || r.Matches[0].PaneID != "w2:p1" || r.Matches[1].Session != "swarm" {
		t.Errorf("--all = %+v", r)
	}
}

// No match is not an error: ntm's line, exit 0.
func TestGrepNoMatchExitsZero(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	code, out, errOut := run("grep", "nothing here", "swarm", "--socket", s.SocketPath)
	if code != 0 || errOut != "" || out != "No matches found for pattern 'nothing here'\n" {
		t.Errorf("grep = %d %q %q", code, out, errOut)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"("}, "Error: invalid pattern: error parsing regexp: missing closing ): `(`\n"},
		{nil, "Error: accepts between 1 and 2 arg(s), received 0\n"},
		{[]string{"a", "nosuch"}, "Error: session \"nosuch\" not found (available: swarm)\n"},
	} {
		code, out, errOut := run(append(append([]string{"grep"}, c.args...), "--socket", s.SocketPath)...)
		if code != 1 || out != "" || errOut != c.want {
			t.Errorf("grep %v = %d %q %q, want %q", c.args, code, out, errOut, c.want)
		}
	}
}

// --json prints ntm's GrepResult: the matches with their context counts,
// null when there are none, and case_insensitive only with -i.
func TestGrepJSON(t *testing.T) {
	s, _, _ := paneServer(t, copyText)
	code, out, _ := run("grep", "ERROR blue", "swarm", "-B", "1", "--json", "--socket", s.SocketPath)
	var got map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("grep = %d %q", code, out)
	}
	want := `{"match_count":1,"matches":[{"content":"ERROR blue","context":["blue one"],"context_before":1,"line":2,"pane":"BlueLake gpt","pane_id":"w1:p2","session":"swarm"}],"panes_searched":3,"pattern":"ERROR blue","session":"swarm","total_lines_searched":12}`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Errorf("JSON = %s\nwant %s", b, want)
	}
	code, out, _ = run("grep", "nothing", "swarm", "-i", "--json", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "\"matches\": null") || !strings.Contains(out, "\"case_insensitive\": true") {
		t.Errorf("no match, -i = %d %s", code, out)
	}
}
