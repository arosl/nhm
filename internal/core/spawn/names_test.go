package spawn

import (
	"context"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// herdr refuses a taken or invalid agent name before it launches
// anything: the agent fails outright, its empty pane and its binding are
// cleaned up, and nothing reads "may have started" (nhm-4xt).
func TestNameRefusalIsNoStart(t *testing.T) {
	ctx := context.Background()
	for _, code := range []string{herdr.CodeAgentNameTaken, herdr.CodeInvalidAgentName} {
		w := newWorld(t)
		w.h.startErr[1] = &herdr.Error{Code: code, Message: "refused"}
		res, err := Spawn(ctx, w.env, req(Want{Kind: "claude", Count: 1}))
		if err != nil {
			t.Fatal(err)
		}
		tg := res.Targets[0]
		if tg.Status != StatusFailed || strings.Contains(tg.Cleanup, "may have started") || !strings.Contains(tg.Cleanup, "closed its empty pane") {
			t.Errorf("%s: target = %+v", code, tg)
		}
		if b, err := w.env.Bindings.Get(ctx, tg.BindingID); err != nil || b.Status != bindings.Retired {
			t.Errorf("%s: binding = %+v %v", code, b, err)
		}
	}
}
