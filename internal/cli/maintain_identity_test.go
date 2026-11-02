package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
	"github.com/arosl/nhm/internal/store"
)

// A maintain pass retires the binding of an identity that Agent Mail
// reports deregistered, and drops the operator mark of a pane that herdr
// no longer has (nhm-h85k).
func TestMaintainReleasesGoneIdentitiesAndDropsGoneOperatorMarks(t *testing.T) {
	s, inst, _ := newSendInstance(t)
	mail := newFakeAgentMail(t)
	t.Setenv("HERDR_SOCKET_PATH", s.SocketPath)
	ownNhm(t, inst)
	pane := func(id string) map[string]any {
		return map[string]any{"pane_id": id, "tab_id": "w1:t1", "workspace_id": "w1", "terminal_id": "t-" + id,
			"agent_status": "idle", "focused": false, "revision": 1}
	}
	s.Handle("session.snapshot", func(herdrtest.Request) (any, *herdrtest.Error) {
		return map[string]any{"type": "session_snapshot", "snapshot": map[string]any{
			"protocol": 22, "version": "0.9.3", "workspaces": []any{}, "layouts": []any{}, "tabs": []any{},
			"panes": []any{pane("w1:p1"), pane("w1:p2"), pane("w1:p3")}, "agents": []any{}}}, nil
	})
	ctx := context.Background()
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bs := bindings.NewStore(db)
	for _, p := range []string{"w1:p3", "w9:p9"} {
		if _, err := bs.MarkOperator(ctx, bindings.OperatorPane{PaneID: p}); err != nil {
			t.Fatal(err)
		}
	}
	mail.who["BlueLake"] = map[string]any{"name": "BlueLake", "deregistered_at": "2026-10-12T00:00:00Z"}

	code, out, errOut := runVerb(runMaintain, "--once", "--json")
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	env := decodeEnvelope(t, out)
	res := env["result"].(map[string]any)
	released := res["released"].([]any)
	if len(released) != 1 || released[0].(map[string]any)["mail_name"] != "BlueLake" || released[0].(map[string]any)["state"] != "deregistered" {
		t.Fatalf("released = %v", res["released"])
	}
	dropped := res["operator_marks_dropped"].([]any)
	if len(dropped) != 1 || dropped[0].(map[string]any)["pane_id"] != "w9:p9" {
		t.Fatalf("dropped = %v", res["operator_marks_dropped"])
	}
	for _, w := range env["warnings"].([]any) {
		if strings.Contains(w.(string), "Agent Mail identit") || strings.Contains(w.(string), "identity check") {
			t.Fatalf("warning: %v", w)
		}
	}
	list, err := bs.List(ctx, bindings.Filter{MailName: "BlueLake"})
	if err != nil || len(list) != 1 || list[0].Status != bindings.Retired || list[0].StatusReason != "the Agent Mail identity was deregistered" {
		t.Fatalf("BlueLake = %+v %v", list, err)
	}
	if b, err := bs.ActiveByPane(ctx, "w1:p1"); err != nil || b.MailName != "IcyKnoll" {
		t.Fatalf("IcyKnoll = %+v %v", b, err)
	}
	if ok, err := bs.IsOperator(ctx, "w1:p3"); err != nil || !ok {
		t.Fatalf("w1:p3's mark went: %v %v", ok, err)
	}

	// The text names what the pass did.
	mail.who["IcyKnoll"] = map[string]any{"name": "IcyKnoll", "retired_at": "2026-10-12T00:00:00Z"}
	code, out, errOut = runVerb(runMaintain, "--once")
	if code != exitOK || !strings.Contains(out, "released: IcyKnoll (w1:p1): its Agent Mail identity was retired") {
		t.Fatalf("text = %d %q %q", code, out, errOut)
	}
}
