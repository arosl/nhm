package cli

import (
	"strings"
	"testing"
)

// TestSendTypesIntoABusyAgentNotABlockedOne: nhm send and --robot-send
// type into a busy agent by default, as ntm's send does (the operator's
// ruling, nhm-r0v). An agent at a dialog (herdr's blocked) is still
// refused by both, and --when ready keeps nhm's no-interrupt send.
func TestSendTypesIntoABusyAgentNotABlockedOne(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	sock := s.SocketPath
	h.mu.Lock()
	h.status["w1:p1"], h.status["w1:p2"] = "working", "blocked"
	h.mu.Unlock()

	if code, out, errOut := run("send", "swarm", "first", "--name", "IcyKnoll", "--socket", sock); code != 0 ||
		strings.Join(h.sent(), ";") != "w1:p1=first" {
		t.Fatalf("busy, the default = %d %s %s %v", code, out, errOut, h.sent())
	}
	if code, out, _ := run("send", "swarm", "second", "--name", "IcyKnoll", "--when", "ready", "--socket", sock); code != exitAdmission ||
		!strings.Contains(out, "refused (busy)") || len(h.sent()) != 1 {
		t.Fatalf("busy, --when ready = %d %s", code, out)
	}
	if code, out, _ := run("send", "swarm", "third", "--name", "BlueLake", "--socket", sock); code != exitAdmission ||
		!strings.Contains(out, "refused (blocked)") || len(h.sent()) != 1 {
		t.Fatalf("blocked = %d %s", code, out)
	}

	busy, code := robotJSON(t, "--robot-send=swarm", "--panes=0.1", "--msg=robot", "--socket", sock)
	if got := h.sent(); code != 0 || busy["success"] != true || got[len(got)-1] != "w1:p1=robot" {
		t.Fatalf("robot, busy = %d %v %v", code, busy, got)
	}
	blocked, code := robotJSON(t, "--robot-send=swarm", "--panes=1.1", "--msg=robot", "--socket", sock)
	if code != 1 || blocked["success"] != false || len(h.sent()) != 2 {
		t.Fatalf("robot, blocked = %d %v %v", code, blocked, h.sent())
	}
}
