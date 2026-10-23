// Derived from ntm (github.com/Dicklesworthstone/ntm) at commit 9d5ed977:
// internal/cli/watch_test.go (TestParseWatchInterval,
// TestExtractBeadMentions, TestFilterPanesCanonicalizesAliases,
// TestWatchEventMatchesPattern_UsesWatchRootRelativePath,
// TestWatchEventMatchesPattern_RejectsOutsideWatchRoot) and
// internal/cli/watch_json_truth_test.go (TestEmitWatchPaneOutput_JSONFrame,
// TestEmitWatchPaneOutput_TextModeStillPrints,
// TestEmitWatchJSONFrame_Framing); see LICENSE in this directory and
// PROVENANCE.md. Local changes: nhm's agents and watchOut in place of
// tmux panes and stdout; the filter test reads kinds; plus nhm's tests of
// the verb on a herdr test server.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/core/watcher"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

func TestParseWatchInterval(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", input: "", want: 250 * time.Millisecond},
		{name: "duration", input: "2s", want: 2 * time.Second},
		{name: "milliseconds integer", input: "500", want: 500 * time.Millisecond},
		{name: "invalid", input: "abc", wantErr: true},
		{name: "zero invalid", input: "0", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWatchInterval(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWatchInterval returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractBeadMentions(t *testing.T) {
	re, err := beadMentionRegexp("bd-123")
	if err != nil {
		t.Fatalf("beadMentionRegexp error: %v", err)
	}
	input := []string{"working on bd-123 now", "noise line", "bd-1234 should not match", "Done with BD-123"}
	got := extractBeadMentions(input, re)
	if len(got) != 2 {
		t.Fatalf("mentions count = %d, want 2", len(got))
	}
	if got[0] != "working on bd-123 now" {
		t.Fatalf("first mention = %q", got[0])
	}
	if got[1] != "Done with BD-123" {
		t.Fatalf("second mention = %q", got[1])
	}
}

func TestFilterPanesCanonicalizesAliases(t *testing.T) {
	agents := []snapshot.Agent{
		{PaneID: "w1:p1", Kind: "claude", HerdrName: "cc_1"},
		{PaneID: "w1:p2", Kind: "codex", HerdrName: "cod_2"},
		{PaneID: "w1:p3", Kind: "gemini", HerdrName: "gmi_3"},
		{PaneID: "w1:p4", Kind: "agy", HerdrName: "agy_4"},
	}
	tests := []struct {
		name string
		f    watchFilter
		want []string
	}{
		{name: "claude alias", f: watchFilter{cc: true}, want: []string{"w1:p1"}},
		{name: "codex alias", f: watchFilter{cod: true}, want: []string{"w1:p2"}},
		{name: "gemini alias", f: watchFilter{gmi: true}, want: []string{"w1:p3"}},
		{name: "antigravity alias", f: watchFilter{agy: true}, want: []string{"w1:p4"}},
		{name: "multiple aliases", f: watchFilter{cc: true, gmi: true}, want: []string{"w1:p1", "w1:p3"}},
		{name: "pane wins over kinds", f: watchFilter{cod: true, pane: "gmi_3"}, want: []string{"w1:p3"}},
		{name: "no filter", f: watchFilter{}, want: []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, a := range filterWatchAgents(agents, tc.f) {
				got = append(got, a.PaneID)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("filterWatchAgents(%+v) = %v, want %v", tc.f, got, tc.want)
			}
		})
	}
}

func TestWatchEventMatchesPattern_UsesWatchRootRelativePath(t *testing.T) {
	watchRoot := filepath.Join(string(filepath.Separator), "tmp", "actual-project")
	eventPath := filepath.Join(watchRoot, "internal", "cli", "watch.go")
	if !watchEventMatchesPattern("internal/cli/*.go", watchRoot, eventPath) {
		t.Fatalf("watchEventMatchesPattern should match nested path relative to watch root")
	}
}

func TestWatchEventMatchesPattern_RejectsOutsideWatchRoot(t *testing.T) {
	watchRoot := filepath.Join(string(filepath.Separator), "tmp", "actual-project")
	eventPath := filepath.Join(string(filepath.Separator), "tmp", "other-project", "internal", "cli", "watch.go")
	if watchEventMatchesPattern("internal/cli/*.go", watchRoot, eventPath) {
		t.Fatalf("watchEventMatchesPattern should reject paths outside the watch root")
	}
}

// str is a frame's field as text: "" when it is left out.
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// parseFrames reads NDJSON: one object per line.
func parseFrames(t *testing.T, out string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var f map[string]any
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("not one JSON object per line: %v\n%s", err, out)
		}
		frames = append(frames, f)
	}
	return frames
}

