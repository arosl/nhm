package bv

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// GetInsightsContext reads bv --robot-insights in the project, with its
// cycles as lists of bead IDs (nhm-cvw); a reply that is not JSON is an
// error, and so is a cancelled context.
func TestGetInsightsContextReadsCycles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools use a POSIX shell")
	}
	project := t.TempDir()
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *--robot-insights*) if [ -f bad ]; then echo nope; else printf '%s\n' '{"Cycles":[["a-1","a-2"]],"Bottlenecks":[]}'; fi ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bv"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := GetInsightsContext(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"a-1", "a-2"}}; !reflect.DeepEqual(got.Cycles, want) {
		t.Fatalf("cycles = %v, want %v", got.Cycles, want)
	}

	if err := os.WriteFile(filepath.Join(project, "bad"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := GetInsightsContext(context.Background(), project); err == nil {
		t.Fatal("a reply that is not JSON was read")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GetInsightsContext(ctx, project); err == nil {
		t.Fatal("a cancelled context ran bv")
	}
}
