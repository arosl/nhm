package herdr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

func subscribeStub(t *testing.T) (*herdrtest.Server, *Client) {
	t.Helper()
	s, c := newStub(t)
	answer(s, "events.subscribe", map[string]any{"type": "subscription_started"})
	return s, c
}

// emitWhenSubscribed emits to the test's one stream. Subscribe returns
// after the subscription_started reply, and the stub registers a
// subscriber before it writes that reply, so no wait is needed.
func emitWhenSubscribed(t *testing.T, s *herdrtest.Server, kind string, data any) {
	t.Helper()
	if n := s.Emit(kind, data); n != 1 {
		t.Fatalf("Emit(%s) wrote to %d subscriptions, want 1", kind, n)
	}
}

func TestSubscribeAndDecodeBothSpellings(t *testing.T) {
	s, c := newStub(t)
	params := answer(s, "events.subscribe", map[string]any{"type": "subscription_started"})
	st, err := c.Subscribe(context.Background(), SubscribeAgentStatus("w1:p1"), SubscribeAgentDetected(), SubscribePaneExited())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	assertJSON(t, *params, `{"subscriptions":[{"type":"pane.agent_status_changed","pane_id":"w1:p1"},{"type":"pane.agent_detected"},{"type":"pane.exited"}]}`)

	emitWhenSubscribed(t, s, "pane.agent_status_changed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent_status": "working", "agent": "claude"})
	s.Emit("pane_agent_detected", map[string]any{"type": "pane_agent_detected", "pane_id": "w1:p1", "workspace_id": "w1", "agent": "claude", "released": true, "final_status": "idle"})
	s.Emit("pane_exited", map[string]any{"type": "pane_exited", "pane_id": "w1:p1", "workspace_id": "w1"})

	ev, err := st.Next()
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ev.AgentStatusChanged()
	if err != nil || ev.Kind != EventPaneAgentStatusChangedSub || sc.AgentStatus != StatusWorking {
		t.Fatalf("status event = %+v %+v %v", ev, sc, err)
	}
	ev, err = st.Next()
	if err != nil {
		t.Fatal(err)
	}
	ad, err := ev.AgentDetected()
	if err != nil || !ad.Released || ad.FinalStatus != StatusIdle {
		t.Fatalf("detected event = %+v %v", ad, err)
	}
	if _, err := ev.AgentStatusChanged(); err == nil {
		t.Fatal("a detected event decoded as a status change")
	}
	ev, err = st.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ref, err := ev.PaneRef(); err != nil || ref.PaneID != "w1:p1" {
		t.Fatalf("exited event = %+v %v", ref, err)
	}
}

func TestEveryEndOfAStreamIsLoss(t *testing.T) {
	s, c := subscribeStub(t)
	st, err := c.Subscribe(context.Background(), SubscribeAgentDetected())
	if err != nil {
		t.Fatal(err)
	}
	emitWhenSubscribed(t, s, "pane_created", map[string]any{"type": "pane_created", "pane": paneFixture("w1:p4")})
	ev, err := st.Next()
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ev.PaneCreated(); err != nil || p.PaneID != "w1:p4" {
		t.Fatalf("pane_created = %+v %v", p, err)
	}
	s.DropSubscriptions()
	_, err = st.Next()
	if !errors.Is(err, ErrStreamLost) {
		t.Fatalf("err = %v", err)
	}
	if _, again := st.Next(); !errors.Is(again, ErrStreamLost) {
		t.Fatalf("second Next = %v", again)
	}
	st.Close() // a second close is harmless
}

func TestCancelEndsAStream(t *testing.T) {
	_, c := subscribeStub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := c.Subscribe(ctx, SubscribeAgentDetected())
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = st.Next()
	if !errors.Is(err, ErrStreamLost) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// A stream outlives the client's call timeout: the deadline bounds only
// the start.
func TestStreamOutlivesTheCallTimeout(t *testing.T) {
	s, c := subscribeStub(t)
	c.Timeout = 50 * time.Millisecond
	st, err := c.Subscribe(context.Background(), SubscribeAgentDetected())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	time.Sleep(150 * time.Millisecond)
	emitWhenSubscribed(t, s, "pane_exited", map[string]any{"type": "pane_exited", "pane_id": "w1:p1", "workspace_id": "w1"})
	if _, err := st.Next(); err != nil {
		t.Fatalf("Next after the call timeout: %v", err)
	}
}

func TestSubscribeRefusedForAnUnknownPane(t *testing.T) {
	s, c := newStub(t)
	s.Handle("events.subscribe", func(r herdrtest.Request) (any, *herdrtest.Error) {
		return nil, &herdrtest.Error{Code: "pane_not_found", Message: "pane w1:p2 not found"}
	})
	_, err := c.Subscribe(context.Background(), SubscribeAgentStatus("w1:p2"))
	if ErrorCode(err) != CodePaneNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownEventKindStaysExplicit(t *testing.T) {
	c := New(rawServer(t,
		`{"id":"nhm-1","result":{"type":"subscription_started"}}`,
		`{"event":"pane_teleported","data":{"pane_id":"w1:p1","to":"w2"}}`,
	))
	st, err := c.Subscribe(context.Background(), SubscribeAgentDetected())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ev, err := st.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != "pane_teleported" || ev.Kind.Valid() || string(ev.Data) != `{"pane_id":"w1:p1","to":"w2"}` {
		t.Fatalf("event = %+v", ev)
	}
	if _, err := ev.AgentDetected(); err == nil {
		t.Fatal("an unknown kind decoded as pane_agent_detected")
	}
}

// If a later herdr sends an error on the stream (an events_lost, say),
// it ends the stream as a loss and keeps its code.
func TestErrorOnTheStreamIsLoss(t *testing.T) {
	c := New(rawServer(t,
		`{"id":"nhm-1","result":{"type":"subscription_started"}}`,
		`{"id":"nhm-1","error":{"code":"events_lost","message":"subscriber lagged"}}`,
	))
	st, err := c.Subscribe(context.Background(), SubscribeAgentDetected())
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Next()
	if !errors.Is(err, ErrStreamLost) || ErrorCode(err) != "events_lost" {
		t.Fatalf("err = %v", err)
	}
}