func TestEmitWatchPaneOutput_JSONFrame(t *testing.T) {
	var b strings.Builder
	out := watchOut{w: &b, json: true, session: "proj", now: time.Now}
	a := snapshot.Agent{PaneID: "w1:p7", Kind: "claude", HerdrName: "proj_cc_1"}
	out.paneOutput(a, []string{"line one", "", "   ", "line two"})
	frames := parseFrames(t, b.String())
	if len(frames) != 1 {
		t.Fatalf("want exactly 1 pane_output frame, got %d (%q)", len(frames), b.String())
	}
	frame := frames[0]
	if frame["event"] != "pane_output" {
		t.Fatalf("event = %v, want pane_output", frame["event"])
	}
	if frame["session"] != "proj" || frame["pane"] != "proj_cc_1" || frame["pane_id"] != "w1:p7" || frame["agent_type"] != "cc" {
		t.Fatalf("pane identity wrong: %+v", frame)
	}
	lines, ok := frame["lines"].([]any)
	if !ok || len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("lines = %v, want the two non-blank lines", frame["lines"])
	}
	if ts, _ := frame["timestamp"].(string); ts == "" {
		t.Fatalf("timestamp missing: %+v", frame)
	}
	// Blank-only output emits nothing.
	b.Reset()
	out.paneOutput(a, []string{"", "   "})
	if got := strings.TrimSpace(b.String()); got != "" {
		t.Fatalf("blank-only output produced a frame: %q", got)
	}
}

func TestEmitWatchPaneOutput_TextModeStillPrints(t *testing.T) {
	var b strings.Builder
	out := watchOut{w: &b, noTimestamps: true, session: "proj", now: time.Now}
	out.paneOutput(snapshot.Agent{PaneID: "w1:p7", Kind: "claude", HerdrName: "proj_cc_1"}, []string{"hello text stream"})
	if b.String() != "[proj_cc_1] hello text stream\n" {
		t.Fatalf("text stream lost: %q", b.String())
	}
}

func TestEmitWatchJSONFrame_Framing(t *testing.T) {
	var b strings.Builder
	out := watchOut{w: &b, json: true, session: "s1", now: time.Now}
	out.frame(watchJSONFrame{Event: "watch_started"})
	out.beadStatus("bd-1", "in_progress", nil)
	frames := parseFrames(t, b.String())
	if len(frames) != 2 {
		t.Fatalf("want 2 frames, got %d", len(frames))
	}
	if frames[0]["event"] != "watch_started" || frames[1]["event"] != "bead_status" {
		t.Fatalf("events wrong: %+v", frames)
	}
	for i, f := range frames {
		if ts, _ := f["timestamp"].(string); ts == "" {
			t.Fatalf("frame %d missing timestamp", i)
		}
	}
	if frames[1]["status"] != "in_progress" || frames[1]["bead"] != "bd-1" || frames[1]["session"] != "s1" {
		t.Fatalf("bead_status fields wrong: %+v", frames[1])
	}
}

// watchFake is a herdr test server for watch: workspace w1 ("swarm") with
// IcyKnoll (claude, w1:p1) and BlueLake (codex, w1:p2), bound in the
// project, and an unbound claude in w1:p3. present says which agents
// herdr has; text answers each read, cut to the lines asked for, as herdr
// cuts them; a pane in fail fails its reads.
type watchFake struct {
	s       *herdrtest.Server
	mu      sync.Mutex
	present map[string]bool
	text    func(pane string, read int) string
	fail    map[string]bool
	reads   map[string]int
	asked   []int
	// lists counts agent.list calls; snapshots counts session.snapshot's.
	lists, snapshots int
	// onList runs on each agent.list call, under the lock.
	onList func(f *watchFake)
}

