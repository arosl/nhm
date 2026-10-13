package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui/actions"
	"github.com/arosl/nhm/internal/ui/runner"
)

// A job that answered keeps the core's result; an answer that did not
// come back names the operation, so the palette shows it as unknown.
func TestPaletteSenderKeepsALostAnswerUnknown(t *testing.T) {
	answer := func(res send.Result, err error) actions.Core {
		return actions.Core{Send: func(context.Context, send.Request) (send.Result, error) { return res, err }}
	}
	req := send.Request{OperationID: "op-1"}
	cases := []struct {
		name   string
		core   actions.Core
		wantID string
	}{
		{"refused before the claim", answer(send.Result{}, &uiJobError{Code: exitRefused, Message: "no agent matches"}), ""},
		{"the wait ended", answer(send.Result{}, context.DeadlineExceeded), "op-1"},
		{"the result did not read", answer(send.Result{}, errors.New("runner: result op-1: bad json")), "op-1"},
		{"claimed, then failed", answer(send.Result{OperationID: "op-1"}, &uiJobError{Code: exitError, Message: "receipt"}), "op-1"},
		{"answered", answer(send.Result{OperationID: "op-1"}, nil), "op-1"},
	}
	for _, c := range cases {
		res, _ := paletteSender(c.core)(context.Background(), req)
		if res.OperationID != c.wantID {
			t.Errorf("%s: operation id %q, want %q", c.name, res.OperationID, c.wantID)
		}
	}
}

// With no record, the job decides: still running is in progress; ended
// with an error is "nothing was sent"; no job at all is no record.
func TestPaletteReaderAsksTheJob(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root, err := store.StateRoot()
	if err != nil {
		t.Fatal(err)
	}
	inst, _, err := store.Enroll(root, "/run/herdr/test.sock", "default", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(inst.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := runner.Client{JobDir: runner.JobDir(inst.Dir)}
	if err := os.MkdirAll(jobs.JobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	read := paletteReader(db, jobs)
	ctx := context.Background()

	if _, err := read(ctx, "op-none"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no job: %v", err)
	}
	if err := os.WriteFile(runner.JobPath(jobs.JobDir, "op-runs"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := read(ctx, "op-runs"); err != nil || !res.InProgress || res.OperationID != "op-runs" {
		t.Fatalf("a running job = %+v %v", res, err)
	}
	if err := runner.Finish(jobs.JobDir, runner.Result{ID: "op-refused", Verb: runner.VerbSend, Code: exitRefused,
		Error: "no agent matches the selection"}); err != nil {
		t.Fatal(err)
	}
	if _, err := read(ctx, "op-refused"); !errors.Is(err, sql.ErrNoRows) || !strings.Contains(err.Error(), "no agent matches") {
		t.Fatalf("a refused job: %v", err)
	}
	if _, err := db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-done", Verb: send.Verb, Scope: map[string]string{},
		Request: map[string]string{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, err := read(ctx, "op-done"); err != nil || res.OperationID != "op-done" {
		t.Fatalf("a recorded send = %+v %v", res, err)
	}
}

// The memory preview uses the send's modes from nhm's config: off by
// default, cm and cass when the config turns them on.
func TestPaletteMemoryFollowsTheConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	(&fakeTools{}).use(t)
	mem := paletteMemory(t.TempDir())
	got, err := mem(context.Background(), "fix the login tests")
	if err != nil || got.CM.Enabled || got.Block != "" {
		t.Fatalf("default config = %+v %v", got, err)
	}
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm", "config.toml")
	writeTestFile(t, cfg, "[memory]\ncm = \"best-effort\"\n[cass]\ncontext = \"best-effort\"\n")
	got, err = mem(context.Background(), "fix the login tests")
	if err != nil || !got.CM.Enabled || !strings.Contains(got.Block, "Run the login tests") {
		t.Fatalf("memory on = %+v %v", got, err)
	}
	included := 0
	for _, it := range got.Items {
		if it.Status == enrich.Included {
			included++
		}
	}
	if included == 0 {
		t.Fatalf("no item included: %+v", got.Items)
	}
}

func TestPaletteGoUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runPaletteGo([]string{"--go", "--help"}, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "nhm palette --go") {
		t.Fatalf("--help = %d %q", code, out.String())
	}
	out.Reset()
	if code := runPaletteGo([]string{"--go", "stray"}, &out, &errOut); code != exitRefused {
		t.Fatalf("a stray argument = %d", code)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
