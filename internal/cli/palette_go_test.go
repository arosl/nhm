package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	// Claimed by a process that ended (killed) with no result and no
	// record: nothing was sent, never "the core still runs this send".
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner.JobPath(jobs.JobDir, "op-killed"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	claim := filepath.Join(jobs.JobDir, "op-killed.run")
	if err := os.WriteFile(claim, []byte(strconv.Itoa(dead.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !runner.Claimed(jobs.JobDir, "op-killed") || !runner.Gone(jobs.JobDir, "op-killed") {
		t.Fatal("setup: the claim is not the runner's, or its process is not gone")
	}
	if res, err := read(ctx, "op-killed"); res.InProgress || !errors.Is(err, sql.ErrNoRows) || !strings.Contains(err.Error(), "ended") {
		t.Fatalf("a killed job = %+v %v", res, err)
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
	if res, err := read(ctx, "op-done"); err != nil || res.OperationID != "op-done" || !res.InProgress {
		t.Fatalf("a recorded send = %+v %v", res, err)
	}
	// A record still open whose job was killed during the send: the core
	// no longer runs it, so it is not "in progress"; its receipts stand.
	if _, err := db.ClaimOperation(ctx, store.ClaimRequest{ID: "op-cut", Verb: send.Verb, Scope: map[string]string{},
		Request: map[string]string{}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner.JobPath(jobs.JobDir, "op-cut"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutClaim := filepath.Join(jobs.JobDir, "op-cut.run")
	if err := os.WriteFile(cutClaim, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := read(ctx, "op-cut"); err != nil || !res.InProgress {
		t.Fatalf("an open record whose job runs = %+v %v", res, err)
	}
	if err := os.WriteFile(cutClaim, []byte(strconv.Itoa(dead.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := read(ctx, "op-cut"); err != nil || res.InProgress || res.OperationID != "op-cut" ||
		!slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "job ended during the send") }) {
		t.Fatalf("an open record whose job was killed = %+v %v", res, err)
	}
}

// The memory preview uses the send's modes from nhm's config: off by
// default, cm and cass when the config turns them on.
func TestPaletteMemoryFollowsTheConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	(&fakeTools{}).use(t)
	mem := paletteMemory(t.TempDir())
	got, err := mem(context.Background(), "fix the login tests", palette.MemoryModes{})
	if err != nil || got.CM.Enabled || got.Block != "" {
		t.Fatalf("default config = %+v %v", got, err)
	}
	// The palette's choice wins over the config, both ways (nhm-t63).
	got, err = mem(context.Background(), "fix the login tests", palette.MemoryModes{CM: enrich.BestEffort})
	if err != nil || !got.CM.Enabled || got.CASS.Enabled || !strings.Contains(got.Block, "Run the login tests") {
		t.Fatalf("CM chosen on = %+v %v", got, err)
	}
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "nhm", "config.toml")
	writeTestFile(t, cfg, "[memory]\ncm = \"best-effort\"\n[cass]\ncontext = \"best-effort\"\n")
	got, err = mem(context.Background(), "fix the login tests", palette.MemoryModes{CASS: enrich.Off})
	if err != nil || !got.CM.Enabled || got.CASS.Enabled {
		t.Fatalf("CASS chosen off = %+v %v", got, err)
	}
	got, err = mem(context.Background(), "fix the login tests", palette.MemoryModes{})
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

// --go is accepted for old callers and does nothing.
func TestPaletteUsage(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--go", "--help"}} {
		var out, errOut bytes.Buffer
		if code := runPalette(args, &out, &errOut); code != exitOK || !strings.Contains(out.String(), "nhm palette --show KEY") {
			t.Fatalf("%v = %d %q", args, code, out.String())
		}
	}
	for _, args := range [][]string{{"--go", "stray"}, {"--json"}, {"--list", "--show", "k"}} {
		var out, errOut bytes.Buffer
		if code := runPalette(args, &out, &errOut); code != exitRefused || !strings.Contains(errOut.String(), "Usage: nhm palette") {
			t.Fatalf("%v = %d %q", args, code, errOut.String())
		}
	}
}

// nhm palette --list and --show (ACFS's lesson 06) read the palette's
// files in NHM_PALETTE_CWD (ACFS's hand-off), and its project root, with
// no herdr call; --file reads that file alone.
func TestPaletteListAndShow(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_PLUGIN_CONTEXT_JSON", "")
	t.Setenv(ui.ContextEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("ACFS_HOME", filepath.Join(home, ".acfs"))
	t.Setenv("NHM_PALETTE_FILE", "")
	proj := t.TempDir()
	writeTestFile(t, filepath.Join(proj, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(proj, "command_palette.md"), "## Review\n### fresh_review | Fresh\teyes\n\nLook again.\n\n### ping\nPing.\n")
	writeTestFile(t, filepath.Join(home, ".config", "nhm", "command_palette.md"), "## Mine\n### fresh_review | Shadowed\nNo.\n### mine | Mine\nMy prompt.\n")
	sub := filepath.Join(proj, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NHM_PALETTE_CWD", sub)
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := runPalette(args, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	code, out, errOut := run("--list")
	want := "fresh_review\tFresh eyes\tReview\t" + filepath.Join(proj, "command_palette.md") + "\n" +
		"ping\tping\tReview\t" + filepath.Join(proj, "command_palette.md") + "\n" +
		"mine\tMine\tMine\t" + filepath.Join(home, ".config", "nhm", "command_palette.md") + "\n"
	if code != exitOK || out != want {
		t.Fatalf("--list = %d %q %q, want %q", code, out, errOut, want)
	}
	code, out, _ = run("--list", "--json")
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); code != exitOK || err != nil || len(rows) != 3 || rows[2]["key"] != "mine" ||
		rows[0]["category"] != "Review" {
		t.Fatalf("--list --json = %d %q %v", code, out, err)
	}
	if code, out, _ = run("--show", "fresh_review"); code != exitOK || out != "Look again.\n" {
		t.Fatalf("--show = %d %q", code, out)
	}
	if code, out, errOut = run("--show", "nosuch"); code != exitError || out != "" || !strings.Contains(errOut, "no palette prompt has the key nosuch") ||
		strings.Contains(errOut, "Usage:") {
		t.Fatalf("--show nosuch = %d %q %q", code, out, errOut)
	}
	if code, out, _ = run("--file", filepath.Join(home, ".config", "nhm", "command_palette.md"), "--show", "fresh_review"); code != exitOK || out != "No.\n" {
		t.Fatalf("--file --show = %d %q", code, out)
	}
	if code, _, errOut = run("--file", filepath.Join(home, "nosuch.md"), "--list"); code != exitRefused || !strings.Contains(errOut, "no such readable file") {
		t.Fatalf("--file nosuch = %d %q", code, errOut)
	}
	// No palette files: --list prints nothing, and its JSON is [].
	t.Setenv("NHM_PALETTE_CWD", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code, out, _ = run("--list"); code != exitOK || out != "" {
		t.Fatalf("--list, no files = %d %q", code, out)
	}
	if code, out, _ = run("--list", "--json"); code != exitOK || out != "[]\n" {
		t.Fatalf("--list --json, no files = %d %q", code, out)
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
	if code := runPalette(nil, &out, &errOut); code != exitRefused || !strings.Contains(errOut.String(), "empty") {
		t.Fatalf("= %d %q", code, errOut.String())
	}
}

// At its exit, the palette waits for a send in flight until nhm ui run
// has claimed its job (the job then runs on its own) or the submission
// has ended, and no longer than its bound.
func TestPaletteHandOff(t *testing.T) {
	jobs := runner.Client{JobDir: t.TempDir()}
	release := make(chan struct{})
	opID, err := store.NewOperationID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sub := palette.NewSubmitter(func(_ context.Context, req send.Request) (send.Result, error) {
		<-release
		return send.Result{OperationID: req.OperationID}, nil
	}, nil, nil, func() (string, error) { return opID, nil })
	if _, _, _, err := sub.Submit(send.Request{Message: "x"}); err != nil {
		t.Fatal(err)
	}
	wait := 300 * time.Millisecond

	// No job yet: the wait ends at its bound.
	start := time.Now()
	if paletteHandOff(sub, jobs, opID, wait) {
		t.Fatal("handed off with no job")
	}
	if d := time.Since(start); d < wait {
		t.Fatalf("waited %s, want %s", d, wait)
	}
	// A job that nhm ui run has not claimed may never start.
	j, err := runner.NewJob(runner.VerbSend, ui.Context{SocketPath: "/run/h.sock", Cwd: t.TempDir()}, send.Request{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	j.ID = opID
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runner.JobPath(jobs.JobDir, opID), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if paletteHandOff(sub, jobs, opID, wait) {
		t.Fatal("handed off to an unclaimed job")
	}
	// Claimed: it runs on its own.
	if err := runner.Claim(jobs.JobDir, opID); err != nil {
		t.Fatal(err)
	}
	if !paletteHandOff(sub, jobs, opID, 5*time.Second) {
		t.Fatal("a claimed job was not handed off")
	}
	// A submission that ended needs no job.
	close(release)
	sub.Wait()
	if !paletteHandOff(sub, jobs, "op-none", 5*time.Second) {
		t.Fatal("an ended submission waited")
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