func watchServer(t *testing.T, project string) *watchFake {
	t.Helper()
	s, inst := cliInstance(t)
	f := &watchFake{s: s, present: map[string]bool{"w1:p1": true, "w1:p2": true, "w1:p3": true},
		fail: map[string]bool{}, reads: map[string]int{}, text: func(string, int) string { return "" }}
	kinds := map[string]string{"w1:p1": "claude", "w1:p2": "codex", "w1:p3": "claude"}
	names := map[string]string{"w1:p1": "icyknoll", "w1:p2": "bluelake"}
	agents := func() []any {
		var out []any
		for _, p := range []string{"w1:p1", "w1:p2", "w1:p3"} {
			if f.present[p] {
				a := agentMap(p, kinds[p], "working", 1, 0)
				if names[p] != "" {
					a["name"] = names[p]
				}
				out = append(out, a)
			}
		}
		return out
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.snapshots++
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "tabs": []any{}, "panes": []any{}, "agents": agents(),
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 3, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "working"}},
		}}, nil
	})
	s.Handle("agent.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lists++
		if f.onList != nil {
			f.onList(f)
		}
		return map[string]any{"type": "agent_list", "agents": agents()}, nil
	})
	s.Handle("pane.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Lines  int    `json:"lines"`
		}
		_ = json.Unmarshal(r.Params, &p)
		f.mu.Lock()
		f.asked = append(f.asked, p.Lines)
		f.reads[p.PaneID]++
		n, fail, text := f.reads[p.PaneID], f.fail[p.PaneID], f.text
		f.mu.Unlock()
		if fail {
			return nil, &herdrtest.Error{Code: "pane_not_found", Message: "no pane " + p.PaneID}
		}
		lines := strings.Split(strings.TrimSuffix(text(p.PaneID, n), "\n"), "\n")
		lines = lines[max(0, len(lines)-p.Lines):]
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3,
			"text": strings.Join(lines, "\n") + "\n", "truncated": false}}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, n := range []struct{ name, tab, pane, kind string }{
		{"IcyKnoll", "w1:t1", "w1:p1", "claude"}, {"BlueLake", "w1:t2", "w1:p2", "codex"},
	} {
		if _, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: n.name, MailProjectKey: project,
			ProjectID: "proj", Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: n.tab,
				PaneID: n.pane, HerdrAlias: strings.ToLower(n.name), Kind: n.kind}}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// fastWatch runs the rounds of a watch without waiting.
func fastWatch(t *testing.T, next func(round int)) {
	t.Helper()
	old := watchNext
	round := 0
	watchNext = func(ctx context.Context, _ time.Duration) bool {
		round++
		if next != nil {
			next(round)
		}
		return ctx.Err() == nil
	}
	t.Cleanup(func() { watchNext = old })
}

// screen is a working agent's pane at its read-th read: six old lines and
// one line more each read, a task line that is ticked from the third
// read, then the live region, whose spinner and last line change on every
// read.
func screen(read int) string {
	task := "☐ task A"
	if read >= 3 {
		task = "☒ task A"
	}
	text := strings.ReplaceAll(numbered(6), "line", "old") + numbered(read) + "\n" + task + "\n"
	text += fmt.Sprintf("✻ Working… (%ds)\n", read) + strings.Repeat("│ box │\n", logsLiveLines-2)
	return text + fmt.Sprintf("status %d\n", read)
}

// watch prints the lines above each pane's live region: an agent's first
// read its last --tail lines, then each line whose text the last read did
// not hold. The spinner and the last line change on every read, and
// neither prints; the task line that the agent redraws above the live
// region prints once, with its new text; blank lines are left out. Each
// read asks for ntm's 100 lines and the live region's 10 (nhm-rnx,
// nhm-zac).
func TestWatchPrintsEachLineOnce(t *testing.T) {
	f := watchServer(t, t.TempDir())
	f.text = func(pane string, read int) string {
		if pane != "w1:p1" {
			return ""
		}
		return screen(read)
	}
	fastWatch(t, nil)
	code, out, errOut := run("watch", "swarm", "IcyKnoll", "--tail", "4", "--count", "4", "--no-timestamps", "--socket", f.s.SocketPath)
	want := "\nWatching session: swarm\nPress Ctrl+C to stop\n" +
		"[IcyKnoll] old 6\n[IcyKnoll] line 1\n[IcyKnoll] ☐ task A\n[IcyKnoll] line 2\n" +
		"[IcyKnoll] line 3\n[IcyKnoll] ☒ task A\n[IcyKnoll] line 4\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("watch = %d\n%q\n%s\nwant\n%q", code, out, errOut, want)
	}
	if fmt.Sprint(f.asked) != "[110 110 110 110]" {
		t.Fatalf("lines asked = %v, want 100 and the live region's 10 in each read", f.asked)
	}
	// The alias w, and the time in UTC by default.
	f.reads = map[string]int{}
	code, out, _ = run("w", "swarm", "IcyKnoll", "--count", "1", "--socket", f.s.SocketPath)
	if code != 0 || !regexp.MustCompile(`(?m)^\[IcyKnoll \d\d:\d\d:\d\d\] old 1$`).MatchString(out) {
		t.Fatalf("w = %d %q", code, out)
	}
}

