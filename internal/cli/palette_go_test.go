package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/store"
	"github.com/arosl/nhm/internal/ui"
	"github.com/arosl/nhm/internal/ui/palette"
	"github.com/arosl/nhm/internal/ui/runner"
)

// uiCore's send answers in the shapes the palette classifies: a lost
// answer names the operation (unknown, never resent); a job that did not
// start, or the core's refusal before its claim, names none (nothing was
// sent); an unfilled variable is read from the refusal's text.
func TestPaletteOutcomesOfUICoresSend(t *testing.T) {
	cases := []struct {
		name string
		res  send.Result
		err  error
		want palette.Outcome
	}{
		{"answered", send.Result{OperationID: "op-1"}, nil, palette.Answered},
		{"the wait ended", send.Result{OperationID: "op-1"}, &uiLostError{ID: "op-1", Err: context.DeadlineExceeded}, palette.Unknown},
		{"the job did not start", send.Result{}, errors.New("runner: start: no such file"), palette.Refused},
		{"refused before the claim", send.Result{}, &uiJobError{Code: exitRefused, Message: "no agent matches the selection"}, palette.Refused},
		{"claimed, then failed", send.Result{OperationID: "op-1"}, &uiJobError{Code: exitError, Message: "receipt"}, palette.Unknown},
	}
	for _, c := range cases {
		if got := (palette.SubmitMsg{Result: c.res, Err: c.err}).Outcome(); got != c.want {
			t.Errorf("%s: outcome %v, want %v", c.name, got, c.want)
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

// A selection handed on always gets the Go palette: the fzf palette
// cannot honour one. Without one, [ui] palette decides.
func TestPaletteEngineForAHandedSelection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := ui.Context{SocketPath: "/run/h.sock", WorkspaceID: "w1"}
	if e, err := paletteEngine(c); err != nil || e != engineFzf {
		t.Fatalf("no selection, no config = %q %v", e, err)
	}
	picked := c.WithTargets("board", []ui.Target{{BindingID: "b1", Generation: 2, PaneID: "w1:p1"}})
	if e, err := paletteEngine(picked); err != nil || e != engineGo {
		t.Fatalf("a handed selection = %q %v", e, err)
	}
	writeTestFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm", "config.toml"), "[ui]\npalette = \"go\"\n")
	if e, err := paletteEngine(c); err != nil || e != engineGo {
		t.Fatalf("[ui] palette = go = %q %v", e, err)
	}
}

// An explicitly empty selection is refused before the palette opens.
func TestPaletteGoRefusesAnEmptySelection(t *testing.T) {
	data, err := json.Marshal(ui.Context{SocketPath: "/run/h.sock", WorkspaceID: "w1"}.WithTargets("board", nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(ui.ContextEnv, string(data))
	var out, errOut bytes.Buffer
	if code := runPaletteGo(nil, &out, &errOut); code != exitRefused || !strings.Contains(errOut.String(), "empty") {
		t.Fatalf("= %d %q", code, errOut.String())
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
