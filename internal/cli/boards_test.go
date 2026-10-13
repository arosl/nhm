package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/uilaunch"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// herdr names a claude agent in w1:p2, and w1:p2 is nhm's live board by
// its verified launch record (plan-v4 section 7). The verbs leave it out:
// no status row, no live agent in quota's count, and adopt refuses it
// (exit 2). Before the record, the same pane is an agent to all three.
func TestTheBoardIsNoAgent(t *testing.T) {
	s, inst := cliInstance(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	s.Handle("pane.process_info", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(r.Params, &p)
		return map[string]any{"type": "pane_process_info", "process_info": map[string]any{"pane_id": p.PaneID, "shell_pid": 4300}}, nil
	})
	s.Handle("agent.list", func(herdrtest.Request) (any, *herdrtest.Error) {
		agent := func(pane string) map[string]any {
			return map[string]any{"pane_id": pane, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t-" + pane,
				"agent": "claude", "agent_status": "idle", "focused": false, "revision": 1}
		}
		return map[string]any{"type": "agent_list", "agents": []any{agent("w1:p1"), agent("w1:p2")}}, nil
	})
	counts := func() (rows int, claude float64) {
		t.Helper()
		code, out, errOut := run("status", "--no-mail", "--json", "--socket", s.SocketPath)
		if code != 0 {
			t.Fatalf("status = %d\n%s%s", code, out, errOut)
		}
		rows = len(envelopeOf(t, out)["result"].(map[string]any)["agents"].([]any))
		code, out, errOut = runVerb(runQuotaView, "--json", "--socket", s.SocketPath)
		if code != 0 {
			t.Fatalf("quota = %d\n%s%s", code, out, errOut)
		}
		for _, p := range envelopeOf(t, out)["result"].(map[string]any)["pools"].([]any) {
			if pool := p.(map[string]any); pool["kind"] == "claude" {
				claude = pool["agents"].(float64)
			}
		}
		return rows, claude
	}
	if rows, claude := counts(); rows != 2 || claude != 2 {
		t.Fatalf("before the record: %d rows, %v claude agents", rows, claude)
	}

	data, err := json.Marshal(uilaunch.Launch{Token: "bl-test", InstanceID: inst.ID, WorkspaceID: "w1", State: uilaunch.Registered,
		OpenedAt: time.Now().UTC(), PaneID: "w1:p2", PID: 4300})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(inst.Dir, uilaunch.Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inst.Dir, uilaunch.Dir, "board-w1.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if rows, claude := counts(); rows != 1 || claude != 1 {
		t.Fatalf("with the board: %d rows, %v claude agents", rows, claude)
	}
	project := t.TempDir()
	code, out, _ := run("adopt", "BlueLake", "--pane", "w1:p2", "--project", project, "--socket", s.SocketPath, "--json")
	if env := decodeEnvelope(t, out); code != 2 || !strings.Contains(env["error"].(string), "nhm's board") {
		t.Fatalf("adopt of the board = %d %v", code, env["error"])
	}
}