// --json prints ntm's frames: watch_started, then a pane_output frame per
// agent and read with new lines, with ntm's short agent types.
func TestWatchJSONFrames(t *testing.T) {
	f := watchServer(t, t.TempDir())
	f.text = func(pane string, read int) string {
		return fmt.Sprintf("%s says %d\n", pane, read) + strings.Repeat("│ box │\n", logsLiveLines)
	}
	fastWatch(t, nil)
	code, out, _ := run("watch", "swarm", "--json", "--count", "2", "--socket", f.s.SocketPath)
	frames := parseFrames(t, out)
	var got []string
	for _, fr := range frames {
		if fr["session"] != "swarm" || fr["timestamp"] == "" {
			t.Fatalf("frame = %v", fr)
		}
		got = append(got, str(fr["event"])+" "+str(fr["pane"])+" "+str(fr["agent_type"])+" "+str(fr["lines"]))
	}
	want := []string{"watch_started   ",
		"pane_output IcyKnoll cc [w1:p1 says 1]", "pane_output BlueLake cod [w1:p2 says 1]", "pane_output w1:p3 cc [w1:p3 says 1]",
		"pane_output IcyKnoll cc [w1:p1 says 2]", "pane_output BlueLake cod [w1:p2 says 2]", "pane_output w1:p3 cc [w1:p3 says 2]"}
	if code != 0 || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("watch --json = %d\n%v\nwant\n%v", code, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// --cc, --cod and --gmi keep their kinds; --pane keeps one agent and wins
// over them; a tmux index is refused; nhm's selectors come first.
func TestWatchFilters(t *testing.T) {
	f := watchServer(t, t.TempDir())
	f.text = func(pane string, _ int) string { return pane + "\n" + strings.Repeat("│ box │\n", logsLiveLines) }
	fastWatch(t, nil)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--cod"}, "[BlueLake] w1:p2\n"},
		{[]string{"--cc"}, "[IcyKnoll] w1:p1\n[w1:p3] w1:p3\n"},
		{[]string{"--gmi"}, ""},
		{[]string{"--cod", "--pane", "icyknoll"}, "[IcyKnoll] w1:p1\n"},
		{[]string{"--pane", "w1:p3"}, "[w1:p3] w1:p3\n"},
		{[]string{"IcyKnoll", "--cod"}, ""},
		{[]string{"BlueLake", "w1:p3", "--cc"}, "[w1:p3] w1:p3\n"},
	} {
		args := append([]string{"watch", "swarm"}, c.args...)
		code, out, errOut := run(append(args, "--count", "1", "--no-timestamps", "--socket", f.s.SocketPath)...)
		if code != 0 || out != "\nWatching session: swarm\nPress Ctrl+C to stop\n"+c.want {
			t.Errorf("%v = %d %q %s", c.args, code, out, errOut)
		}
	}
	code, out, errOut := run("watch", "swarm", "--pane", "2", "--socket", f.s.SocketPath)
	if code != 2 || out != "" || !strings.Contains(errOut, "herdr has no tmux pane indexes") {
		t.Fatalf("--pane 2 = %d %q %q", code, out, errOut)
	}
}

// A read that fails ends the watch, as ntm's; with --activity it is
// skipped.
func TestWatchReadFailure(t *testing.T) {
	f := watchServer(t, t.TempDir())
	f.text = func(pane string, _ int) string { return pane + "\n" + strings.Repeat("│ box │\n", logsLiveLines) }
	f.fail["w1:p2"] = true
	fastWatch(t, nil)
	code, out, errOut := run("watch", "swarm", "--count", "2", "--no-timestamps", "--socket", f.s.SocketPath)
	if code != 1 || !strings.Contains(errOut, "failed to capture pane output") || !strings.Contains(out, "[IcyKnoll] w1:p1\n") {
		t.Fatalf("watch = %d %q %q", code, out, errOut)
	}
	code, out, errOut = run("watch", "swarm", "--activity", "--count", "2", "--no-timestamps", "--socket", f.s.SocketPath)
	if code != 0 || errOut != "" || out != "\nWatching session: swarm\nPress Ctrl+C to stop\n[IcyKnoll] w1:p1\n[w1:p3] w1:p3\n" {
		t.Fatalf("watch --activity = %d %q %q", code, out, errOut)
	}
}

// An agent that starts during the watch joins it, with its first read's
// tail; one that stops leaves it. The model is built again only when
// herdr's agents change.
func TestWatchAgentsJoinAndLeave(t *testing.T) {
	f := watchServer(t, t.TempDir())
	f.present["w1:p2"] = false
	f.text = func(pane string, read int) string {
		return fmt.Sprintf("%s read %d\n", pane, read) + strings.Repeat("│ box │\n", logsLiveLines)
	}
	// The first list is the start's; each later round lists once.
	f.onList = func(f *watchFake) {
		switch f.lists {
		case 2:
			f.present["w1:p2"] = true
		case 4:
			f.present["w1:p1"] = false
		}
	}
	fastWatch(t, nil)
	code, out, errOut := run("watch", "swarm", "--count", "5", "--no-timestamps", "--socket", f.s.SocketPath)
	want := "\nWatching session: swarm\nPress Ctrl+C to stop\n" +
		"[IcyKnoll] w1:p1 read 1\n[w1:p3] w1:p3 read 1\n" +
		"[IcyKnoll] w1:p1 read 2\n[BlueLake] w1:p2 read 1\n[w1:p3] w1:p3 read 2\n" +
		"[IcyKnoll] w1:p1 read 3\n[BlueLake] w1:p2 read 2\n[w1:p3] w1:p3 read 3\n" +
		"[BlueLake] w1:p2 read 3\n[w1:p3] w1:p3 read 4\n" +
		"[BlueLake] w1:p2 read 4\n[w1:p3] w1:p3 read 5\n"
	if code != 0 || out != want {
		t.Fatalf("watch = %d %s\n%q\nwant\n%q", code, errOut, out, want)
	}
	// One snapshot to find the session, then a model at the start and at
	// each of the two changes: not one per round.
	if f.snapshots != 4 {
		t.Fatalf("session.snapshot calls = %d, want 4", f.snapshots)
	}
}

// Without a session, the calling pane's workspace; outside herdr, the
// session must be named.
func TestWatchSessionFromCallingPane(t *testing.T) {
	f := watchServer(t, t.TempDir())
	f.text = func(pane string, _ int) string { return pane + "\n" + strings.Repeat("│ box │\n", logsLiveLines) }
	fastWatch(t, nil)
	t.Setenv("HERDR_WORKSPACE_ID", "w1")
	code, out, _ := run("watch", "--pane", "BlueLake", "--count", "1", "--no-timestamps", "--socket", f.s.SocketPath)
	if code != 0 || out != "\nWatching session: swarm\nPress Ctrl+C to stop\n[BlueLake] w1:p2\n" {
		t.Fatalf("watch = %d %q", code, out)
	}
	t.Setenv("HERDR_WORKSPACE_ID", "")
	code, out, errOut := run("watch", "--count", "1", "--socket", f.s.SocketPath)
	if code != 2 || out != "" || !strings.Contains(errOut, "name the session") {
		t.Fatalf("no session = %d %q %q", code, out, errOut)
	}
}

// --bead prints the lines that name the bead (all of an agent's first
// read, then its new ones) and the bead's status when it changes: with
// --interval given, every round; without it, every 30 seconds.
func TestWatchBead(t *testing.T) {
	project := t.TempDir()
	f := watchServer(t, project)
	f.text = func(pane string, read int) string {
		if pane != "w1:p1" {
			return "nothing here\n" + strings.Repeat("│ nhm-7 box │\n", logsLiveLines)
		}
		text := "working on nhm-7 now\nnhm-77 is another\n"
		if read >= 2 {
			text += "NHM-7 done\n"
		}
		return text + strings.Repeat("│ nhm-7 box │\n", logsLiveLines)
	}
	var mu sync.Mutex
	var roots []string
	statuses := []string{"open", "in_progress", "in_progress"}
	old := watchBeadStatus
	t.Cleanup(func() { watchBeadStatus = old })
	watchBeadStatus = func(_ context.Context, root, id string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		roots = append(roots, root+" "+id)
		if len(roots) > len(statuses) {
			return "", fmt.Errorf("br show %s: gone", id)
		}
		return statuses[len(roots)-1], nil
	}
	// Each round takes at least --interval (1ms), so each reads the status.
	fastWatch(t, func(int) { time.Sleep(2 * time.Millisecond) })
	code, out, errOut := run("watch", "swarm", "--bead", "nhm-7", "--interval", "1", "--count", "4", "--no-timestamps", "--socket", f.s.SocketPath)
	want := "\nWatching bead nhm-7 in session: swarm\nPress Ctrl+C to stop\n" +
		"[IcyKnoll] mention: working on nhm-7 now\nbead status: open\n" +
		"[IcyKnoll] mention: NHM-7 done\nbead status: in_progress\n" +
		"bead status: unknown (lookup error: br show nhm-7: gone)\n"
	if code != 0 || out != want {
		t.Fatalf("watch --bead = %d %s\n%q\nwant\n%q", code, errOut, out, want)
	}
	if len(roots) != 4 || roots[0] != project+" nhm-7" {
		t.Fatalf("status reads = %v", roots)
	}
	roots, f.reads = nil, map[string]int{}
	code, out, _ = run("watch", "swarm", "--bead", "nhm-7", "--json", "--count", "3", "--socket", f.s.SocketPath)
	frames := parseFrames(t, out)
	var events []string
	for _, fr := range frames {
		events = append(events, str(fr["event"])+" "+str(fr["bead"])+" "+str(fr["text"])+str(fr["status"]))
	}
	if code != 0 || len(roots) != 1 || fmt.Sprint(events) != fmt.Sprint([]string{"watch_started nhm-7 ",
		"bead_mention nhm-7 working on nhm-7 now", "bead_status nhm-7 open", "bead_mention nhm-7 NHM-7 done"}) {
		t.Fatalf("watch --bead --json = %d %v (status reads %v)", code, events, roots)
	}
}

// --pattern --command scans the project each round when fsnotify cannot
// start, and says so once on stderr. When a file that matches changes,
// the command goes to the agents through core send: the bound ones, in
// any state; an unbound agent is a failure of its own.
func TestWatchFilesSendsCommand(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "internal", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := watchServer(t, project)
	pollWatch(t)
	var sent []send.Request
	old := watchSend
	t.Cleanup(func() { watchSend = old })
	watchSend = func(_ context.Context, _ *instanceEnv, req send.Request) (send.Result, error) {
		sent = append(sent, req)
		var res send.Result
		for i, id := range req.Selector.Bindings {
			status := send.StatusSubmitted
			if i == 1 {
				status = send.StatusRefused
			}
			r := send.Receipt{BindingID: id, PaneID: fmt.Sprintf("w1:p%d", i+1), Status: status}
			r.Result.Reason = "agent is blocked"
			res.Targets = append(res.Targets, r)
		}
		return res, nil
	}
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(project, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A file that does not match; a burst over two scans that does, sent
	// once after the scan that sees no change to a matching file (ntm's
	// debounce, nhm-zac review), which a file that does not match does not
	// hold back; then a file under an ignored folder.
	fastWatch(t, func(round int) {
		switch round {
		case 1:
			write("notes.txt")
		case 2:
			write("internal/cli/watch.go")
		case 3:
			if err := os.WriteFile(filepath.Join(project, "internal", "cli", "watch.go"), []byte("longer"), 0o600); err != nil {
				t.Error(err)
			}
		case 4:
			write("notes2.txt")
		case 5:
			if err := os.MkdirAll(filepath.Join(project, "node_modules"), 0o755); err != nil {
				t.Error(err)
			}
			write("node_modules/x.go")
		}
	})
	code, out, errOut := run("watch", "swarm", "--pattern", "*.go", "--command", "go test ./...", "--count", "6", "--socket", f.s.SocketPath)
	want := fmt.Sprintf("\nWatching files matching '*.go' in %s...\nWill run command: go test ./...\nPress Ctrl+C to stop\n", project) +
		fmt.Sprintf("File changed: %s\n", filepath.Join(project, "internal", "cli", "watch.go")) +
		"Triggering command on 3 pane(s)...\n" +
		"Failed to send to pane w1:p3: no binding: nhm send reaches bound agents only\n" +
		"Failed to send to pane w1:p2: agent is blocked\n"
	if code != 0 || out != want || errOut != pollNote {
		t.Fatalf("watch --pattern = %d %q\n%q\nwant\n%q", code, errOut, out, want)
	}
	if len(sent) != 1 || sent[0].Message != "go test ./..." || !sent[0].WhenAny || sent[0].WorkspaceID != "w1" ||
		sent[0].ProjectRoot != project || len(sent[0].Selector.Bindings) != 2 {
		t.Fatalf("sends = %+v", sent)
	}
	// JSON, a pattern with a slash, and the calling pane (BlueLake's) left
	// out: IcyKnoll gets the command, and the unbound agent fails.
	sent = nil
	t.Setenv("HERDR_PANE_ID", "w1:p2")
	fastWatch(t, func(round int) {
		if round == 1 {
			if err := os.WriteFile(filepath.Join(project, "internal", "cli", "watch.go"), []byte("changed"), 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	code, out, _ = run("watch", "swarm", "--pattern", "internal/*/*.go", "--command", "make", "--json", "--count", "2", "--socket", f.s.SocketPath)
	frames := parseFrames(t, out)
	var events []string
	for _, fr := range frames {
		events = append(events, str(fr["event"])+" "+str(fr["path"])+str(fr["pane_id"])+" "+str(fr["text"])+str(fr["error"])+
			" "+str(fr["delivered"])+str(fr["failed"]))
	}
	want = fmt.Sprint([]string{"watch_started " + project + " internal/*/*.go ",
		"file_change " + filepath.Join(project, "internal", "cli", "watch.go") + "  ",
		"dispatch_error w1:p3 no binding: nhm send reaches bound agents only ",
		"dispatch  make 11"})
	if code != 0 || fmt.Sprint(events) != want || len(sent) != 1 || len(sent[0].Selector.Bindings) != 1 {
		t.Fatalf("watch --pattern --json = %d\n%s\nwant %s\nsends %+v", code, strings.Join(events, "\n"), want, sent)
	}
}

// pollNote is what watch says when fsnotify cannot start in pollWatch.
const pollNote = "nhm watch: fsnotify cannot start (too many open files); polling the project instead\n"

// pollWatch makes the file watch poll: fsnotify cannot start.
func pollWatch(t *testing.T) {
	t.Helper()
	old := watchNotify
	watchNotify = func(watcher.Handler, watcher.ErrorHandler, func(string) bool) (watchNotifier, error) {
		return nil, errors.New("too many open files")
	}
	t.Cleanup(func() { watchNotify = old })
}

// readyNotifier closes ready once the watch of the root is in place.
type readyNotifier struct {
	watchNotifier
	ready chan struct{}
}

func (n readyNotifier) Add(path string) error {
	err := n.watchNotifier.Add(path)
	close(n.ready)
	return err
}

// The file watch on fsnotify, as ntm's: a burst of writes to a matching
// file sends --command once, after the 250ms debounce; a file that does
// not match and the files under ignored folders, old or new, send
// nothing; --count counts the bursts (nhm-98l).
func TestWatchFilesNotify(t *testing.T) {
	project := t.TempDir()
	for _, d := range []string{"internal/cli", "vendor"} {
		if err := os.MkdirAll(filepath.Join(project, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := watchServer(t, project)
	var mu sync.Mutex
	var sent []time.Time
	old := watchSend
	t.Cleanup(func() { watchSend = old })
	watchSend = func(_ context.Context, _ *instanceEnv, req send.Request) (send.Result, error) {
		mu.Lock()
		sent = append(sent, time.Now())
		mu.Unlock()
		var res send.Result
		for i, id := range req.Selector.Bindings {
			res.Targets = append(res.Targets, send.Receipt{BindingID: id, PaneID: fmt.Sprintf("w1:p%d", i+1), Status: send.StatusSubmitted})
		}
		return res, nil
	}
	sends := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(sent)
	}
	ready := make(chan struct{})
	oldNotify := watchNotify
	t.Cleanup(func() { watchNotify = oldNotify })
	watchNotify = func(h watcher.Handler, e watcher.ErrorHandler, m func(string) bool) (watchNotifier, error) {
		n, err := oldNotify(h, e, m)
		if err != nil {
			return nil, err
		}
		return readyNotifier{n, ready}, nil
	}
	type result struct {
		code        int
		out, errOut string
	}
	done := make(chan result, 1)
	go func() {
		code, out, errOut := run("watch", "swarm", "--pattern", "*.go", "--command", "go test ./...", "--count", "2", "--socket", f.s.SocketPath)
		done <- result{code, out, errOut}
	}()
	select {
	case <-ready:
	case r := <-done:
		t.Fatalf("watch ended before its watch was in place: %+v", r)
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not start")
	}
	write := func(name, text string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(project, name)), 0o755); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(project, name), []byte(text), 0o600); err != nil {
			t.Error(err)
		}
	}
	write("notes.txt", "n")
	write("vendor/x.go", "x")
	write("node_modules/y.go", "y")
	time.Sleep(3 * watcher.DefaultDebounceDuration)
	if n := sends(); n != 0 {
		t.Fatalf("%d sends for files that do not match or are ignored", n)
	}
	var burst time.Time
	waitSends := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); sends() < want; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%d sends, want %d", sends(), want)
			}
		}
	}
	for i := range 3 {
		write("internal/cli/watch.go", strings.Repeat("x", i+1))
		burst = time.Now()
	}
	waitSends(1)
	mu.Lock()
	if wait := sent[0].Sub(burst); wait < watcher.DefaultDebounceDuration {
		t.Errorf("the send came %v after the burst, before the 250ms debounce", wait)
	}
	mu.Unlock()
	write("internal/cli/watch.go", "again")
	waitSends(2)
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watch --count 2 did not stop")
	}
	changed := fmt.Sprintf("File changed: %s\nTriggering command on 3 pane(s)...\n", filepath.Join(project, "internal", "cli", "watch.go")) +
		"Failed to send to pane w1:p3: no binding: nhm send reaches bound agents only\n"
	want := fmt.Sprintf("\nWatching files matching '*.go' in %s...\nWill run command: go test ./...\nPress Ctrl+C to stop\n", project) +
		changed + changed
	if r.code != 0 || r.out != want || r.errOut != "" || sends() != 2 {
		t.Fatalf("watch --pattern on fsnotify = %d %q (%d sends)\n%q\nwant\n%q", r.code, r.errOut, sends(), r.out, want)
	}
}

