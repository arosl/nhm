package cli

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/spawn"
)

// parseRobotSpawn parses --robot-spawn's flags and builds the request.
func parseRobotSpawn(t *testing.T, args ...string) (spawn.Request, map[string]any) {
	t.Helper()
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf := addRobotSpawnFlags(fs)
	if err := fs.Parse(append([]string{"--robot-spawn=s", "--spawn-dir=/p"}, args...)); err != nil {
		return spawn.Request{}, map[string]any{"error_code": "INVALID_FLAG", "error": err.Error()}
	}
	req, _, refusal := robotSpawnRequest(fs, rf)
	if refusal != nil {
		return req, map[string]any{"error_code": refusal.ErrorCode, "error": refusal.Error, "hint": refusal.Hint}
	}
	return req, nil
}

// --spawn-<kind> takes ntm's spec: a bare count, or count:model[:effort]
// (effort also as model@effort), in ntm's launch order whatever the order
// of the flags (nhm-2p6.4.2).
func TestRobotSpawnAgentSpecModelEffort(t *testing.T) {
	req, refusal := parseRobotSpawn(t, "--spawn-oc=1:openrouter/m@beta", "--spawn-cod=1:gpt-5@max", "--spawn-grok=1", "--spawn-omp=1:opus:high",
		"--spawn-cc=2:opus:high", "--spawn-agy=0", "--spawn-pi=1")
	if refusal != nil {
		t.Fatal(refusal)
	}
	want := []spawn.Want{{Kind: "claude", Count: 2, Model: "opus", Effort: "high"}, {Kind: "codex", Count: 1, Model: "gpt-5", Effort: "max"},
		{Kind: "grok", Count: 1}, {Kind: "omp", Count: 1, Model: "opus", Effort: "high"}, {Kind: "opencode", Count: 1, Model: "openrouter/m@beta"},
		{Kind: "pi", Count: 1}}
	if !reflect.DeepEqual(req.Agents, want) {
		t.Fatalf("agents = %+v\nwant %+v", req.Agents, want)
	}
}

// A bad spec, a negative count or no agent is ntm's INVALID_FLAG, with
// its words and hint; the --spawn- flags ntm does not have are unknown.
func TestRobotSpawnAgySpecRefusesModel(t *testing.T) {
	const specHint = "Use count[:model[:effort]] (effort also as model@effort), e.g. --spawn-cod=2 or --spawn-cod=8:gpt-5.3-codex:high"
	const countHint = "Use non-negative agent counts and a supported assignment strategy"
	for _, tt := range []struct {
		args        []string
		error, hint string
	}{
		{[]string{"--spawn-agy=1:gemini-3"}, `invalid --spawn-agy value "1:gemini-3": antigravity model overrides are not supported; ` +
			`model is pinned to "Gemini 3.8 Flash (High)" (use --agy=N)`, specHint},
		{[]string{"--spawn-cc=1:bad model"}, `invalid --spawn-cc value "1:bad model": invalid characters in model "bad model"; ` +
			`allowed: letters, numbers, . _ / @ : + -`, specHint},
		{[]string{"--spawn-cc=-1"}, "--spawn-cc must be zero or greater, got -1", countHint},
		{[]string{"--spawn-cc=0"}, "no agents specified (use cc, cod, gmi, agy, grok, omp, or oc counts)", countHint},
		{[]string{"--spawn-claude=1"}, "flag provided but not defined: -spawn-claude", ""},
		{[]string{"--spawn-codex=1"}, "flag provided but not defined: -spawn-codex", ""},
		{[]string{"--spawn-opencode=1"}, "flag provided but not defined: -spawn-opencode", ""},
	} {
		_, got := parseRobotSpawn(t, tt.args...)
		if got == nil || got["error_code"] != "INVALID_FLAG" || got["error"] != tt.error || (tt.hint != "" && got["hint"] != tt.hint) {
			t.Errorf("%v = %v", tt.args, got)
		}
	}
}

