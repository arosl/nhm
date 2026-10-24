package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/spawn"
)

// ntm's spawn-only pane flags: --pane-env reaches the request,
// --verify-boot waits (30s, or --ready-timeout) even with no prompt, and
// --no-hooks changes nothing; add has none of them (nhm-2p6.4.7).
func TestSpawnPaneFlags(t *testing.T) {
	t.Setenv("NHM_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	_, req, err := parseVerbArgs(t, false, "--cc=1", "--pane-env", "A=1,B={role}", "--pane-env", "C={pane}", "--no-hooks")
	if err != nil || len(req.PaneEnv) != 3 || req.PaneEnv["B"] != "{role}" || req.PaneEnv["C"] != "{pane}" || !req.NoWait {
		t.Fatalf("pane env = %v, no wait %v, %v", req.PaneEnv, req.NoWait, err)
	}
	if _, req, err = parseVerbArgs(t, false, "--cc=1", "--verify-boot"); err != nil || req.NoWait || !req.VerifyBoot ||
		req.ReadyTimeout != 30*time.Second {
		t.Fatalf("verify boot = %+v %v", req, err)
	}
	if _, req, err = parseVerbArgs(t, false, "--cc=1", "--verify-boot", "--ready-timeout", "10s"); err != nil || req.ReadyTimeout != 10*time.Second {
		t.Fatalf("verify boot with a timeout = %v %v", req.ReadyTimeout, err)
	}
	for _, flag := range []string{"--pane-env=A=1", "--verify-boot", "--no-hooks"} {
		if _, _, err := parseVerbArgs(t, true, "--cc=1", flag); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("add %s = %v", flag, err)
		}
	}
}

// A --verify-boot spawn whose agent did not get ready fails with ntm's
// words, and under --json with TIMEOUT (nhm-2p6.4.7).
func TestSpawnVerifyBoot(t *testing.T) {
	res := twoAgents()
	res.Stagger = nil
	res.Targets[1].Ready, res.Targets[1].Status, res.Targets[1].Error = false, spawn.StatusUnresolved, "not ready after 30s"
	outcome := spawnOutcomeError(res).(*spawnOutcome)
	err := verifyBootError{outcome: outcome, within: 30 * time.Second}
	want := "spawn --verify-boot: 1/2 agent(s) ready within 30s: codex agent w1:p3: not ready after 30s; " +
		"the session and panes still exist — inspect with --robot-tail"
	var stdout, stderr bytes.Buffer
	if code := writeSpawnNTM(&stdout, &stderr, false, spawnOut{res: res}, err); code != exitError ||
		!strings.HasSuffix(stderr.String(), "Error: "+want+"\n") {
		t.Fatalf("text = %d %q", code, stderr.String())
	}
	if f := spawnFailureOf(spawnOut{res: res}, err); f.ErrorCode != "TIMEOUT" || f.Error != want {
		t.Fatalf("json = %s %q", f.ErrorCode, f.Error)
	}
}

// ntm's --create-dir, on --project's folder: --create-dir or --json makes
// a missing folder; without a terminal the spawn refuses in ntm's words; a
// dry run makes nothing; add has no --create-dir (nhm-2p6.4.5).
func TestSpawnCreateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "proj")
	var out bytes.Buffer
	want := "project directory " + dir + " does not exist; pass --create-dir to create it, or scaffold with 'nhm quick proj'"
	if code, done, err := ensureProjectDir(dir, "proj", &spawnFlags{}, &out); !done || code != exitError || err == nil || err.Error() != want {
		t.Fatalf("no terminal = %d %v %v", code, done, err)
	}
	if _, done, err := ensureProjectDir(dir, "proj", &spawnFlags{createDir: true, dryRun: true}, &out); !done || err == nil ||
		!strings.Contains(err.Error(), "a dry run creates nothing") {
		t.Fatalf("dry run = %v %v", done, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("made before --create-dir: %v", err)
	}
	if code, done, err := ensureProjectDir(dir, "proj", &spawnFlags{createDir: true}, &out); done || code != 0 || err != nil {
		t.Fatalf("--create-dir = %d %v %v", code, done, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("--create-dir made %v %v", info, err)
	}
	asJSON := filepath.Join(t.TempDir(), "j")
	if _, done, err := ensureProjectDir(asJSON, "proj", &spawnFlags{asJSON: true}, &out); done || err != nil {
		t.Fatalf("--json = %v %v", done, err)
	}
	if _, err := os.Stat(asJSON); err != nil || out.Len() != 0 {
		t.Fatalf("--json made %v, printed %q", err, out.String())
	}
	// Through the verb, the refusal is the root's error.
	missing := filepath.Join(t.TempDir(), "x")
	if code, stdout, errOut := run("spawn", "proj", "--cc=1", "--project", missing, "--allow-unguarded"); code != exitError ||
		stdout != "" || errOut != "Error: project directory "+missing+" does not exist; pass --create-dir to create it, or scaffold with 'nhm quick proj'\n" {
		t.Fatalf("spawn = %d %q %q", code, stdout, errOut)
	}
	if _, _, err := parseVerbArgs(t, true, "--cc=1", "--create-dir"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("add --create-dir = %v", err)
	}
}
