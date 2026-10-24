package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/contention"
	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// doctorWorld is an enrolled herdr server (swarmInstance) and a PATH of
// fake tools: bv, br and herdr only, so the report does not depend on the
// host.
func doctorWorld(t *testing.T) *herdrtest.Server {
	t.Helper()
	s := swarmInstance(t)
	bin := t.TempDir()
	for name, version := range map[string]string{"bv": "bv 0.31.0", "br": "br 0.1.4", "herdr": "herdr 0.9.5"} {
		script := fmt.Sprintf("#!/bin/sh\necho '%s'\n", version)
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A tool whose --version fails, as slb's does.
	if err := os.WriteFile(filepath.Join(bin, "slb"), []byte("#!/bin/sh\necho 'Error: unknown flag: --version' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HERDR_BIN_PATH", filepath.Join(bin, "herdr"))
	t.Chdir(t.TempDir())
	return s
}

func decodeDoctor(t *testing.T, out string) DoctorReport {
	t.Helper()
	var r DoctorReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not a DoctorReport: %v\n%s", err, out)
	}
	return r
}

// --json is ntm's bare DoctorReport: its sections in ntm's order, herdr in
// tmux's place, nhm's checks in Configuration, the six invariants.
func TestDoctorJSONIsDoctorReport(t *testing.T) {
	s := doctorWorld(t)
	code, out, errOut := run("doctor", "--no-mail", "--json", "--socket", s.SocketPath)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil || errOut != "" {
		t.Fatalf("doctor --json = %d %v %q %s", code, err, errOut, out)
	}
	for _, k := range []string{"timestamp", "overall", "safety_defaults", "tools", "dependencies", "daemons", "configuration", "invariants", "warnings", "errors"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("no %q", k)
		}
	}
	r := decodeDoctor(t, out)
	if code != 0 || r.Overall != "warning" || r.Errors != 0 || r.Warnings == 0 {
		t.Fatalf("verdict = %d %s %d errors %d warnings", code, r.Overall, r.Errors, r.Warnings)
	}
	if len(r.Tools) != 21 || r.Tools[0].Name != "bv" || !r.Tools[0].Required || r.Tools[0].Status != "ok" || r.Tools[0].Version != "bv 0.31.0" ||
		r.Tools[1].Name != "br" || !r.Tools[1].Required || r.Tools[2].Name != "am" || r.Tools[2].Status != "warning" ||
		r.Tools[2].Message != "optional tool not found" || r.Tools[20].Name != "rust_proxy" {
		t.Fatalf("tools = %+v", r.Tools)
	}
	if slb := r.Tools[8]; slb.Name != "slb" || !slb.Installed || slb.Status != "warning" || slb.Message != "health check failed" || slb.Version != "" {
		t.Fatalf("slb = %+v", slb)
	}
	h := r.Dependencies[0]
	if h.Name != "herdr" || !h.Installed || h.Version != "herdr 0.9.5" || h.Status != "ok" || h.MinVersion != "protocol 22" ||
		r.Dependencies[1].Name != "go" || r.Dependencies[1].Status != "warning" || r.Dependencies[2].Name != "grok" || r.Dependencies[2].Status != "ok" {
		t.Fatalf("dependencies = %+v", r.Dependencies)
	}
	if d := r.Daemons[0]; d.Name != "agent-mail" || d.Status != "unknown" || d.Message != "not checked (--no-mail)" || r.Daemons[1].Name != "cm-server" {
		t.Fatalf("daemons = %+v", r.Daemons)
	}
	cfg := map[string]string{}
	for _, c := range r.Configuration {
		cfg[c.Name] = c.Status
	}
	if cfg["nhm state directory"] != "ok" || cfg[".beads directory"] != "warning" || cfg["enrollment"] != "ok" ||
		cfg["database"] != "ok" || cfg["owner"] != "warning" || cfg["kinds"] != "ok" || cfg["guard claude dcg"] == "" {
		t.Fatalf("configuration = %v", cfg)
	}
	var ids []string
	for _, i := range r.Invariants {
		ids = append(ids, i.ID+"="+i.Status)
	}
	if want := "[no_silent_data_loss=warning graceful_degradation=ok idempotent_orchestration=ok recoverable_state=ok auditable_actions=unverified safe_by_default=unverified]"; fmt.Sprint(ids) != want {
		t.Fatalf("invariants = %v", ids)
	}
	if r.SafetyDefaults.RedactionMode != "warn" || r.SafetyDefaults.ClaudeAgentHooks == nil {
		t.Fatalf("safety defaults = %+v", r.SafetyDefaults)
	}
}

// The text is ntm's sections, icons and box.
func TestDoctorNTMSections(t *testing.T) {
	s := doctorWorld(t)
	code, out, _ := run("doctor", "--no-mail", "--socket", s.SocketPath)
	for _, want := range []string{"NHM Doctor\n", "Tools:\n  ✓ bv bv 0.31.0\n  ✓ br br 0.1.4\n  ⚠ am optional tool not found\n",
		"Dependencies:\n  ✓ herdr (herdr 0.9.5)\n  ⚠ go\n", "Daemons:\n  ? agent-mail not checked (--no-mail)\n",
		"Configuration:\n  ✓ nhm state directory exists and writable\n  ⚠ .beads directory no beads in current directory\n",
		"  ✓ enrollment ", "Safety Defaults:\n  ✓ Redaction mode: warn (allowlist: none)\n  ✓ Privacy default: disabled\n",
		"Design Invariants:\n", "  ? Auditable Actions not checked on nhm (deferred: nhm-2p6.19)\n", "Overall: HEALTHY ("} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if strings.Contains(out, "      dcg for claude") {
		t.Fatal("details without -v")
	}
}

