package hooks

import (
	"os"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core"
	"github.com/arosl/nhm/internal/core/bindings"
)

// A registration hook binds a hand-started agent whose shell still names
// another agent in AGENT_MAIL_AGENT (a pane spawn made, reused by hand):
// the bind stands, and the reconcile warns (nhm-a73). An agent whose
// shell names itself gets no warning.
func TestRegistrationWarnsOnAnotherAgentsMailVar(t *testing.T) {
	h := newHarness(t, core.OwnerNhm)
	old := bindings.ProcEnviron
	envs := map[int64]map[string]string{500: {"AGENT_MAIL_AGENT": "OldFox"}, 600: {"AGENT_MAIL_AGENT": "BlueLake"}}
	bindings.ProcEnviron = func(pid int64) (map[string]string, error) {
		if e, ok := envs[pid]; ok {
			return e, nil
		}
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { bindings.ProcEnviron = old })
	project := t.TempDir()

	h.world.set("w1:p1", pane{kind: "claude", session: "sess-a", pid: 500})
	res := h.hook(registration("sess-a", "IcyKnoll", project), "e1")
	if b := h.binding("IcyKnoll"); b.Status != bindings.Active || b.PaneID != "w1:p1" {
		t.Fatalf("binding = %+v", b)
	}
	if w := strings.Join(res.Report.Warnings, " | "); !strings.Contains(w, "AGENT_MAIL_AGENT=OldFox") || !strings.Contains(w, "not IcyKnoll") {
		t.Fatalf("warnings = %q", w)
	}

	h.vars["HERDR_PANE_ID"] = "w1:p2"
	h.world.set("w1:p2", pane{kind: "claude", session: "sess-b", pid: 600})
	res = h.hook(registration("sess-b", "BlueLake", project), "e2")
	if b := h.binding("BlueLake"); b.Status != bindings.Active || b.PaneID != "w1:p2" {
		t.Fatalf("binding = %+v", b)
	}
	for _, w := range res.Report.Warnings {
		if strings.Contains(w, "AGENT_MAIL_AGENT") {
			t.Fatalf("own name warned: %q", w)
		}
	}
}
