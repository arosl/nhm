package guards

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ompGuard writes the extension that dcg install --omp writes (dcg
// 0.15.4's first line and DCG_BIN line) into dir, running bin.
func ompGuard(t *testing.T, dir, bin string) string {
	t.Helper()
	path := filepath.Join(dir, "extensions", "dcg-guard.ts")
	write(t, path, ompMarker+" - do not edit.\n// Routes every Oh My Pi bash tool call through dcg.\nimport path from \"node:path\";\n\n"+
		"const DCG_BIN = "+strconv.Quote(bin)+";\nconst DCG_CHILD_TIMEOUT_MS = 30000;\n")
	return path
}

// dcgFile is an executable file standing in for dcg.
func dcgFile(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dcg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// omp's dcg guard is the extension that dcg install --omp writes, in the
// agent dir's or the project's extensions dir; rch and the stop hook have
// no place in omp. A launch verifies it unless --no-extensions (or a
// --profile, for the agent dir's) keeps omp from loading it (nhm-yncb).
func TestOmpGuard(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	agent := filepath.Join(home, ".omp", "agent")
	env := Env{Home: home, OmpAgentDir: agent, ProjectRoot: project}

	got := byGuard(Check("omp", env))
	if got[DCG].State != Missing || got[DCG].Fix != "dcg install --omp (it writes dcg's omp extension)" ||
		!strings.Contains(got[DCG].Detail, filepath.Join(agent, "extensions", "dcg-guard.ts")+" does not exist") ||
		got[RCH].State != Unsupported || got[AgentMailStop].State != Unsupported {
		t.Fatalf("no extension = %+v", got)
	}

	bin := dcgFile(t)
	ompGuard(t, agent, bin)
	got = byGuard(Check("omp", env))
	if got[DCG].State != Configured || strings.Join(got[DCG].Sources, ",") != "user" {
		t.Fatalf("user extension = %+v", got[DCG])
	}
	argv := []string{"omp", "--model", "m", "--thinking", "high"}
	if v := byGuard(VerifyLaunch(Check("omp", env), argv)); v[DCG].State != VerifiedForLaunch || v[RCH].State != Unsupported {
		t.Fatalf("verify = %+v", v)
	}
	for _, extra := range []string{"--no-extensions", "--profile=work"} {
		if v := byGuard(VerifyLaunch(Check("omp", env), append(argv, extra))); v[DCG].State != Missing || v[DCG].Detail == "" {
			t.Fatalf("verify with %s = %+v", extra, v[DCG])
		}
	}

	// The project's extension counts too, and a --profile keeps it.
	penv := Env{Home: home, OmpAgentDir: filepath.Join(home, "elsewhere"), ProjectRoot: project}
	ompGuard(t, filepath.Join(project, ".omp"), bin)
	got = byGuard(Check("omp", penv))
	if got[DCG].State != Configured || strings.Join(got[DCG].Sources, ",") != "project" {
		t.Fatalf("project extension = %+v", got[DCG])
	}
	if v := byGuard(VerifyLaunch(Check("omp", penv), append(argv, "--profile", "work"))); v[DCG].State != VerifiedForLaunch {
		t.Fatalf("project extension with --profile = %+v", v[DCG])
	}

	// omp's disabledExtensions, in either settings file, keeps it off.
	cfg := filepath.Join(agent, "config.yml")
	write(t, cfg, "setupVersion: 2\ndisabledExtensions:\n  - extension-module:dcg-guard\n")
	got = byGuard(Check("omp", env))
	if got[DCG].State != Missing || !strings.Contains(got[DCG].Detail, "disabledExtensions in "+cfg) ||
		got[DCG].Fix != "remove extension-module:dcg-guard from disabledExtensions in "+cfg {
		t.Fatalf("disabled = %+v", got[DCG])
	}
	write(t, cfg, "disabledExtensions: [extension-module:other]\n")
	if got = byGuard(Check("omp", env)); got[DCG].State != Configured {
		t.Fatalf("another extension disabled = %+v", got[DCG])
	}
	write(t, cfg, "disabledExtensions: [unclosed\n")
	if got = byGuard(Check("omp", env)); got[DCG].State != Missing || !strings.Contains(got[DCG].Detail, "cannot be read") {
		t.Fatalf("unreadable settings = %+v", got[DCG])
	}
}

// A file without dcg's marker is not dcg's extension, and an extension
// whose dcg is gone screens nothing.
func TestOmpGuardChecksTheExtension(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "agent")
	env := Env{Home: t.TempDir(), OmpAgentDir: agent}
	path := filepath.Join(agent, "extensions", "dcg-guard.ts")
	write(t, path, "export default function (pi) {}\n")
	if got := byGuard(Check("omp", env)); got[DCG].State != Missing || !strings.Contains(got[DCG].Detail, "is not the extension that dcg install --omp writes") {
		t.Fatalf("not dcg's = %+v", got[DCG])
	}
	gone := filepath.Join(t.TempDir(), "dcg")
	ompGuard(t, agent, gone)
	if got := byGuard(Check("omp", env)); got[DCG].State != Missing || !strings.Contains(got[DCG].Detail, "runs "+gone+", which is not an executable file") {
		t.Fatalf("dcg gone = %+v", got[DCG])
	}
}

// omp finds its agent dir by profile, then PI_CODING_AGENT_DIR, then
// ~/.omp/agent.
func TestOmpAgentDir(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{nil, "/h/.omp/agent"},
		{map[string]string{"OMP_PROFILE": "work"}, "/h/.omp/profiles/work/agent"},
		{map[string]string{"PI_PROFILE": "work"}, "/h/.omp/profiles/work/agent"},
		{map[string]string{"OMP_PROFILE": "work", "PI_CODING_AGENT_DIR": "/x"}, "/h/.omp/profiles/work/agent"},
		{map[string]string{"OMP_PROFILE": "default", "PI_CODING_AGENT_DIR": "/x"}, "/x"},
		{map[string]string{"OMP_PROFILE": "../up"}, "/h/.omp/agent"},
		{map[string]string{"PI_CODING_AGENT_DIR": "/x"}, "/x"},
	} {
		if got := ompAgentDir("/h", func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: %s, want %s", c.env, got, c.want)
		}
	}
}
