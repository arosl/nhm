package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/store"
)

var t0 = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

func TestMissingOwnerFileMeansBash(t *testing.T) {
	o, err := ReadOwner(t.TempDir())
	if err != nil || o.Owner != OwnerBash || o.Generation != 0 {
		t.Fatalf("ReadOwner = %+v, %v", o, err)
	}
}

func TestCorruptOwnerFileRefusesEverything(t *testing.T) {
	for name, body := range map[string]string{
		"not toml":      "owner = ",
		"unknown owner": "owner = \"tmux\"\ngeneration = 1\n",
		"no generation": "owner = \"nhm\"\n",
		"extra key":     "owner = \"nhm\"\ngeneration = 1\nepoch = 2\n",
		"negative":      "owner = \"nhm\"\ngeneration = -1\n",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, store.OwnerFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadOwner(dir); !errors.Is(err, ErrOwnerCorrupt) {
			t.Errorf("%s: ReadOwner = %v", name, err)
		}
		called := false
		err := WithOwnerShared(context.Background(), dir, func(Owner) error { called = true; return nil })
		if !errors.Is(err, ErrOwnerCorrupt) || called {
			t.Errorf("%s: WithOwnerShared = %v, called %v", name, err, called)
		}
		if _, err := SwitchOwner(context.Background(), dir, OwnerNhm, t0, nil); !errors.Is(err, ErrOwnerCorrupt) {
			t.Errorf("%s: SwitchOwner = %v", name, err)
		}
	}
}

func TestTakeoverAndHandback(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	o, err := SwitchOwner(ctx, dir, OwnerNhm, t0, func(cur Owner) error {
		if cur.Owner != OwnerBash {
			t.Errorf("prepare saw %+v", cur)
		}
		return nil
	})
	if err != nil || o.Owner != OwnerNhm || o.Generation != 1 {
		t.Fatalf("takeover = %+v, %v", o, err)
	}
	if got, err := ReadOwner(dir); err != nil || got.Owner != o.Owner || got.Generation != o.Generation || !got.UpdatedAt.Equal(o.UpdatedAt) {
		t.Fatalf("ReadOwner = %+v, %v; want %+v", got, err, o)
	}
	if _, err := SwitchOwner(ctx, dir, OwnerNhm, t0, nil); err == nil {
		t.Fatal("a second takeover was accepted")
	}
	err = WithOwnerShared(ctx, dir, func(o Owner) error { return RequireOwner(o, OwnerNhm) })
	if err != nil {
		t.Fatalf("nhm mutator refused after takeover: %v", err)
	}
	err = WithOwnerShared(ctx, dir, func(o Owner) error { return RequireOwner(o, OwnerBash) })
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("Bash-side check after takeover = %v", err)
	}

	// A handback whose export fails changes nothing.
	if _, err := SwitchOwner(ctx, dir, OwnerBash, t0, func(Owner) error { return errors.New("token export lost a binding") }); err == nil {
		t.Fatal("a failed handback switched the owner")
	}
	if got, _ := ReadOwner(dir); got.Owner != OwnerNhm || got.Generation != 1 {
		t.Fatalf("owner after a failed handback = %+v", got)
	}
	o, err = SwitchOwner(ctx, dir, OwnerBash, t0.Add(time.Hour), nil)
	if err != nil || o.Owner != OwnerBash || o.Generation != 2 {
		t.Fatalf("handback = %+v, %v", o, err)
	}
	if _, err := SwitchOwner(ctx, dir, "tmux", t0, nil); err == nil {
		t.Fatal("an unknown owner was accepted")
	}
}

// A takeover waits for a mutator that holds the shared lock, and a
// mutator cannot start while a takeover holds it.
// acfs-9hkh.77 F5: a fresh intent marker holds new shared takers back; a
// stale one (a dead switch's) does not.
func TestIntentMarkerHoldsSharedTakers(t *testing.T) {
	dir := t.TempDir()
	intent := filepath.Join(dir, OwnerIntentFile)
	if err := os.WriteFile(intent, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := WithOwnerShared(ctx, dir, func(Owner) error { return nil }); !errors.Is(err, ErrOwnerLockTimeout) {
		t.Fatalf("a shared taker behind a fresh marker = %v", err)
	}
	old := time.Now().Add(-IntentMaxAge - time.Minute)
	if err := os.Chtimes(intent, old, old); err != nil {
		t.Fatal(err)
	}
	if err := WithOwnerShared(context.Background(), dir, func(Owner) error { return nil }); err != nil {
		t.Fatalf("a stale marker held a shared taker: %v", err)
	}
	// A marker from the future is stale too.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(intent, future, future); err != nil {
		t.Fatal(err)
	}
	if err := WithOwnerShared(context.Background(), dir, func(Owner) error { return nil }); err != nil {
		t.Fatalf("a marker from the future held a shared taker: %v", err)
	}
}

// acfs-9hkh.77 F5: overlapping shared holders, which alone would keep the
// lock shared for ever, cannot starve a switch: its marker drains them.
func TestSwitchIsNotStarvedByOverlappingSharedHolders(t *testing.T) {
	dir := t.TempDir()
	stop := make(chan struct{})
	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			time.Sleep(time.Duration(i) * 15 * time.Millisecond)
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				_ = WithOwnerShared(ctx, dir, func(Owner) error { time.Sleep(30 * time.Millisecond); return nil })
				cancel()
			}
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o, err := SwitchOwner(ctx, dir, OwnerNhm, t0, nil)
	close(stop)
	<-done
	<-done
	if err != nil || o.Owner != OwnerNhm {
		t.Fatalf("switch = %+v, %v", o, err)
	}
	if _, err := os.Stat(filepath.Join(dir, OwnerIntentFile)); !os.IsNotExist(err) {
		t.Fatalf("the intent marker stayed: %v", err)
	}
	if _, err := SwitchOwner(context.Background(), dir, OwnerNhm, t0, nil); !errors.Is(err, ErrAlreadyOwner) {
		t.Fatalf("a switch to the current owner = %v", err)
	}
}

func TestTakeoverWaitsForSharedHolders(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	inside := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithOwnerShared(ctx, dir, func(Owner) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := SwitchOwner(short, dir, OwnerNhm, t0, nil); !errors.Is(err, ErrOwnerLockTimeout) {
		t.Fatalf("takeover during a shared hold = %v", err)
	}
	switched := make(chan error, 1)
	go func() {
		_, err := SwitchOwner(ctx, dir, OwnerNhm, t0, func(Owner) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		switched <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-switched; err != nil {
		t.Fatalf("takeover after release = %v", err)
	}
	if o, _ := ReadOwner(dir); o.Owner != OwnerNhm {
		t.Fatalf("owner = %+v", o)
	}
}

// ACFS's Bash takes the same lock with flock(1); a takeover waits for it.
func TestTakeoverWaitsForBashFlock(t *testing.T) {
	flock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("flock(1) not installed")
	}
	dir := t.TempDir()
	lock := filepath.Join(dir, store.OwnerLockFile)
	cmd := exec.Command(flock, "-s", lock, "sleep", "0.3")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	// Wait until the Bash side holds the lock.
	deadline := time.Now().Add(2 * time.Second)
	for {
		probe, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := SwitchOwner(probe, dir, OwnerNhm, t0, func(Owner) error { return errors.New("probe") })
		cancel()
		if errors.Is(err, ErrOwnerLockTimeout) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("flock(1) never took the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := SwitchOwner(ctx, dir, OwnerNhm, t0, nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("takeover did not wait for the Bash holder (%v)", time.Since(start))
	}
}