// fakeNotifier is a watcher whose Add fails with add, or with late
// succeeds and then reports two errors: one that watch goes on after,
// and that inotify had no watch left for a new folder.
type fakeNotifier struct {
	add    error
	late   bool
	onErr  watcher.ErrorHandler
	closed int
}

func (n *fakeNotifier) Add(string) error {
	if n.late {
		n.onErr(errors.New("fsnotify: queue or buffer overflow"))
		n.onErr(fmt.Errorf("watching new directory x: watching x: %w", watcher.ErrNoWatches))
	}
	return n.add
}

func (n *fakeNotifier) Close() error {
	n.closed++
	return nil
}

// When inotify has no watch left, at the start or for a new folder,
// watch releases the watcher's watches, says so once on stderr, and
// polls the project; another error of the start ends the watch, as ntm's
// (nhm-98l).
func TestWatchFilesFallsBackWhenWatchesRunOut(t *testing.T) {
	project := t.TempDir()
	f := watchServer(t, project)
	var sent int
	old := watchSend
	t.Cleanup(func() { watchSend = old })
	watchSend = func(context.Context, *instanceEnv, send.Request) (send.Result, error) {
		sent++
		return send.Result{}, nil
	}
	oldNotify := watchNotify
	t.Cleanup(func() { watchNotify = oldNotify })
	for _, c := range []struct {
		name string
		n    *fakeNotifier
	}{
		{"at the start", &fakeNotifier{add: fmt.Errorf("watching %s: %w", project, watcher.ErrNoWatches)}},
		{"for a new folder", &fakeNotifier{late: true}},
	} {
		sent = 0
		watchNotify = func(_ watcher.Handler, onErr watcher.ErrorHandler, _ func(string) bool) (watchNotifier, error) {
			c.n.onErr = onErr
			return c.n, nil
		}
		fastWatch(t, func(round int) {
			if round == 1 {
				if err := os.WriteFile(filepath.Join(project, c.name+".go"), []byte("x"), 0o600); err != nil {
					t.Error(err)
				}
			}
		})
		code, out, errOut := run("watch", "swarm", "--pattern", "*.go", "--command", "make", "--count", "2", "--socket", f.s.SocketPath)
		if code != 0 || errOut != watchNoWatches+"\n" || c.n.closed == 0 || sent != 1 ||
			!strings.Contains(out, "File changed: "+filepath.Join(project, c.name+".go")) {
			t.Errorf("%s: watch = %d %q, closed %d, %d sends\n%s", c.name, code, errOut, c.n.closed, sent, out)
		}
	}
	watchNotify = func(watcher.Handler, watcher.ErrorHandler, func(string) bool) (watchNotifier, error) {
		return &fakeNotifier{add: os.ErrNotExist}, nil
	}
	code, _, errOut := run("watch", "swarm", "--pattern", "*.go", "--command", "make", "--socket", f.s.SocketPath)
	if code != 1 || !strings.Contains(errOut, "failed to watch directory: file does not exist") {
		t.Errorf("a start that fails otherwise = %d %q", code, errOut)
	}
}

