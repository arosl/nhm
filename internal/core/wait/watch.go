package wait

import (
	"context"

	"github.com/arosl/nhm/internal/herdr"
)

// Subscriber opens an event stream; *herdr.Client has it.
type Subscriber interface {
	Subscribe(ctx context.Context, subs ...herdr.Subscription) (*herdr.Stream, error)
}

// StreamWatch is an Env.Watch on herdr's events.subscribe: one
// subscription to the pane's agent state changes. herdr's events.wait
// needs the state to wait for, so it cannot wake the loop on any change.
// The channel is closed when the stream ends; events are not durable,
// and the loop then reads the agent again.
func StreamWatch(s Subscriber) func(ctx context.Context, paneID string) <-chan struct{} {
	return func(ctx context.Context, paneID string) <-chan struct{} {
		st, err := s.Subscribe(ctx, herdr.SubscribeAgentStatus(paneID))
		if err != nil {
			// No stream: the loop polls, and tries again next round.
			return nil
		}
		ch := make(chan struct{}, 1)
		go func() {
			defer close(ch)
			defer st.Close()
			for {
				if _, err := st.Next(); err != nil {
					return
				}
				select {
				case ch <- struct{}{}:
				default:
					// A wake-up is pending already.
				}
			}
		}()
		return ch
	}
}