// --spawn-label makes the session <session>--<label>; ntm's checks refuse
// a session with "--" and a bad label.
func TestRobotSpawnLabel(t *testing.T) {
	req, refusal := parseRobotSpawn(t, "--spawn-cc=1", "--spawn-label=frontend")
	if refusal != nil || req.Session != "s--frontend" {
		t.Fatalf("label = %q %v", req.Session, refusal)
	}
	_, got := parseRobotSpawn(t, "--spawn-cc=1", "--spawn-label=front--end")
	if got["error"] != "invalid label: label must not contain '--' (reserved as separator)" ||
		got["hint"] != "Use a valid label (alphanumeric, dash, underscore)" {
		t.Errorf("a bad label = %v", got)
	}
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf := addRobotSpawnFlags(fs)
	if err := fs.Parse([]string{"--robot-spawn=my--proj", "--spawn-cc=1"}); err != nil {
		t.Fatal(err)
	}
	if _, _, refusal := robotSpawnRequest(fs, rf); refusal == nil || refusal.ErrorCode != "INVALID_FLAG" ||
		refusal.Error != `project name "my--proj" contains '--' which is reserved as the session-label separator` {
		t.Errorf("a session with -- = %+v", refusal)
	}
}

// --spawn-prompt-file reads the prompt from a file or stdin, as ntm's
// loadRobotMessageInput does: not with --spawn-prompt, and not blank.
func TestRobotSpawnPromptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte("Read AGENTS.md.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req, refusal := parseRobotSpawn(t, "--spawn-cc=1", "--spawn-prompt-file="+path)
	if refusal != nil || req.Prompt != "Read AGENTS.md.\n" || req.NoKickoff {
		t.Fatalf("prompt file = %q %v", req.Prompt, refusal)
	}
	old := stdin
	stdin = strings.NewReader("From stdin.")
	defer func() { stdin = old }()
	if req, refusal = parseRobotSpawn(t, "--spawn-cc=1", "--spawn-prompt-file=-"); refusal != nil || req.Prompt != "From stdin." {
		t.Fatalf("stdin = %q %v", req.Prompt, refusal)
	}
	// With --spawn-assign-work the spawn sends nothing, and the file's
	// prompt goes ahead of each work prompt.
	fs := flag.NewFlagSet("nhm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf := addRobotSpawnFlags(fs)
	if err := fs.Parse([]string{"--robot-spawn=s", "--spawn-cc=1", "--spawn-assign-work", "--spawn-prompt-file=" + path}); err != nil {
		t.Fatal(err)
	}
	if req, prompt, refusal := robotSpawnRequest(fs, rf); refusal != nil || prompt != "Read AGENTS.md.\n" || req.Prompt != "" || !req.NoKickoff ||
		!strings.Contains(req.AssignOptions, `"prompt":"Read AGENTS.md.\n"`) {
		t.Fatalf("assign with a prompt file = %+v %q %v", req, prompt, refusal)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--spawn-prompt=x", "--spawn-prompt-file=" + path}, "use either --spawn-prompt or --spawn-prompt-file, not both"},
		{[]string{"--spawn-prompt-file=" + empty}, "--spawn-prompt-file content is empty"},
		{[]string{"--spawn-prompt-file=/nonexistent/p"}, "open --spawn-prompt-file: open /nonexistent/p: no such file or directory"},
	} {
		if _, got := parseRobotSpawn(t, append([]string{"--spawn-cc=1"}, tt.args...)...); got["error"] != tt.want ||
			!strings.HasPrefix(got["hint"].(string), "Use one of --spawn-prompt/--spawn-prompt-file") {
			t.Errorf("%v = %v", tt.args, got)
		}
	}
}

// Each agent of the answer has ntm's name and pane title, in launch
// order: --spawn-names first, then <type>-<NATO word>, and
// <session>__<short type>_<n> (nhm-2p6.4.2).
func TestRobotSpawnNames(t *testing.T) {
	res := spawn.Result{Session: "s--x", DryRun: true, Targets: []spawn.Target{{Kind: "claude"}, {Kind: "claude"}, {Kind: "codex"},
		{Kind: "agy"}, {Kind: "opencode"}}}
	out := spawn.SpawnOutput(res, nil, "json", time.Now())
	spawn.NameAgents(&out, res, spawn.ParseCustomNames(" alice, ,bob"))
	var names, titles []string
	for _, a := range out.WouldCreate {
		names, titles = append(names, a.Name), append(titles, a.Title)
	}
	if want := []string{"alice", "bob", "codex-alpha", "antigravity-bravo", "oc-charlie"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}
	if want := []string{"s--x__cc_1", "s--x__cc_2", "s--x__cod_1", "s--x__agy_1", "s--x__oc_1"}; !reflect.DeepEqual(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}
}

// As ntm's robot spawn: it waits for the agents to be ready only with
// --spawn-wait or a prompt, and after a failed wait it sends no prompt
// (nhm-2p6.4.10).
func TestRobotSpawnNoWaitWithoutSpawnWait(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tt := range []struct {
		args   []string
		noWait bool
	}{
		{nil, true},
		{[]string{"--spawn-wait"}, false},
		{[]string{"--spawn-prompt=Read AGENTS.md."}, false},
		{[]string{"--spawn-assign-work"}, true},
		{[]string{"--spawn-assign-work", "--spawn-prompt=Read AGENTS.md."}, false},
	} {
		req, refusal := parseRobotSpawn(t, append([]string{"--spawn-cc=1"}, tt.args...)...)
		if refusal != nil || req.NoWait != tt.noWait || !req.KickoffAllReady {
			t.Errorf("%v: NoWait %v (want %v), KickoffAllReady %v, %v", tt.args, req.NoWait, tt.noWait, req.KickoffAllReady, refusal)
		}
	}
}

// --spawn-stagger-mode and --spawn-stagger-delay pace the prompts, with
// ntm's defaults (none, 30s) under the [spawn] table of nhm's config, and
// ntm's refusals.
func TestRobotSpawnStagger(t *testing.T) {
	const promptHint = "Use one of --spawn-prompt/--spawn-prompt-file, --spawn-stagger-mode=none|fixed|smart, and a " +
		"--spawn-stagger-delay such as 20s (0-5m); fix any invalid [spawn] config value"
	const staggerHint = "Use --spawn-stagger-mode=none|fixed|smart and a --spawn-stagger-delay between 0 and 5m (e.g. 20s)"
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, tt := range []struct {
		config string
		args   []string
		mode   string
		delay  time.Duration
	}{
		{"", nil, "none", 30 * time.Second},
		{"", []string{"--spawn-stagger-mode=fixed", "--spawn-stagger-delay=20s"}, "fixed", 20 * time.Second},
		{"[spawn]\nstagger_mode = \"smart\"\nstagger_delay = \"12s\"\n", nil, "smart", 12 * time.Second},
		{"[spawn]\nstagger_mode = \"smart\"\nstagger_delay = \"12s\"\n", []string{"--spawn-stagger-mode=none"}, "none", 12 * time.Second},
	} {
		writeAll(t, filepath.Join(dir, "nhm", "config.toml"), tt.config)
		req, refusal := parseRobotSpawn(t, append([]string{"--spawn-cc=1"}, tt.args...)...)
		if refusal != nil || req.StaggerMode != tt.mode || req.StaggerDelay != tt.delay {
			t.Errorf("%q %v: %s %v %v", tt.config, tt.args, req.StaggerMode, req.StaggerDelay, refusal)
		}
	}
	for _, tt := range []struct {
		config      string
		args        []string
		error, hint string
	}{
		{"", []string{"--spawn-stagger-mode=slow"}, `--spawn-stagger-mode must be one of none, fixed, or smart; got "slow"`, staggerHint},
		{"", []string{"--spawn-stagger-delay=6m"}, "--spawn-stagger-delay must be between 0 and 5m0s", staggerHint},
		{"", []string{"--spawn-stagger-delay=soon"}, `invalid --spawn-stagger-delay "soon": time: invalid duration "soon"`, promptHint},
		{"[spawn]\nstagger_mode = \"slow\"\n", nil, `[spawn] stagger_mode must be one of none, fixed, or smart; got "slow"`, promptHint},
		{"[spawn]\nstagger_delay = \"9m\"\n", nil, "[spawn] stagger_delay must be between 0 and 5m0s", promptHint},
	} {
		writeAll(t, filepath.Join(dir, "nhm", "config.toml"), tt.config)
		_, got := parseRobotSpawn(t, append([]string{"--spawn-cc=1"}, tt.args...)...)
		if got["error_code"] != "INVALID_FLAG" || got["error"] != tt.error || got["hint"] != tt.hint {
			t.Errorf("%q %v = %v", tt.config, tt.args, got)
		}
	}
}
