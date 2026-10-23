package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A br whose child holds stdout open (a wrapper, sh: sleep 30; exec br)
// does not keep the feedback past its context (nhm-fty).
func TestBrBeadEndsWhenAChildHoldsStdout(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "br"), []byte("#!/bin/sh\nsleep 30\necho '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := brBead(ctx, t.TempDir(), "nhm-1"); err == nil {
		t.Fatal("a killed br returned no error")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("br show took %v after its context ended", took)
	}
}
