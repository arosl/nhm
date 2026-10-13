package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/store"
)

// TestSendIfGeneration is nhm-47w: a pinned send goes to the pinned
// binding while it is at the recorded generation; after a recycle in the
// same pane, the same pinned send is refused with generation_changed and
// nothing is typed.
func TestSendIfGeneration(t *testing.T) {
	s, inst, h := newSendInstance(t)
	sock := s.SocketPath
	ownNhm(t, inst)
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	ctx := context.Background()
	icy, err := bs.ActiveByName(ctx, "/p", "IcyKnoll")
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("%s:%d", icy.ID, icy.Generation)

	// The pin alone is the selection: IcyKnoll only, not every agent.
	code, out, errOut := run("send", "swarm", "packet", "--if-generation", pin, "--op-id", "op-pin1", "--socket", sock)
	if code != 0 || !strings.Contains(out, "IcyKnoll (0.1): submitted") || strings.Contains(out, "BlueLake") {
		t.Fatalf("matching pin = %d %s %s", code, out, errOut)
	}
	if got := h.sent(); len(got) != 1 || got[0] != "w1:p1=packet" {
		t.Fatalf("prompts = %v", got)
	}
	h.finish("w1:p1")

	// A recycle in the same pane: same binding, next generation.
	if _, err := bs.Occupy(ctx, icy.ID, icy.Generation, bindings.Occupant{WorkspaceID: "w1", TabID: icy.TabID,
		PaneID: icy.PaneID, HerdrAlias: icy.HerdrAlias, Kind: icy.Kind}); err != nil {
		t.Fatal(err)
	}
	code, out, _ = run("send", "swarm", "packet", "--if-generation", pin, "--op-id", "op-pin2", "--socket", sock, "--json")
	tg := decodeEnvelope(t, out)["result"].(map[string]any)["targets"].([]any)
	first := tg[0].(map[string]any)
	if code != exitAdmission || len(tg) != 1 || first["submission"] != "refused" || first["error_code"] != "generation_changed" {
		t.Fatalf("stale pin = %d %v", code, tg)
	}
	if len(h.sent()) != 1 {
		t.Fatalf("a prompt was typed: %v", h.sent())
	}
	code, out, _ = run("send", "swarm", "packet", "--if-generation", pin, "--dry-run", "--socket", sock)
	if code != 0 || !strings.Contains(out, "refused") || !strings.Contains(out, "expected generation 1, the binding is at generation 2") {
		t.Fatalf("stale pin, dry run = %d %s", code, out)
	}

	// With a selector, a selected agent that is not pinned is refused,
	// and a pin the selector does not reach is reported.
	code, out, _ = run("send", "swarm", "packet", "--name", "BlueLake", "--if-generation", icy.ID+":2", "--op-id", "op-pin3",
		"--socket", sock, "--json")
	tg = decodeEnvelope(t, out)["result"].(map[string]any)["targets"].([]any)
	codes := map[string]bool{}
	for _, x := range tg {
		codes[fmt.Sprint(x.(map[string]any)["error_code"])] = true
	}
	if code != exitAdmission || !codes["not_reviewed"] || !codes["not_selected"] || len(h.sent()) != 1 {
		t.Fatalf("pin with a selector = %d %v", code, tg)
	}

	for _, bad := range []string{"bnd-x", "bnd-x:0", "bnd-x:two", ":3", "bnd-x:1,bnd-x:2"} {
		if code, _, _ := run("send", "swarm", "x", "--if-generation", bad, "--socket", sock); code != exitRefused {
			t.Errorf("--if-generation %q = %d, want %d", bad, code, exitRefused)
		}
	}

	// The robot form takes the same pin.
	resp, _ := robotJSON(t, "--robot-send=swarm", "--if-generation="+pin, "--msg=packet", "--op-id=op-pin4", "--socket="+sock)
	if resp["success"] == true || !strings.Contains(fmt.Sprint(resp), "generation_changed") || len(h.sent()) != 1 {
		t.Fatalf("robot stale pin = %v", resp)
	}
	resp, _ = robotJSON(t, "--robot-send=swarm", "--if-generation=bnd-x", "--msg=packet", "--socket="+sock)
	if resp["success"] == true || resp["error_code"] != "INVALID_FLAG" {
		t.Fatalf("robot bad pin = %v", resp)
	}
}
