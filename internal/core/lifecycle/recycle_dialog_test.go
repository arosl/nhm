package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// backgroundWork is the end of a pane at Claude Code 2.1.309's dialog
// when /exit finds background work, as herdr 0.9.5 read it in the
// sandbox (nhm-m7x).
const backgroundWork = `
✻ Sautéed for 3s · done 11:03 PM · 1 shell still running

▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Background work is running
   The following will stop when you exit:

   shell · sleep 600

   ❯ 1. Exit and stop tasks
     2. Move to background and exit
     3. Stay

   Enter to confirm · Esc to cancel
`

// dialogPane is a herdr stub with one agent in pane w1:p1. Its exit
// command puts dialog on the screen in place of an exit ("": the agent
// exits); exitKeys, sent at the dialog, end the agent (nil: the dialog
// stays), and so does the exitAfter'th agent.get after the exit command
// (0: never). A successor's ready wait runs out when startScreen is set,
// with that on the screen.
type dialogPane struct {
	mu          sync.Mutex
	kind        string
	running     bool
	screen      string
	dialog      string
	exitKeys    []string
	exitAfter   int
	gets        int
	exiting     bool
	startScreen string
	prompts     []string
	keys        [][]string
	reads       int
	starts      int
}

func newDialogPane(t *testing.T, kind, dialog string, exitKeys []string) (*dialogPane, *herdr.Client) {
	t.Helper()
	p := &dialogPane{kind: kind, running: true, screen: "❯ \n", dialog: dialog, exitKeys: exitKeys}
	s := herdrtest.New(t)
	agent := func() (any, *herdrtest.Error) {
		if !p.running {
			return nil, &herdrtest.Error{Code: "agent_not_found", Message: "agent target w1:p1 not found"}
		}
		return map[string]any{"type": "agent_info", "agent": p.agentJSON()}, nil
	}
	s.Handle("agent.get", func(herdrtest.Request) (any, *herdrtest.Error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.exiting {
			if p.gets++; p.gets == p.exitAfter {
				p.running, p.exiting, p.screen = false, false, "$ \n"
			}
		}
		return agent()
	})
	s.Handle("agent.wait", func(herdrtest.Request) (any, *herdrtest.Error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.startScreen != "" {
			return nil, &herdrtest.Error{Code: "timeout", Message: "agent did not reach the state"}
		}
		return agent()
	})
	s.Handle("agent.prompt", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var q struct{ Text string }
		_ = json.Unmarshal(r.Params, &q)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.prompts = append(p.prompts, q.Text)
		switch {
		case !strings.HasPrefix(q.Text, "/"):
		case p.dialog == "":
			p.running, p.screen = false, "$ \n"
		default:
			p.screen, p.exiting = p.dialog, true
		}
		return map[string]any{"type": "agent_prompted", "agent": p.agentJSON()}, nil
	})
	s.Handle("agent.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var q struct {
			Source string
			Lines  int
		}
		_ = json.Unmarshal(r.Params, &q)
		p.mu.Lock()
		defer p.mu.Unlock()
		if q.Source != "recent" || q.Lines != 40 {
			return nil, &herdrtest.Error{Code: "invalid_request", Message: fmt.Sprintf("read %s %d", q.Source, q.Lines)}
		}
		p.reads++
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1",
			"source": "recent", "format": "text", "revision": 1, "text": p.screen, "truncated": false}}, nil
	})
	s.Handle("agent.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var q struct{ Keys []string }
		_ = json.Unmarshal(r.Params, &q)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.keys = append(p.keys, q.Keys)
		if p.exitKeys != nil && slices.Equal(q.Keys, p.exitKeys) && p.screen == p.dialog {
			p.running, p.exiting, p.screen = false, false, "$ \n"
		}
		return map[string]any{"type": "ok"}, nil
	})
	s.Handle("pane.process_info", func(herdrtest.Request) (any, *herdrtest.Error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		info := map[string]any{"pane_id": "w1:p1", "shell_pid": 100, "foreground_process_group_id": 100, "foreground_processes": []any{}}
		if p.running {
			info["foreground_process_group_id"] = 200
			info["foreground_processes"] = []any{map[string]any{"pid": 200, "name": p.kind, "argv": []string{p.kind}}}
		}
		return map[string]any{"type": "pane_process_info", "process_info": info}, nil
	})
	s.Handle("agent.start", func(herdrtest.Request) (any, *herdrtest.Error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.starts++
		p.running, p.screen = true, "❯ \n"
		if p.startScreen != "" {
			p.screen = p.startScreen
		}
		return map[string]any{"type": "agent_started", "argv": []string{p.kind}, "agent": p.agentJSON()}, nil
	})
	pane := map[string]any{"type": "pane_info", "pane": map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1",
		"terminal_id": "term_1", "agent_status": "idle", "focused": false, "revision": 1}}
	s.Handle("pane.get", func(herdrtest.Request) (any, *herdrtest.Error) { return pane, nil })
	s.Handle("pane.rename", func(herdrtest.Request) (any, *herdrtest.Error) { return pane, nil })
	s.Handle("pane.report_metadata", func(herdrtest.Request) (any, *herdrtest.Error) { return map[string]any{"type": "ok"}, nil })
	return p, herdr.New(s.SocketPath)
}

// agentJSON is the pane's agent; the caller holds p.mu.
func (p *dialogPane) agentJSON() map[string]any {
	return map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "term_1",
		"name": "icyknoll", "agent": p.kind, "agent_status": "idle", "focused": false, "revision": 1, "state_change_seq": 3, "cwd": "/p"}
}

