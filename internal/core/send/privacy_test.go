package send

import (
	"context"
	"testing"

	"github.com/arosl/nhm/internal/core/privacy"
	"github.com/arosl/nhm/internal/herdr"
)

// ntm skips the prompt history, silently, for a session in privacy mode
// (nhm-2p6.4.16): a send that reaches an agent of a private workspace
// still goes out, and writes no history line; --allow-persist lets it.
func TestSendSkipsHistoryInPrivacyMode(t *testing.T) {
	prev := privacy.GetDefaultManager()
	m := privacy.New(privacy.DefaultConfig())
	privacy.SetDefaultManager(m)
	t.Cleanup(func() { privacy.SetDefaultManager(prev) })
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	m.RegisterSession("w1", true, false)
	res, err := Send(ctx, w.env, base(Request{Message: "Fix it.", OperationID: "op-p", WhenAny: true}))
	if err != nil || len(res.Targets) != 1 || res.Targets[0].Status != StatusSubmitted || len(w.hist) != 0 {
		t.Fatalf("private = %+v %v, history %d", res.Targets, err, len(w.hist))
	}
	m.RegisterSession("w1", true, true)
	if _, err := Send(ctx, w.env, base(Request{Message: "Fix it again.", OperationID: "op-q", WhenAny: true})); err != nil ||
		len(w.hist) != 1 || w.hist[0].OperationID != "op-q" {
		t.Fatalf("--allow-persist = %v, history %+v", err, w.hist)
	}
}
