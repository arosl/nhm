package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/arosl/nhm/internal/core/spawn"
)

// parseVerbArgs parses args as spawn's or add's flags, the spawn-only
// ones with spawn, and builds the request.
func parseVerbArgs(t *testing.T, add bool, args ...string) (*spawnFlags, spawn.Request, error) {
	t.Helper()
	fs := pflag.NewFlagSet("spawn", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := addSpawnFlags(fs)
	if !add {
		addSpawnOnlyFlags(fs, f)
	}
	if err := fs.Parse(args); err != nil {
		return f, spawn.Request{}, err
	}
	req, err := f.request("swarm", add)
	return f, req, err
}

// spawn's stagger is ntm's: --stagger-mode none and --stagger-delay 30s by
// default, the [spawn] keys beneath the flags, --stagger[=D] the legacy
// interval (90s bare), which also counts as choosing the mode. add takes
// no [spawn] defaults and no --stagger, as ntm's add has no stagger
// (nhm-2p6.4.14).
func TestSpawnStaggerDefaults(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("NHM_CONFIG", config)
	for _, c := range []struct {
		add    bool
		config string
		args   []string
		mode   string
		delay  time.Duration
		legacy time.Duration
		err    string
	}{
		{args: []string{"--cc=1"}, mode: "none", delay: 30 * time.Second},
		{args: []string{"--cc=1", "--stagger"}, mode: "none", delay: 30 * time.Second, legacy: 90 * time.Second},
		{args: []string{"--cc=1", "--stagger=2m"}, mode: "none", delay: 30 * time.Second, legacy: 2 * time.Minute},
		{args: []string{"--cc=1", "--stagger=0"}, mode: "none", delay: 30 * time.Second},
		{args: []string{"--cc=1", "--stagger=-1s"}, err: "stagger duration cannot be negative"},
		{args: []string{"--cc=1", "--stagger=6m"}, err: "stagger duration cannot exceed 5m0s"},
		{config: "[spawn]\nstagger_mode = \"fixed\"\nstagger_delay = \"10s\"\n", args: []string{"--cc=1"}, mode: "fixed", delay: 10 * time.Second},
		{config: "[spawn]\nstagger_mode = \"fixed\"\n", args: []string{"--cc=1", "--stagger"}, mode: "none", delay: 30 * time.Second,
			legacy: 90 * time.Second},
		{config: "[spawn]\nstagger_mode = \"fixed\"\n", args: []string{"--cc=1", "--stagger-delay=5s"}, mode: "fixed", delay: 5 * time.Second},
		{config: "[spawn]\nstagger_mode = \"bogus\"\n", args: []string{"--cc=1"}, err: "[spawn] stagger_mode must be one of none, fixed, or smart"},
		{add: true, config: "[spawn]\nstagger_mode = \"fixed\"\n", args: []string{"--cc=1"}, mode: "none", delay: 30 * time.Second},
		{add: true, args: []string{"--cc=1", "--stagger"}, err: "unknown flag: --stagger"},
	} {
		if err := os.WriteFile(config, []byte(c.config), 0o600); err != nil {
			t.Fatal(err)
		}
		_, req, err := parseVerbArgs(t, c.add, c.args...)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("add %v %v %q: err %v, want %q", c.add, c.args, c.config, err, c.err)
			}
			continue
		}
		if err != nil || req.StaggerMode != c.mode || req.StaggerDelay != c.delay || req.StaggerLegacy != c.legacy {
			t.Errorf("add %v %v %q: %q %v %v %v", c.add, c.args, c.config, req.StaggerMode, req.StaggerDelay, req.StaggerLegacy, err)
		}
	}
}

// With no agent flag, spawn takes the counts of the project's [defaults]
// agents in its .nhm/config.toml, in ntm's order, and says so; a type nhm
// has no kind for refuses. An agent flag, or add, takes none. ntm's
// .ntm/config.toml plays no part (nhm-2p6.4.14).
func TestSpawnProjectDefaults(t *testing.T) {
	t.Setenv("NHM_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	project := t.TempDir()
	writeAll(t, filepath.Join(project, ".ntm", "config.toml"), "[defaults]\nagents = { cc = 3 }\n")
	sub := filepath.Join(project, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f, req, err := parseVerbArgs(t, false, "--project", sub)
	if err != nil || len(req.Agents) != 0 || f.defaultsNote != "" {
		t.Fatalf("ntm's file = %+v %q %v", req.Agents, f.defaultsNote, err)
	}
	write := func(body string) {
		t.Helper()
		writeAll(t, filepath.Join(project, ".nhm", "config.toml"), body)
	}
	write("[defaults]\nagents = { cod = 1, claude = 2 }\n")
	f, req, err = parseVerbArgs(t, false, "--project", sub)
	if err != nil || len(req.Agents) != 2 || req.Agents[0] != (spawn.Want{Kind: "claude", Count: 2}) ||
		req.Agents[1] != (spawn.Want{Kind: "codex", Count: 1}) || f.defaultsNote != "Using default configuration: 2 cc, 1 cod" {
		t.Fatalf("defaults = %+v %q %v", req.Agents, f.defaultsNote, err)
	}
	if f, req, err = parseVerbArgs(t, false, "--project", project, "--omp=1"); err != nil || len(req.Agents) != 1 || req.Agents[0].Kind != "omp" ||
		f.defaultsNote != "" {
		t.Fatalf("with a flag = %+v %q %v", req.Agents, f.defaultsNote, err)
	}
	if _, req, err = parseVerbArgs(t, true, "--project", project); err != nil || len(req.Agents) != 0 {
		t.Fatalf("add = %+v %v", req.Agents, err)
	}
	write("[defaults]\nagents = { cc = 1, gmi = 1 }\n")
	if _, _, err = parseVerbArgs(t, false, "--project", project); err == nil ||
		err.Error() != "[defaults] agents: 1 gmi, and nhm has no kind for gmi" {
		t.Fatalf("gmi = %v", err)
	}
}

// The note goes out in text, before the run (nhm-2p6.4.14).
func TestSpawnProjectDefaultsNote(t *testing.T) {
	s, _, h := newSendInstance(t)
	spawnHerdr(t, s, h)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("NHM_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	t.Setenv("PATH", t.TempDir())
	project := t.TempDir()
	writeAll(t, filepath.Join(project, ".nhm", "config.toml"), "[defaults]\nagents = { cc = 1 }\n")
	code, out, errOut := run("spawn", "swarm", "--project", project, "--allow-unguarded", "--dry-run", "--socket", s.SocketPath)
	if code != 0 || !strings.HasPrefix(out, "Using default configuration: 1 cc\n") || !strings.Contains(out, "would start 1 agents in swarm") {
		t.Fatalf("note = %d %q %q", code, out, errOut)
	}
}
