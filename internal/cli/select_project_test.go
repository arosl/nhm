package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// TestInterruptAndWaitRefuseANameOfTwoProjects: the swarm holds an
// IcyKnoll of /p and one of another project. `--name IcyKnoll` alone
// interrupts nobody and waits for nobody; with --project it picks that
// project's agent (nhm-y2e).
func TestInterruptAndWaitRefuseANameOfTwoProjects(t *testing.T) {
	s, inst, _ := waitInstance(t, func(pane string) map[string]any {
		return agentMap(pane, "claude", "idle", 4, 0)
	})
	project := t.TempDir()
	scope, err := projectScope(context.Background(), inst.ID, project)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	other, err := bindings.NewStore(db).Create(context.Background(), bindings.New{MailName: "IcyKnoll", MailProjectKey: scope.MailProjectKey,
		ProjectID: scope.ProjectID, Provenance: bindings.Created, Occupant: bindings.Occupant{WorkspaceID: "w1", TabID: "w1:t1",
			PaneID: "w1:p3", HerdrAlias: "icyknoll-2", Kind: "claude"}})
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.SwitchOwner(context.Background(), inst.Dir, core.OwnerNhm, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []string
	s.Handle("agent.send_keys", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		sent = append(sent, p.Target)
		mu.Unlock()
		return map[string]any{"type": "ok"}, nil
	})

	code, _, errOut := runVerb(runInterrupt, "swarm", "--name", "IcyKnoll", "--include-self", "--socket", s.SocketPath)
	if code != exitRefused || !strings.Contains(errOut, "name one by its slot") {
		t.Fatalf("interrupt without --project = %d %s", code, errOut)
	}
	code, out, errOut := runVerb(runInterrupt, "swarm", "--name", "IcyKnoll", "--project", project, "--include-self", "--socket", s.SocketPath)
	if code != exitOK || !strings.Contains(out, "IcyKnoll ("+other.Slot+"): sent esc") {
		t.Fatalf("interrupt with --project = %d %s %s", code, out, errOut)
	}
	mu.Lock()
	if strings.Join(sent, " ") != "w1:p3" {
		t.Errorf("keys went to %v, want only w1:p3", sent)
	}
	mu.Unlock()

	code, _, errOut = runVerb(runWait, "swarm", "--name", "IcyKnoll", "--until", "idle", "--socket", s.SocketPath)
	if code != exitRefused || !strings.Contains(errOut, "name one by its slot") {
		t.Fatalf("wait without --project = %d %s", code, errOut)
	}
	code, out, errOut = runVerb(runWait, "swarm", "--name", "IcyKnoll", "--project", project, "--until", "idle", "--socket", s.SocketPath, "--json")
	res, _ := decodeEnvelope(t, out)["result"].(map[string]any)
	targets, _ := res["targets"].([]any)
	if code != exitOK || len(targets) != 1 || !strings.Contains(out, "w1:p3") {
		t.Fatalf("wait with --project = %d %s %s", code, out, errOut)
	}
}
