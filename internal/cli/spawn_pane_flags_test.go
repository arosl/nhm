package cli

import (
	"bytes"
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
