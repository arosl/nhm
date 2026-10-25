package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func errorsText(pane string) string {
	switch pane {
	case "w1:p1":
		return "start\nbuilding\nError: config invalid\nnext\nlast\n"
	case "w1:p2":
		return "ok\npanic: boom\n"
	case "w1:p3":
		return "ERROR in the shell\n"
	}
	return "fine\n"
}

// errors finds ntm's patterns in the agent panes (not the shells), with
// -C lines of context, under a header per pane, then ntm's total. As in
// ntm, a match fewer than -C lines from the top gets no context before
// it (its loop starts below line 1 and stops at once).
func TestErrorsFindsPatterns(t *testing.T) {
	s, _, reads := paneServer(t, errorsText)
	code, out, errOut := run("errors", "swarm", "--socket", s.SocketPath)
	want := "\n2 error(s) found in session 'swarm'\n\n" +
		"─── IcyKnoll opus (claude) ───\n" +
		"1- start\n2- building\n3: Error: config invalid [error]\n4- next\n5- last\n" +
		"\n─── BlueLake gpt (codex) ───\n" +
		"2: panic: boom [panic]\n3- \n" +
		"\nSearched 3 pane(s), 11 line(s)\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("errors = %d %q\n%s\nwant\n%s", code, errOut, out, want)
	}
	if got := reads(); len(got) != 3 || got[0] != (paneRead{"w1:p4", 1000}) {
		t.Errorf("reads = %v, want 1000 lines of each agent pane", got)
	}
	// -C sets the context; -n the lines read; --cod keeps the codex panes.
	code, out, _ = run("errors", "swarm", "-C", "0", "-n", "20", "--cod", "--socket", s.SocketPath)
	if code != 0 || out != "\n1 error(s) found in session 'swarm'\n\n─── BlueLake gpt (codex) ───\n2: panic: boom [panic]\n\nSearched 1 pane(s), 3 line(s)\n" {
		t.Errorf("-C 0 --cod = %d\n%s", code, out)
	}
	if got := reads(); len(got) != 1 || got[0] != (paneRead{"w1:p2", 20}) {
		t.Errorf("-n 20 --cod reads = %v", got)
	}
}

// --panes takes pane indexes: pane M of the session's agent tab.
func TestErrorsPanesAsSlots(t *testing.T) {
	s, _, reads := paneServer(t, errorsText)
	code, out, _ := run("errors", "swarm", "--panes", "2", "-C", "0", "--socket", s.SocketPath)
	if code != 0 || !strings.Contains(out, "─── BlueLake gpt (codex) ───\n2: panic: boom [panic]\n") || strings.Contains(out, "IcyKnoll") {
		t.Errorf("--panes 2 = %d\n%s", code, out)
	}
	if got := reads(); len(got) != 1 || got[0].pane != "w1:p2" {
		t.Errorf("--panes 2 reads = %v", got)
	}
	code, out, errOut := run("errors", "swarm", "--panes", "1,x", "--socket", s.SocketPath)
	if code != 1 || out != "" || errOut != "Error: invalid pane index 'x': strconv.Atoi: parsing \"x\": invalid syntax\n" {
		t.Errorf("--panes 1,x = %d %q %q", code, out, errOut)
	}
}

// Errors found are not a failure: exit 0, as ntm.
func TestErrorsExitZeroWithErrors(t *testing.T) {
	s, _, _ := paneServer(t, errorsText)
	if code, _, _ := run("errors", "swarm", "--socket", s.SocketPath); code != 0 {
		t.Errorf("errors with errors found = %d, want 0", code)
	}
	s2, _, _ := paneServer(t, func(string) string { return "all good\n" })
	code, out, _ := run("errors", "swarm", "--socket", s2.SocketPath)
	if code != 0 || out != "✓ No errors found in session 'swarm'\n" {
		t.Errorf("no errors = %d %q", code, out)
	}
}

// --json prints ntm's ErrorsResult; errors is null when there are none.
func TestErrorsJSONNullWhenNone(t *testing.T) {
	s, _, _ := paneServer(t, errorsText)
	code, out, _ := run("errors", "swarm", "--cod", "-C", "1", "--json", "--socket", s.SocketPath)
	var r ErrorsResult
	if code != 0 || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("--json = %d %q", code, out)
	}
	if r.Session != "swarm" || r.TotalErrors != 1 || r.TotalLines != 3 || r.PaneCount != 1 || r.Timestamp.IsZero() ||
		len(r.Errors) != 1 {
		t.Fatalf("result = %+v", r)
	}
	e := r.Errors[0]
	if e.Pane != "BlueLake gpt" || e.PaneIndex != 2 || e.Line != 2 || e.Content != "panic: boom" || e.MatchType != "panic" ||
		e.AgentType != "codex" || strings.Join(e.Context, "|") != "ok|" || e.Session != "swarm" {
		t.Errorf("entry = %+v", e)
	}
	s2, _, _ := paneServer(t, func(string) string { return "all good\n" })
	code, out, _ = run("errors", "swarm", "--json", "--socket", s2.SocketPath)
	if code != 0 || !strings.Contains(out, "\"errors\": null") || !strings.Contains(out, "\"total_errors\": 0") {
		t.Errorf("no errors --json = %d %s", code, out)
	}
}
