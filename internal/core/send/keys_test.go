package send

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/herdr"
)

// keysHerdr records the keys that Keys sends, one call per key.
type keysHerdr struct {
	*fakeHerdr
	mu   sync.Mutex
	sent []string
	err  error
}

func (k *keysHerdr) AgentSendKeys(_ context.Context, pane string, keys ...string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return k.err
	}
	k.sent = append(k.sent, pane+" "+strings.Join(keys, "+"))
	return nil
}

func keysWorld(t *testing.T) (*world, *keysHerdr, KeysEnv) {
	t.Helper()
	w := newWorld(t)
	h := &keysHerdr{fakeHerdr: w.fake}
	return w, h, KeysEnv{Herdr: h, Bindings: w.env.Bindings, InstanceDir: w.env.InstanceDir}
}

// Keys sends each key on its own, after the gap and the caller's check,
// with the agent's target lock held throughout.
func TestKeysSendsUnderTheTargetLock(t *testing.T) {
	w, h, env := keysWorld(t)
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusBlocked)
	var before []int
	var busy error
	sent, err := Keys(context.Background(), env, KeysRequest{PaneID: "w1:p1", Binding: &b, Keys: []string{"down", "enter"},
		Gap: time.Millisecond, Before: func(i int) error {
			before = append(before, i)
			if i == 1 {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				unlock, err := LockTarget(ctx, env.InstanceDir, b.ID)
				if err == nil {
					unlock()
				}
				busy = err
			}
			return nil
		}})
	if err != nil || sent != 2 || !reflect.DeepEqual(h.sent, []string{"w1:p1 down", "w1:p1 enter"}) || !reflect.DeepEqual(before, []int{0, 1}) {
		t.Fatalf("Keys = %d, %v; sent %v, before %v", sent, err, h.sent, before)
	}
	if !errors.Is(busy, ErrTargetBusy) {
		t.Errorf("the lock during the keys: %v", busy)
	}
	// The lock is free again.
	unlock, err := LockTarget(context.Background(), env.InstanceDir, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

// Keys types nothing when the binding moved on, when the pane holds no
// agent or an agent of another kind, and stops where the caller's check
// fails or herdr refuses a key.
func TestKeysRefuses(t *testing.T) {
	w, h, env := keysWorld(t)
	b := w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusBlocked)
	old := b
	old.Generation--
	codex := b
	codex.Kind = "codex"
	w.fake.add("w1:p3", "", "", herdr.StatusUnknown, 1)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		req  KeysRequest
		want string
	}{
		{"changed binding", KeysRequest{PaneID: "w1:p1", Binding: &old, Keys: []string{"1"}}, "the binding changed since selection"},
		{"moved pane", KeysRequest{PaneID: "w1:p2", Binding: &b, Keys: []string{"1"}}, "the binding changed since selection"},
		{"another kind", KeysRequest{PaneID: "w1:p1", Binding: &codex, Keys: []string{"1"}}, "the pane holds a claude agent; the binding is codex"},
		{"a shell", KeysRequest{PaneID: "w1:p3", Keys: []string{"1"}}, "the pane holds no agent: w1:p3"},
		{"no pane", KeysRequest{PaneID: "w1:p9", Keys: []string{"1"}}, herdr.CodeAgentNotFound},
	} {
		sent, err := Keys(ctx, env, c.req)
		if sent != 0 || err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Keys = %d, %v", c.name, sent, err)
		}
	}
	if len(h.sent) != 0 {
		t.Fatalf("keys sent: %v", h.sent)
	}
	stop := errors.New("the dialog changed")
	sent, err := Keys(ctx, env, KeysRequest{PaneID: "w1:p1", Binding: &b, Keys: []string{"down", "enter"},
		Before: func(i int) error {
			if i == 1 {
				return stop
			}
			return nil
		}})
	if sent != 1 || !errors.Is(err, stop) || !reflect.DeepEqual(h.sent, []string{"w1:p1 down"}) {
		t.Errorf("a failed check = %d, %v, %v", sent, err, h.sent)
	}
	h.err = &herdr.Error{Method: "agent.send_keys", Code: herdr.CodeInvalidParams}
	var ke *KeyError
	if sent, err := Keys(ctx, env, KeysRequest{PaneID: "w1:p1", Binding: &b, Keys: []string{"bogus"}}); sent != 0 ||
		!errors.As(err, &ke) || ke.Index != 0 || herdr.ErrorCode(err) != herdr.CodeInvalidParams {
		t.Errorf("herdr's refusal = %d, %v", sent, err)
	}
}

// An agent that nhm did not bind takes keys with no lock; a cancelled
// context stops the sequence in a gap.
func TestKeysUnboundAgentAndCancel(t *testing.T) {
	w, h, env := keysWorld(t)
	w.fake.add("w1:p4", "claude", "", herdr.StatusBlocked, 1)
	if sent, err := Keys(context.Background(), env, KeysRequest{PaneID: "w1:p4", Keys: []string{"esc"}}); sent != 1 || err != nil {
		t.Fatalf("unbound = %d, %v", sent, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent, err := Keys(ctx, env, KeysRequest{PaneID: "w1:p4", Keys: []string{"down", "enter"}, Gap: time.Hour,
		Before: func(int) error { cancel(); return nil }})
	if sent != 1 || !errors.Is(err, context.Canceled) || !reflect.DeepEqual(h.sent, []string{"w1:p4 esc", "w1:p4 down"}) {
		t.Errorf("cancel = %d, %v, %v", sent, err, h.sent)
	}
}
