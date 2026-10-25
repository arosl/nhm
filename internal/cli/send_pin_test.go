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
	if code != 0 || out != "Sent to 1 pane(s)\n" {
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
	// The refused target is ntm's failed delivery (exit 1); the receipt
	// under _nhm says why.
	code, out, _ = run("send", "swarm", "packet", "--if-generation", pin, "--op-id", "op-pin2", "--socket", sock, "--json")
	r := decodeSendResult(t, out)
	if code != exitError || r.Success || r.Failed != 1 || r.ErrorCode != "SEND_FAILED" || r.NHM == nil || len(r.NHM.Receipts) != 1 ||
		r.NHM.Receipts[0].Status != "refused" || r.NHM.Receipts[0].ErrorCode != "generation_changed" {
		t.Fatalf("stale pin = %d %s", code, out)
	}
	if len(h.sent()) != 1 {
		t.Fatalf("a prompt was typed: %v", h.sent())
	}
	// A dry run names the refusal as a warning and would send nothing.
	code, out, errOut = run("send", "swarm", "packet", "--if-generation", pin, "--dry-run", "--socket", sock)
	if code != 0 || !strings.Contains(out, "Would send 0 prompt(s):") ||
		!strings.Contains(errOut, "Warning: pane 0.1 (IcyKnoll): refused: ") || !strings.Contains(errOut, "expected generation 1, the binding is at generation 2") {
		t.Fatalf("stale pin, dry run = %d %q %q", code, out, errOut)
	}

	// With a selector, a selected agent that is not pinned is refused,
	// and a pin the selector does not reach is reported.
	code, out, _ = run("send", "swarm", "packet", "--name", "BlueLake", "--if-generation", icy.ID+":2", "--op-id", "op-pin3",
		"--socket", sock, "--json")
	r = decodeSendResult(t, out)
	codes := map[string]bool{}
	if r.NHM != nil {
		for _, rc := range r.NHM.Receipts {
			codes[rc.ErrorCode] = true
		}
	}
	if code != exitError || !codes["not_reviewed"] || !codes["not_selected"] || len(h.sent()) != 1 {
		t.Fatalf("pin with a selector = %d %s", code, out)
	}

	// Every pinned binding gone: refused before the claim (ntm's error,
	// exit 1), and never widened to the other agents.
	code, _, _ = run("send", "swarm", "packet", "--if-generation", "bnd-gone:1", "--op-id", "op-pin5", "--socket", sock)
	if code != exitError || len(h.sent()) != 1 {
		t.Fatalf("only gone pins = %d, prompts %v", code, h.sent())
	}

	for _, bad := range []string{"bnd-x", "bnd-x:0", "bnd-x:two", ":3", "bnd-x:1,bnd-x:2"} {
		if code, _, _ := run("send", "swarm", "x", "--if-generation", bad, "--socket", sock); code != exitError {
			t.Errorf("--if-generation %q = %d, want %d", bad, code, exitError)
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
