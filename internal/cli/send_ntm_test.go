package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// ntmSendInstance is newSendInstance, owned by nhm, with herdr's tabs and
// pane labels: IcyKnoll (claude, model opus, tags frontend and api) in
// tab 1, BlueLake (codex, tag backend) in tab 2.
func ntmSendInstance(t *testing.T) (string, *sendHerdr) {
	t.Helper()
	sock, h, _, _ := ntmSendWorld(t)
	return sock, h
}

// ntmSendWorld is ntmSendInstance with its instance and a setter of a
// pane's herdr label.
func ntmSendWorld(t *testing.T) (string, *sendHerdr, store.Instance, func(pane, label string)) {
	t.Helper()
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	var mu sync.Mutex
	labels := map[string]string{"w1:p1": "IcyKnoll opus[frontend,api]", "w1:p2": "BlueLake[backend]"}
	pane := func(id, tab, kind string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"pane_id": id, "tab_id": tab, "workspace_id": "w1", "terminal_id": "t-" + id,
			"label": labels[id], "agent": kind, "agent_status": "idle", "focused": false, "revision": 1}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "layouts": []any{}, "agents": []any{},
			"tabs": []any{
				map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "number": 1, "label": "1", "focused": true, "pane_count": 1, "agent_status": "idle"},
				map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "number": 2, "label": "2", "focused": false, "pane_count": 1, "agent_status": "idle"},
			},
			"panes": []any{pane("w1:p1", "w1:t1", "claude"), pane("w1:p2", "w1:t2", "codex")},
			"workspaces": []any{map[string]any{"workspace_id": "w1", "number": 1, "label": "swarm", "focused": true,
				"pane_count": 2, "tab_count": 2, "active_tab_id": "w1:t1", "agent_status": "idle"}},
		}}, nil
	})
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	icy, err := bs.ActiveByName(context.Background(), "/p", "IcyKnoll")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Occupy(context.Background(), icy.ID, icy.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: icy.TabID,
		PaneID: icy.PaneID, HerdrAlias: icy.HerdrAlias, Kind: icy.Kind, Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	return s.SocketPath, h, inst, func(id, label string) {
		mu.Lock()
		labels[id] = label
		mu.Unlock()
	}
}

// TestSendNTMSelectors: ntm's selectors on nhm's bound agents: a type
// flag's variant (the binding's model, compared exactly, as ntm's),
// --tag on the herdr pane label, -s, --pane (ntm's index in the active
// tab, or W.P) and --panes, with ntm's text and errors.
func TestSendNTMSelectors(t *testing.T) {
	sock, h := ntmSendInstance(t)
	last := func() string {
		got := h.sent()
		return strings.Join(got, ";")
	}
	for _, c := range []struct {
		args []string
		out  string
		sent string
	}{
		{[]string{"a", "--cc=opus"}, "Sent to 1 pane(s)\n", "w1:p1=a"},
		{[]string{"b", "--tag", "backend"}, "Sent to 1 pane(s)\n", "w1:p2=b"},
		{[]string{"c", "--tag", "API,nothing"}, "Sent to 1 pane(s)\n", "w1:p1=c"},
		{[]string{"d", "-s"}, "Sent to 1 pane(s)\n", "w1:p2=d"},
		{[]string{"e", "--pane", "1"}, "Sent to pane 0.1\n", "w1:p1=e"},
		{[]string{"f", "-p", "1.1"}, "Sent to pane 1.1\n", "w1:p2=f"},
		{[]string{"g", "--panes", "0.1,1.1"}, "Sent to 2 pane(s)\n", "w1:p1=g;w1:p2=g"},
	} {
		before := len(h.sent())
		code, out, errOut := run(append([]string{"send", "swarm", "--socket", sock}, c.args...)...)
		// Core send types into the targets at once, in no fixed order.
		sent := h.sent()[before:]
		sort.Strings(sent)
		got := strings.Join(sent, ";")
		if code != 0 || out != c.out || got != c.sent {
			t.Errorf("%v = %d %q %q, sent %q (all %s)", c.args, code, out, errOut, got, last())
		}
	}
	// No agent matches: ntm's line and error, exit 1; under --json
	// NO_MATCHING_PANES.
	for _, v := range []string{"--cc=sonnet", "--cc=Opus"} {
		code, out, errOut := run("send", "swarm", "x", v, "--socket", sock)
		if code != exitError || out != "No matching panes found\n" || errOut != "Error: no matching panes found\n" {
			t.Fatalf("no match %s = %d %q %q", v, code, out, errOut)
		}
	}
	code, out, _ := run("send", "swarm", "x", "--cc=sonnet", "--socket", sock, "--json")
	if r := decodeSendResult(t, out); code != exitError || r.ErrorCode != "NO_MATCHING_PANES" || len(r.Targets) != 0 {
		t.Fatalf("no match, --json = %d %s", code, out)
	}
	for _, c := range []struct {
		args []string
		err  string
	}{
		{[]string{"x", "--pane", "1", "--panes", "1.1"}, "cannot use --pane and --panes together"},
		{[]string{"x", "-s", "--pane", "1"}, "cannot combine --skip-first with explicit --pane/--panes selectors"},
		{[]string{"x", "--pane", "7"}, "7"},
	} {
		if code, _, errOut := run(append([]string{"send", "swarm", "--socket", sock}, c.args...)...); code != exitError || !strings.Contains(errOut, c.err) {
			t.Errorf("%v = %d %q", c.args, code, errOut)
		}
	}
}