// bindKind binds IcyKnoll of a kind in pane w1:p1, for the stub.
func bindKind(t *testing.T, w *world, kind string) bindings.Binding {
	t.Helper()
	b, err := w.env.Bindings.Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: "/p", ProjectID: "proj",
		Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			HerdrAlias: "icyknoll", Kind: kind, LaunchID: "op-x.0", NativeSession: &bindings.NativeSession{Value: "sess-icyknoll"}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Claude Code's /exit opens 'Background work is running' when the agent
// has background work, such as a wait for mail (nhm-m7x). Recycle
// chooses 'Exit and stop tasks' once, with a warning; the agent exits
// and its successor starts.
func TestRecycleAnswersTheBackgroundWorkDialog(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := bindKind(t, w, "claude")
	p, c := newDialogPane(t, "claude", backgroundWork, []string{"enter"})
	w.env.Herdr = c
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "Continue the bead."})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if !slices.ContainsFunc(res.Warnings, func(s string) bool {
		return strings.HasPrefix(s, "/exit opened Claude's 'Background work is running' dialog") && strings.HasSuffix(s, `recycle answered "Exit and stop tasks"`)
	}) {
		t.Fatalf("warnings = %q", res.Warnings)
	}
	if fmt.Sprint(p.prompts, p.keys, p.starts) != "[/exit] [[enter]] 1" || len(w.sent) != 1 {
		t.Fatalf("prompts %v, keys %v, starts %d, kickoffs %d", p.prompts, p.keys, p.starts, len(w.sent))
	}
}

// A dialog that recycle does not answer, Claude's with the cursor off
// 'Exit and stop tasks', fails the stop at once and names it. No key is
// sent and nothing starts.
func TestRecycleNamesAnExitDialogItDoesNotAnswer(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := bindKind(t, w, "claude")
	stay := strings.Replace(strings.Replace(backgroundWork, "❯ 1.", "  1.", 1), "  3. Stay", "❯ 3. Stay", 1)
	p, h := newDialogPane(t, "claude", stay, nil)
	w.env.Herdr = h
	began := w.clock
	res, err := Recycle(ctx, w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	want := "IcyKnoll did not exit: /exit opened Claude's 'Background work is running' dialog (the agent has background tasks), " +
		"which recycle does not answer; nothing started: answer it in pane w1:p1"
	if err == nil || res.Stage != StageStop || res.Error != want {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if len(p.keys) != 0 || p.starts != 0 || w.clock.Sub(began) >= stopWait {
		t.Fatalf("keys %v, starts %d, after %s", p.keys, p.starts, w.clock.Sub(began))
	}
	if nb, _ := w.env.Bindings.Get(ctx, b.ID); nb.Generation != b.Generation {
		t.Fatal("the generation moved")
	}
}

// An agent whose output quotes the dialog above its prompt box, and
// that takes a few seconds to exit, has no dialog open: recycle reads
// the pane, sends no key, warns of nothing, and starts the successor
// once the agent exits.
func TestRecycleIgnoresAQuotedExitDialog(t *testing.T) {
	quote := "● The bead says that /exit opened this dialog:\n\n" + strings.TrimPrefix(backgroundWork, "\n") +
		"\n" + strings.Repeat("─", 94) + "\n❯ \n" + strings.Repeat("─", 94) + "\n  ⏵⏵ bypass permissions on · 1 shell · ↓ to manage\n"
	w := newWorld(t)
	b := bindKind(t, w, "claude")
	p, c := newDialogPane(t, "claude", quote, nil)
	p.exitAfter = 6
	w.env.Herdr = c
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err != nil || res.Stage != StageDone || res.Generation != b.Generation+1 {
		t.Fatalf("recycle = %+v %v", res, err)
	}
	if p.reads < 2 || len(p.keys) != 0 || p.starts != 1 || slices.ContainsFunc(res.Warnings, func(s string) bool { return strings.Contains(s, "dialog") }) {
		t.Fatalf("reads %d, keys %v, starts %d, warnings %q", p.reads, p.keys, p.starts, res.Warnings)
	}
}

// A dialog that stays after recycle answered it fails the stop at its
// deadline, named, after one answer.
func TestRecycleNamesAnExitDialogThatStays(t *testing.T) {
	w := newWorld(t)
	bindKind(t, w, "claude")
	p, c := newDialogPane(t, "claude", backgroundWork, nil)
	w.env.Herdr = c
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	want := "IcyKnoll did not exit within 30s: the pane still shows Claude's 'Background work is running' dialog (the agent has background tasks) " +
		"after recycle answered it; nothing started: answer it in pane w1:p1"
	if err == nil || res.Stage != StageStop || res.Error != want || fmt.Sprint(p.keys) != "[[enter]]" || p.starts != 0 {
		t.Fatalf("recycle = %+v %v, keys %v, starts %d", res, err, p.keys, p.starts)
	}
}

// The successor's ready wait runs out at a startup dialog that herdr
// does not report: the start's failure names it, as spawn's does
// (nhm-5as).
func TestRecycleNamesTheSuccessorsDialog(t *testing.T) {
	w := newWorld(t)
	bindKind(t, w, "codex")
	p, c := newDialogPane(t, "codex", "", nil)
	p.startScreen = "  Hooks need review\n  1 hook is new or changed.\n\n› 1. Review hooks\n  2. Trust all and continue\n" +
		"  3. Continue without trusting (hooks won't run)\n"
	w.env.Herdr = c
	res, err := Recycle(context.Background(), w.env, RecycleRequest{Target: "IcyKnoll", Prompt: "x"})
	if err == nil || res.Stage != StageStart || !strings.Contains(res.Error, "the agent waits at Codex's 'Hooks need review' dialog") ||
		!strings.Contains(res.Error, "answer it in pane w1:p1") || !strings.Contains(res.Error, "agent did not reach the state") {
		t.Fatalf("recycle = %+v %v", res, err)
	}
}