// -v adds each invariant's details.
func TestDoctorVerbose(t *testing.T) {
	s := doctorWorld(t)
	for _, v := range []string{"-v", "--verbose"} {
		if code, out, _ := run("doctor", "--no-mail", v, "--socket", s.SocketPath); code != 0 ||
			!strings.Contains(out, "      21 external tool(s) registered, 18 unavailable\n") {
			t.Fatalf("doctor %s = %d\n%s", v, code, out)
		}
	}
}

// Only an unhealthy report exits 1: in text with ntm's error, under --json
// with the report alone.
func TestDoctorExitsOneOnlyWhenUnhealthy(t *testing.T) {
	doctorWorld(t)
	none := filepath.Join(t.TempDir(), "none.sock")
	code, out, errOut := run("doctor", "--no-mail", "--socket", none)
	if code != exitError || errOut != "Error: ecosystem is unhealthy\n" || !strings.Contains(out, "Overall: UNHEALTHY (") ||
		!strings.Contains(out, "  ✗ herdr (herdr 0.9.5)\n") {
		t.Fatalf("doctor without herdr = %d %q\n%s", code, errOut, out)
	}
	code, out, errOut = run("doctor", "--no-mail", "--json", "--socket", none)
	r := decodeDoctor(t, out)
	if code != exitError || errOut != "" || r.Overall != "unhealthy" || r.Errors == 0 || r.Dependencies[0].Status != "error" ||
		!strings.Contains(r.Dependencies[0].Message, none) {
		t.Fatalf("doctor --json without herdr = %d %q %+v", code, errOut, r.Dependencies[0])
	}
	if code, _, errOut := run("doctor", "stray"); code != exitError || !strings.Contains(errOut, `unknown command "stray" for "nhm doctor"`) {
		t.Fatalf("doctor stray = %d %q", code, errOut)
	}
}

// fakeContention points doctor contention at a /proc of the given
// processes: pid → command line (NUL-separated) and cwd.
func fakeContention(t *testing.T, procs map[string][2]string) {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(p[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p[1], filepath.Join(dir, "cwd")); err != nil {
			t.Fatal(err)
		}
	}
	old := contentionScanner
	contentionScanner = func() contention.Scanner { return contention.Scanner{Root: root, Now: time.Now, ClockTicks: 100} }
	t.Cleanup(func() { contentionScanner = old })
}

// ntm's doctor contention: its table, its JSON and its refusals.
func TestDoctorContention(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	fakeContention(t, map[string][2]string{})
	if code, out, errOut := run("doctor", "contention"); code != 0 || out != "No shared-resource contention detected.\n" || errOut != "" {
		t.Fatalf("no holders = %d %q %q", code, out, errOut)
	}
	if code, out, _ := run("doctor", "contention", "--json"); code != 0 || out != "{\"holders\":[]}\n" {
		t.Fatalf("no holders --json = %d %q", code, out)
	}

	fakeContention(t, map[string][2]string{
		"42": {"br\x00ready\x00", filepath.Join(cwd, "sub")},
		"7":  {"/usr/bin/cargo\x00build\x00", "/elsewhere"},
	})
	code, out, errOut := run("doctor", "contention")
	want := "PID\tAGE\tRESOURCE\tIN_SWARM\tCWD\tCOMMAND\n" +
		"7\t0s\tcargo_registry\tfalse\t/elsewhere\t/usr/bin/cargo build\n" +
		"42\t0s\tbeads_db\ttrue\t" + filepath.Join(cwd, "sub") + "\tbr ready\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("holders = %d %q %q", code, out, errOut)
	}
	code, out, _ = run("doctor", "contention", "--json")
	var got struct {
		Holders []contention.Holder `json:"holders"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil || len(got.Holders) != 2 || got.Holders[1].PID != 42 || !got.Holders[1].InSwarm {
		t.Fatalf("holders --json = %d %v %q", code, err, out)
	}

	for _, c := range []struct {
		args []string
		err  string
	}{
		{[]string{"--terminate-pid", "42"}, "Error: refusing termination without --confirm\n"},
		{[]string{"--terminate-pid", "42", "--confirm"}, "Error: refusing to terminate in-swarm PID 42 without --force\n"},
		{[]string{"--terminate-pid", "1", "--confirm"}, "Error: PID 1 is not a detected contention holder\n"},
	} {
		if code, out, errOut := run(append([]string{"doctor", "contention"}, c.args...)...); code != exitError || out != "" || errOut != c.err {
			t.Fatalf("%v = %d %q %q", c.args, code, out, errOut)
		}
	}
	code, out, _ = run("doctor", "contention", "--terminate-pid", "1", "--confirm", "--json")
	var refusal struct {
		Success   *bool  `json:"success"`
		Error     string `json:"error"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal([]byte(out), &refusal); code != exitError || err != nil || refusal.Success == nil || *refusal.Success ||
		refusal.Error != "PID 1 is not a detected contention holder" || refusal.ErrorCode != "INTERNAL_ERROR" {
		t.Fatalf("--json refusal = %d %v %q", code, err, out)
	}
	if code, _, errOut := run("doctor", "contention", "extra"); code != exitError || !strings.Contains(errOut, `unknown command "extra" for "nhm doctor contention"`) {
		t.Fatalf("contention extra = %d %q", code, errOut)
	}
}
