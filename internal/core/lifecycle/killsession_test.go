package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/arosl/nhm/internal/core/bindings"
	"github.com/arosl/nhm/internal/herdr"
)

// fakeReaper is a process tree by pid; Reap records what it got.
type fakeReaper struct {
	children map[int][]int
	reaped   []int
}

func (r *fakeReaper) Descendants(pid int) []int { return r.children[pid] }
func (r *fakeReaper) Reap(pids []int) []int {
	r.reaped = append(r.reaped, pids...)
	return pids
}

// TestKillSessionClosesTheWorkspace: ntm's kill runs no retire check: a
// working agent dies with the rest. The bindings are retired, the
// reservations released, the identities left, and the processes under
// the panes' shells (read before the close, each once) reaped.
func TestKillSessionClosesTheWorkspace(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	icy := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusWorking)
	blue := w.bind(t, "BlueLake", "w1:p2", herdr.StatusIdle)
	w.h.panes["w1:p3"] = true // a shell with no agent
	red := w.bind(t, "RedFox", "w1:p4", herdr.StatusIdle)
	if _, err := w.env.Bindings.Displace(ctx, red.ID, red.Generation, "test"); err != nil {
		t.Fatal(err)
	}
	r := &fakeReaper{children: map[int][]int{100: {300, 301}}}
	res, err := KillSession(ctx, w.env, KillSessionRequest{WorkspaceID: "w1", Reaper: r})
	if err != nil {
		t.Fatal(err)
	}
	if !res.WorkspaceClosed || !slices.Equal(w.h.wsClosed, []string{"w1"}) || len(w.h.closed) != 0 {
		t.Fatalf("closed = %v %v %v", res.WorkspaceClosed, w.h.wsClosed, w.h.closed)
	}
	slices.Sort(res.Retired)
	if !slices.Equal(res.Retired, []string{"BlueLake", "IcyKnoll", "RedFox"}) || !slices.Equal(r.reaped, []int{300, 301}) || !slices.Equal(res.Reaped, []int{300, 301}) {
		t.Fatalf("retired %v, reaped %v", res.Retired, r.reaped)
	}
	for _, b := range []bindings.Binding{icy, blue, red} {
		got, err := w.env.Bindings.Get(ctx, b.ID)
		if err != nil || got.Status != bindings.Retired || got.StatusReason != "kill" {
			t.Fatalf("%s = %+v %v", b.MailName, got, err)
		}
	}
	slices.Sort(res.Released)
	if w.mail.called("release_file_reservations") != 3 || !slices.Equal(res.Released, []string{"BlueLake", "IcyKnoll", "RedFox"}) ||
		w.mail.called("retire_agent") != 0 || len(res.Warnings) != 0 {
		t.Fatalf("mail calls %v, released %v, warnings %v", w.mail.calls, res.Released, res.Warnings)
	}
}

// TestKillSessionTaggedPanes: a kill of some panes closes them alone and
// retires their bindings; like ntm's, it reaps nothing and releases no
// reservation.
func TestKillSessionTaggedPanes(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	icy := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	blue := w.bind(t, "BlueLake", "w1:p2", herdr.StatusWorking)
	r := &fakeReaper{children: map[int][]int{100: {300}}}
	res, err := KillSession(ctx, w.env, KillSessionRequest{WorkspaceID: "w1", Panes: []string{"w1:p2"}, Reaper: r})
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkspaceClosed || len(w.h.wsClosed) != 0 || !slices.Equal(w.h.closed, []string{"w1:p2"}) || !slices.Equal(res.Retired, []string{"BlueLake"}) {
		t.Fatalf("result %+v, closed %v", res, w.h.closed)
	}
	if len(r.reaped) != 0 || w.mail.called("release_file_reservations") != 0 {
		t.Fatalf("reaped %v, mail %v", r.reaped, w.mail.calls)
	}
	if got, _ := w.env.Bindings.Get(ctx, icy.ID); got.Status != bindings.Active {
		t.Fatalf("IcyKnoll = %s", got.Status)
	}
	if got, _ := w.env.Bindings.Get(ctx, blue.ID); got.Status != bindings.Retired {
		t.Fatalf("BlueLake = %s", got.Status)
	}
}

// closeFails is the fake herdr with a workspace.close that fails.
type closeFails struct{ *fakeHerdr }

func (closeFails) WorkspaceClose(context.Context, string) error { return errors.New("herdr: down") }

// TestKillSessionFromInside: the caller's pane closes with the workspace
// and ends this process, so the bindings are retired and the reservations
// released before the close. From outside, a failed close leaves them.
func TestKillSessionFromInside(t *testing.T) {
	for _, inside := range []bool{true, false} {
		t.Run(fmt.Sprint("inside=", inside), func(t *testing.T) {
			w := newWorld(t)
			ctx := context.Background()
			icy := w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
			w.env.Herdr = closeFails{w.h}
			if inside {
				w.env.Caller = "w1:p1"
			}
			_, err := KillSession(ctx, w.env, KillSessionRequest{WorkspaceID: "w1"})
			got, _ := w.env.Bindings.Get(ctx, icy.ID)
			want := bindings.Active
			if inside {
				want = bindings.Retired
			}
			if err == nil || !strings.Contains(err.Error(), "close workspace w1") || got.Status != want {
				t.Fatalf("err %v, status %s, want %s", err, got.Status, want)
			}
		})
	}
}

// TestKillSessionMailDown: Agent Mail down is a warning; the kill stands.
func TestKillSessionMailDown(t *testing.T) {
	w := newWorld(t)
	w.bind(t, "IcyKnoll", "w1:p1", herdr.StatusIdle)
	w.mail.down = true
	res, err := KillSession(context.Background(), w.env, KillSessionRequest{WorkspaceID: "w1"})
	if err != nil || !res.WorkspaceClosed || len(res.Released) != 0 || len(res.Warnings) != 1 ||
		!strings.Contains(res.Warnings[0], "release the reservations of IcyKnoll") {
		t.Fatalf("result %+v, err %v", res, err)
	}
}

// TestProcReaper: the kernel's child lists, depth first, without this
// process; SIGTERM to the live ones, then SIGKILL to what survives.
func TestProcReaper(t *testing.T) {
	root := t.TempDir()
	children := func(pid int, list string) {
		dir := filepath.Join(root, fmt.Sprint(pid), "task", fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "children"), []byte(list), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	children(100, fmt.Sprintf("200 %d 201\n", os.Getpid()))
	children(200, "300")
	var mu sync.Mutex
	alive := map[int]bool{200: true, 300: true}
	var signals []string
	r := ProcReaper{Root: root, Grace: 0, Signal: func(pid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		signals = append(signals, fmt.Sprintf("%d:%d", pid, sig))
		if !alive[pid] {
			return syscall.ESRCH
		}
		if sig == syscall.SIGTERM && pid == 200 {
			alive[200] = false
		}
		return nil
	}}
	got := r.Descendants(100)
	if !slices.Equal(got, []int{200, 300, 201}) {
		t.Fatalf("descendants = %v", got)
	}
	if reaped := r.Reap(got); !slices.Equal(reaped, []int{200, 300}) {
		t.Fatalf("reaped = %v", reaped)
	}
	want := []string{"200:0", "200:15", "300:0", "300:15", "201:0", "200:0", "300:0", "300:9"}
	if !slices.Equal(signals, want) {
		t.Fatalf("signals = %v, want %v", signals, want)
	}
	if (ProcReaper{Root: root}).Reap(got) != nil {
		t.Fatal("a reaper without Signal must signal nothing")
	}
}