// TestSendNTMLaterFlags: a flag of a later bead of the send family
// refuses and names its bead; under --json it is NOT_IMPLEMENTED, exit 2.
func TestSendNTMLaterFlags(t *testing.T) {
	sock, h := ntmSendInstance(t)
	if code, _, errOut := run("send", "swarm", "x", "--smart", "--socket", sock); code != exitError || !strings.Contains(errOut, "nhm-2p6.5.7") {
		t.Fatalf("--smart = %d %q", code, errOut)
	}
	code, out, _ := run("send", "swarm", "x", "--context", "a.go", "--socket", sock, "--json")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || code != robotNotImplemented || m["error_code"] != "NOT_IMPLEMENTED" ||
		!strings.Contains(m["error"].(string), "nhm-2p6.5.5") {
		t.Fatalf("--context --json = %d %s", code, out)
	}
	if len(h.sent()) != 0 {
		t.Fatalf("prompts = %v", h.sent())
	}
}

// TestSendNTMProjectDryRunAndStdin: --project sends to every session of
// the base project (ntm's sendProjectResult); a dry run under --json is
// ntm's SendDryRunResult; a piped stdin is the prompt.
func TestSendNTMProjectDryRunAndStdin(t *testing.T) {
	sock, h := ntmSendInstance(t)
	code, out, errOut := run("send", "--project", "swarm", "hello all", "--socket", sock, "--json")
	var p sendProjectResult
	if err := json.Unmarshal([]byte(out), &p); err != nil || code != 0 || !p.Success || p.MatchedSessions != 1 || p.Delivered != 2 ||
		len(p.Sessions) != 1 || p.Sessions[0].Session != "swarm" {
		t.Fatalf("--project = %d %s %s", code, out, errOut)
	}
	if code, _, errOut := run("send", "--project", "nowhere", "x y", "--socket", sock); code != exitError || !strings.Contains(errOut, `no sessions found for project "nowhere"`) {
		t.Fatalf("--project nowhere = %d %q", code, errOut)
	}
	// As ntm: a first argument of one word reads as a session name.
	if code, _, errOut := run("send", "--project", "swarm", "hello", "--socket", sock); code != exitError || !strings.Contains(errOut, "cannot use --project with a specific session name") {
		t.Fatalf("--project with one word = %d %q", code, errOut)
	}
	code, out, _ = run("send", "swarm", "look", "--dry-run", "--socket", sock, "--json")
	var d SendDryRunResult
	if err := json.Unmarshal([]byte(out), &d); err != nil || code != 0 || !d.DryRun || d.Total != 2 || d.WouldSend[0].Pane != "0.1" ||
		d.WouldSend[0].PaneID != "w1:p1" || d.WouldSend[0].Agent != "IcyKnoll" || d.WouldSend[0].Prompt != "look" {
		t.Fatalf("dry run = %d %s", code, out)
	}
	before := len(h.sent())
	sendStdinPiped = func() bool { return true }
	stdin = strings.NewReader("from a pipe")
	t.Cleanup(func() { sendStdinPiped, stdin = stdinHasData, os.Stdin })
	if code, out, errOut := run("send", "swarm", "--name", "BlueLake", "--socket", sock); code != 0 || strings.Join(h.sent()[before:], ";") != "w1:p2=from a pipe" {
		t.Fatalf("stdin = %d %q %q %v", code, out, errOut, h.sent())
	}
}

