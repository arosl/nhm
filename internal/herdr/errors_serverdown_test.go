package herdr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// A missing socket and a socket nobody accepts on are a server that is
// down; a decode failure from a server that answers is not (nhm-swm).
func TestServerDown(t *testing.T) {
	// A socket path must fit sun_path, so not under a gate's long TMPDIR.
	dir, err := os.MkdirTemp("/tmp", "nhm-serverdown-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	missing := New(filepath.Join(dir, "absent.sock"))
	_, err = missing.Snapshot(context.Background())
	if !ServerDown(fmt.Errorf("bindings: import: %w", err)) {
		t.Fatalf("missing socket: ServerDown(%v) = false", err)
	}

	stale := filepath.Join(dir, "stale.sock")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	// Close the listener but keep its file: a socket that refuses.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("the stale socket file is gone: %v", err)
	}
	_, err = New(stale).Snapshot(context.Background())
	if !ServerDown(err) {
		t.Fatalf("refused socket: ServerDown(%v) = false", err)
	}

	for _, other := range []error{
		&TransportError{Method: "session.snapshot", Err: fmt.Errorf("decode reply: %w", errors.New("bad json"))},
		errors.New("herdr: no such file"),
		context.Canceled,
	} {
		if ServerDown(other) {
			t.Fatalf("ServerDown(%v) = true", other)
		}
	}
}