// A scan sees a file created, written and removed, skips ntm's ignored
// names, and is nil when the root cannot be read, so that a missing root
// does not read as every file removed.
func TestScanTree(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "a")
	write("old.go", "o")
	before := scanTree(root)
	write("a.go", "longer")
	write("b.go", "b")
	write(".git/HEAD", "ref")
	write("node_modules/x/y.go", "y")
	if err := os.Remove(filepath.Join(root, "old.go")); err != nil {
		t.Fatal(err)
	}
	after := scanTree(root)
	var got []string
	for _, p := range changedPaths(before, after) {
		if p != root {
			got = append(got, strings.TrimPrefix(p, root+string(filepath.Separator)))
		}
	}
	if fmt.Sprint(got) != "[a.go b.go old.go]" {
		t.Fatalf("changed = %v", got)
	}
	if scanTree(filepath.Join(root, "gone")) != nil {
		t.Fatal("a missing root scans as a tree")
	}
}

// The file watch waits --interval between scans, or five times the last
// scan's time when that is longer.
func TestScanPause(t *testing.T) {
	for _, c := range []struct{ interval, scan, want time.Duration }{
		{250 * time.Millisecond, 10 * time.Millisecond, 250 * time.Millisecond},
		{250 * time.Millisecond, time.Second, 5 * time.Second},
		{10 * time.Second, time.Second, 10 * time.Second},
	} {
		if got := scanPause(c.interval, c.scan); got != c.want {
			t.Errorf("scanPause(%v, %v) = %v, want %v", c.interval, c.scan, got, c.want)
		}
	}
}

// The file watch's refusals, as ntm's.
func TestWatchRefusals(t *testing.T) {
	f := watchServer(t, t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--pattern", "*.go"}, "--command is required with --pattern"},
		{[]string{"--pattern", "*.go", "--command", "x", "--bead", "b-1"}, "--pattern and --bead cannot be used together"},
		{[]string{"--bead", " "}, "--bead is required"},
		{[]string{"--interval", "0"}, "--interval must be positive"},
		{[]string{"--interval", "soon"}, "invalid --interval"},
	} {
		code, out, errOut := run(append([]string{"watch", "swarm", "--socket", f.s.SocketPath}, c.args...)...)
		if code != 2 || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%v = %d %q %q", c.args, code, out, errOut)
		}
	}
}