// TestSendNTMOperationsAndErrors: nhm's --op-id with ntm's output. A
// replay stays a replay under --wait; an operation without a final
// outcome exits 4 under --json too; the id binds the selector, not
// herdr's pane labels of the moment; --project takes no --op-id. A pin
// that the selection does not reach is named by its binding. An early
// failure has ntm's error code: INVALID_FLAG for a template flag, else
// INTERNAL_ERROR.
func TestSendNTMOperationsAndErrors(t *testing.T) {
	sock, h, inst, setLabel := ntmSendWorld(t)
	if code, out, errOut := run("send", "swarm", "w", "--name", "BlueLake", "--op-id", "op-w", "--wait", "--poll", "20ms", "--timeout", "100ms", "--socket", sock); code != exitError {
		t.Fatalf("first --wait = %d %q %q", code, out, errOut)
	}
	if code, out, _ := run("send", "swarm", "w", "--name", "BlueLake", "--op-id", "op-w", "--wait", "--poll", "20ms", "--timeout", "100ms", "--socket", sock); code == 0 ||
		!strings.HasPrefix(out, "operation op-w was claimed before; nothing sent\n") || len(h.sent()) != 1 {
		t.Fatalf("replay --wait = %d %q", code, out)
	}
	// The same id after a label change: the stored receipt.
	if code, out, _ := run("send", "swarm", "tagged", "--tag", "backend", "--op-id", "op-t", "--socket", sock); code != 0 || out != "Sent to 1 pane(s)\n" {
		t.Fatalf("tagged = %d %q", code, out)
	}
	setLabel("w1:p2", "BlueLake[ops]")
	if code, out, errOut := run("send", "swarm", "tagged", "--tag", "backend", "--op-id", "op-t", "--socket", sock); code != 0 || out != "operation op-t was claimed before; nothing sent\n" {
		t.Fatalf("tagged replay = %d %q %q", code, out, errOut)
	}
	// An operation that never ended.
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE operations SET completed_at = NULL WHERE operation_id = ?`, "op-t"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	code, out, _ := run("send", "swarm", "tagged", "--tag", "backend", "--op-id", "op-t", "--socket", sock, "--json")
	if r := decodeSendResult(t, out); code != exitAmbiguous || r.Success || !strings.Contains(r.Error, "no final outcome") || r.NHM == nil || !r.NHM.InProgress {
		t.Fatalf("in progress, --json = %d %s", code, out)
	}
	if code, _, errOut := run("send", "--project", "swarm", "to all", "--op-id", "op-p", "--socket", sock); code != exitError || !strings.Contains(errOut, "cannot combine --project with --op-id") {
		t.Fatalf("--project --op-id = %d %q", code, errOut)
	}
	// A pin of a binding that is gone, beside one that holds.
	db, err = store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	icy, err := bindings.NewStore(db).ActiveByName(context.Background(), "/p", "IcyKnoll")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	pins := fmt.Sprintf("%s:%d,bnd-gone:1", icy.ID, icy.Generation)
	code, _, errOut := run("send", "swarm", "pinned", "--if-generation", pins, "--socket", sock)
	if code != exitError || !strings.Contains(errOut, "sending to pane bnd-gone: ") {
		t.Fatalf("gone pin = %d %q", code, errOut)
	}
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"x", "--pane", "1", "--panes", "1.1"}, "INTERNAL_ERROR"},
		{[]string{"x", "--bead", "b-1"}, "INVALID_FLAG"},
		{[]string{"x", "--var", "novalue", "-t", "fresh_review"}, "INVALID_FLAG"},
		{[]string{"-t", "no_such_template"}, "INVALID_FLAG"},
	} {
		code, out, _ := run(append([]string{"send", "swarm", "--socket", sock, "--json"}, c.args...)...)
		if r := decodeSendResult(t, out); code != exitError || r.ErrorCode != c.code {
			t.Errorf("%v = %d %s, want %s", c.args, code, out, c.code)
		}
	}
}

// TestSendNTMTemplateAndProjectDryRun: with -t, positional text is
// ignored, as ntm's; --project's dry run counts only the prompts it would
// send.
func TestSendNTMTemplateAndProjectDryRun(t *testing.T) {
	sock, h := ntmSendInstance(t)
	tmpl := t.TempDir() + "/brief.md"
	if err := os.WriteFile(tmpl, []byte("Read the brief.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := run("send", "swarm", "--name", "IcyKnoll", "-t", tmpl, "ignored", "words", "--socket", sock); code != 0 ||
		len(h.sent()) != 1 || !strings.HasPrefix(h.sent()[0], "w1:p1=Read the brief.") {
		t.Fatalf("template = %d %q %q %v", code, out, errOut, h.sent())
	}
	rule := strings.Repeat("─", 40)
	h.mu.Lock()
	h.screens = map[string]string{"w1:p1": rule + "\n!\n" + rule + "\n  ! for shell mode\n"}
	h.mu.Unlock()
	code, out, _ := run("send", "--project", "swarm", "look here", "--dry-run", "--socket", sock, "--json")
	var p sendProjectResult
	if err := json.Unmarshal([]byte(out), &p); err != nil || code != 0 || len(p.Sessions) != 1 || p.Sessions[0].WouldSend != 1 ||
		strings.Join(p.Sessions[0].Targets, ",") != "1.1" {
		t.Fatalf("project dry run = %d %s", code, out)
	}
}
