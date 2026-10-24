package guards

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arosl/nhm/internal/integrations/kinds"
)

// piHarness loads the extension as pi does (its default export gets the
// pi object) with a fake pi.exec, and runs its tool_call hook on each
// case. It prints the hook's answer and the exec calls per case.
const piHarness = `import guard from "./guard.mjs";
const cases = JSON.parse(process.argv[2]);
const out = [];
for (const c of cases) {
  let hook;
  const calls = [];
  const pi = {
    on(name, fn) { if (name === "tool_call") hook = fn; },
    async exec(cmd, args, opts) {
      calls.push({ cmd, args, opts });
      if (c.throws) throw new Error(c.throws);
      return c.result;
    },
  };
  guard(pi);
  const res = await hook(c.event, {});
  out.push({ res: res ?? null, calls });
}
console.log(JSON.stringify(out));
`

type piCase struct {
	Event  map[string]any `json:"event"`
	Result map[string]any `json:"result,omitempty"`
	Throws string         `json:"throws,omitempty"`
}

type piOutcome struct {
	Res *struct {
		Block  bool   `json:"block"`
		Reason string `json:"reason"`
	} `json:"res"`
	Calls []struct {
		Cmd  string         `json:"cmd"`
		Args []string       `json:"args"`
		Opts map[string]any `json:"opts"`
	} `json:"calls"`
}

// node runs a harness beside the extension, with pi's fail-closed hooks
// set as nhm's panes set them (failClosed) or not.
func node(t *testing.T, harness string, failClosed bool, args ...string) []byte {
	t.Helper()
	bin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the extension's decisions need a JavaScript runtime")
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "guard.mjs"), string(PiExtension("/opt/dcg/bin/dcg")))
	write(t, filepath.Join(dir, "harness.mjs"), harness)
	cmd := exec.CommandContext(t.Context(), bin, append([]string{filepath.Join(dir, "harness.mjs")}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if failClosed {
		cmd.Env = append(cmd.Env, "PI_EXTENSION_HOOKS_FAIL_CLOSED=1")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v: %s", err, out)
	}
	return out
}

func runPiGuard(t *testing.T, cases []piCase) []piOutcome {
	t.Helper()
	in, _ := json.Marshal(cases)
	out := node(t, piHarness, true, string(in))
	var got []piOutcome
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(cases) {
		t.Fatalf("harness output %q: %v", out, err)
	}
	return got
}

// Without fail-closed hooks the extension does not load, and a pi that
// cannot load an -e extension does not start (the sandbox run).
func TestPiGuardNeedsFailClosedHooks(t *testing.T) {
	const load = `import guard from "./guard.mjs";
try {
  guard({ on() {} });
  console.log("loaded");
} catch (e) {
  console.log("threw: " + e.message);
}
`
	if out := strings.TrimSpace(string(node(t, load, false))); !strings.HasPrefix(out, "threw: nhm's pi guard needs PI_EXTENSION_HOOKS_FAIL_CLOSED=1") {
		t.Fatalf("without the env: %s", out)
	}
	if out := strings.TrimSpace(string(node(t, load, true))); out != "loaded" {
		t.Fatalf("with the env: %s", out)
	}
}

func bash(command any) map[string]any {
	return map[string]any{"type": "tool_call", "toolName": "bash", "toolCallId": "toolu_1", "input": map[string]any{"command": command}}
}

func dcgSays(code int, decision string) map[string]any {
	return map[string]any{"code": code, "killed": false, "stderr": "",
		"stdout": `{"decision":"` + decision + `","rule_id":"core.git:reset-hard","reason":"git reset --hard destroys uncommitted changes."}`}
}

// The extension runs only what dcg allows: every answer but exit 0 with
// "allow" blocks the call, and so does a guard that cannot run dcg.
func TestPiGuardDecides(t *testing.T) {
	cases := []struct {
		name  string
		c     piCase
		block string // "" when the call runs
		dcg   bool   // dcg is asked
	}{
		{"allowed", piCase{Event: bash("ls -la"), Result: dcgSays(0, "allow")}, "", true},
		{"denied", piCase{Event: bash("git reset --hard"), Result: dcgSays(1, "deny")},
			"dcg denies it: git reset --hard destroys uncommitted changes. [core.git:reset-hard]", true},
		{"allow with a deny exit", piCase{Event: bash("x"), Result: dcgSays(1, "allow")}, "dcg gave no allow (exit 1)", true},
		{"exit 0 without allow", piCase{Event: bash("x"), Result: dcgSays(0, "warn")}, "dcg gave no allow (exit 0)", true},
		{"dcg error", piCase{Event: bash("x"), Result: map[string]any{"code": 3, "killed": false, "stdout": "", "stderr": "bad config"}},
			"dcg gave no allow (exit 3)", true},
		{"dcg killed at the limit", piCase{Event: bash("x"), Result: map[string]any{"code": -9, "killed": true, "stdout": "", "stderr": ""}},
			"killed at the limit", true},
		{"killed after an allow", piCase{Event: bash("x"), Result: map[string]any{"code": 0, "killed": true, "stdout": `{"decision":"allow"}`}},
			"killed at the limit", true},
		{"no JSON", piCase{Event: bash("x"), Result: map[string]any{"code": 0, "killed": false, "stdout": "allow"}}, "dcg gave no allow", true},
		{"exec denied by pi's policy", piCase{Event: bash("x"), Throws: "Capability 'exec' denied by policy (deny_caps)"},
			"Capability 'exec' denied by policy", true},
		{"no command string", piCase{Event: bash(42)}, "the bash call has no command string", false},
		{"no input", piCase{Event: map[string]any{"toolName": "bash"}}, "the bash call has no command string", false},
		{"read", piCase{Event: map[string]any{"toolName": "read", "input": map[string]any{"path": "x"}}}, "", false},
		{"write", piCase{Event: map[string]any{"toolName": "write", "input": map[string]any{"path": "x", "content": "rm -rf /"}}}, "", false},
		{"hub", piCase{Event: map[string]any{"toolName": "hub", "input": map[string]any{"op": "start", "application": "rm"}}},
			"pi's hub tool starts programs that dcg cannot screen", false},
		{"debug", piCase{Event: map[string]any{"toolName": "debug", "input": map[string]any{"action": "launch"}}}, "pi's debug tool", false},
		{"subagent", piCase{Event: map[string]any{"toolName": "subagent", "input": map[string]any{}}}, "pi's subagent tool", false},
		{"hub, spelt otherwise", piCase{Event: map[string]any{"toolName": " Hub ", "input": map[string]any{}}}, "pi's hub tool", false},
		{"xdev list", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "list"}}}, "", false},
		{"xdev describe", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "describe", "name": "hub"}}}, "", false},
		{"xdev promote", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "promote", "name": "hub"}}}, "", false},
		{"xdev, another action", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "exec", "name": "hub"}}},
			"xdev exec is no call that nhm's pi guard knows", false},
		{"xdev, no input", piCase{Event: map[string]any{"toolName": "xdev"}}, "xdev  is no call", false},
		// xdev runs a tool without the hook seeing it (the sandbox run).
		{"xdev runs hub", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run", "name": "Hub",
			"args": map[string]any{"op": "start", "application": "touch"}}}}, "xdev runs hub unseen by the guard", false},
		{"xdev Runs hub", piCase{Event: map[string]any{"toolName": "XDEV", "input": map[string]any{"action": " Run", "name": " hub "}}},
			"xdev runs hub", false},
		{"xdev runs hub, input as JSON", piCase{Event: map[string]any{"toolName": "xdev", "input": `{"action": "run", "name": "hub"}`}},
			"xdev runs hub", false},
		{"xdev runs debug", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run", "name": "debug"}}},
			"xdev runs debug", false},
		{"xdev runs a tool nhm does not know", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run",
			"name": "shell"}}}, "xdev runs shell", false},
		{"xdev runs lsp", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run", "name": "lsp",
			"args": map[string]any{"action": "status"}}}}, "", false},
		{"xdev runs jobs", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run", "name": "jobs",
			"args": "not JSON"}}}, "", false},
		{"xdev input not JSON", piCase{Event: map[string]any{"toolName": "xdev", "input": "{"}}, "nhm's pi guard: SyntaxError", false},
		{"xdev runs bash", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run", "name": "bash",
			"args": `{"command": "git reset --hard"}`}}, Result: dcgSays(1, "deny")}, "dcg denies it", true},
		{"xdev runs bash, bad args", piCase{Event: map[string]any{"toolName": "xdev", "input": map[string]any{"action": "run", "name": "bash",
			"args": "{"}}}, "nhm's pi guard: SyntaxError", false},
	}
	in := make([]piCase, len(cases))
	for i, c := range cases {
		in[i] = c.c
	}
	got := runPiGuard(t, in)
	for i, c := range cases {
		g := got[i]
		switch {
		case c.block == "" && g.Res != nil:
			t.Errorf("%s: blocked (%q), want it to run", c.name, g.Res.Reason)
		case c.block != "" && (g.Res == nil || !g.Res.Block || !strings.HasPrefix(g.Res.Reason, "nhm's pi guard: ") ||
			!strings.Contains(g.Res.Reason, c.block)):
			t.Errorf("%s: answer %+v, want a block naming %q", c.name, g.Res, c.block)
		case c.dcg != (len(g.Calls) == 1):
			t.Errorf("%s: exec calls %+v, want dcg asked: %v", c.name, g.Calls, c.dcg)
		}
	}
	// dcg's robot test in the Bash hook's dialect, the command after "--"
	// (a command that starts with "-" is no option), within pi's 5 s limit.
	call := got[0].Calls[0]
	if call.Cmd != "/opt/dcg/bin/dcg" || !slices.Equal(call.Args, []string{"test", "--robot", "--dialect", "posix", "--", "ls -la"}) ||
		call.Opts["timeout"] != float64(3000) {
		t.Fatalf("exec = %+v", call)
	}
}

func TestPiExtensionNamesDCG(t *testing.T) {
	for _, dcg := range []string{"/usr/local/bin/dcg", `/o "dd"/b\in/dcg`, "/opt/dcg\u2028/dcg"} {
		src := string(PiExtension(dcg))
		lit, _ := json.Marshal(dcg)
		if !strings.Contains(src, "\nconst DCG = "+string(lit)+";\n") || strings.Contains(src, "__DCG__") {
			t.Fatalf("source for %q:\n%s", dcg, src)
		}
	}
}

func TestWritePiExtension(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pi")
	if _, err := WritePiExtension(dir, "dcg"); err == nil {
		t.Fatal("a relative dcg path was written")
	}
	path, err := WritePiExtension(dir, "/usr/bin/dcg")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	dinfo, _ := os.Stat(dir)
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "nhm-guard-") || filepath.Ext(path) != ".ts" ||
		string(data) != string(PiExtension("/usr/bin/dcg")) || info.Mode().Perm() != 0o600 || dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("path %s, mode %v, dir %v:\n%s", path, info.Mode(), dinfo.Mode(), data)
	}
	// The same dcg gives the same file; a changed file is written again.
	write(t, path, "export default function () {}")
	again, err := WritePiExtension(dir, "/usr/bin/dcg")
	if data2, _ := os.ReadFile(again); err != nil || again != path || string(data2) != string(data) {
		t.Fatalf("again = %s %v", again, err)
	}
	// Another dcg gives another file.
	other, err := WritePiExtension(dir, "/opt/bin/dcg")
	if err != nil || other == path {
		t.Fatalf("other = %s %v", other, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("dir holds %v, want the two extensions and no temp file", entries)
	}
}

// piDCG installs a dcg (fakeDCG) and returns its path.
func piDCG(t *testing.T) string {
	t.Helper()
	fakeDCG(t, `{"decision":"allow"}`, 0)
	return DCGPath
}

func TestCheckPi(t *testing.T) {
	dcg := piDCG(t)
	got := byGuard(Check("pi", Env{Home: t.TempDir()}))
	if got[DCG].State != Configured || got[DCG].Sources[0] != "nhm's pi guard extension" || !strings.Contains(got[DCG].Detail, dcg) ||
		got[RCH].State != Unsupported || !strings.Contains(got[RCH].Detail, "cannot route a build through rch") ||
		got[AgentMailStop].State != Unsupported || len(got) != len(Guards) {
		t.Fatalf("pi = %+v", got)
	}
	DCGPath = filepath.Join(t.TempDir(), "no-dcg")
	got = byGuard(Check("pi", Env{Home: t.TempDir()}))
	if got[DCG].State != Missing || !strings.Contains(got[DCG].Detail, "dcg is not installed") || got[DCG].Fix == "" ||
		got[RCH].State != Unsupported {
		t.Fatalf("pi without dcg = %+v", got)
	}
}

// A launch keeps pi's dcg guard only when -e loads nhm's own extension and
// the last --extension-policy is permissive.
func TestVerifyLaunchPi(t *testing.T) {
	piDCG(t)
	dir := t.TempDir()
	ext, err := WritePiExtension(filepath.Join(dir, "pi"), "/usr/bin/dcg")
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.ts")
	write(t, other, "export default function () {}")
	near := filepath.Join(dir, "near.ts")
	write(t, near, string(PiExtension("/usr/bin/dcg"))+"\n")
	relative := filepath.Join(dir, "relative.ts")
	write(t, relative, string(PiExtension("dcg")))
	results := Check("pi", Env{Home: t.TempDir()})
	cases := []struct {
		argv []string
		want State
		why  string
	}{
		{[]string{"pi", "-e", ext, "--extension-policy", "permissive"}, VerifiedForLaunch, ""},
		{[]string{"pi", "--model", "m", "--extension=" + ext, "--extension-policy=permissive"}, VerifiedForLaunch, ""},
		{[]string{"pi", "-e", other, "-e", ext, "--extension-policy", "permissive"}, VerifiedForLaunch, ""},
		{[]string{"pi"}, Missing, "does not load nhm's pi guard extension"},
		{[]string{"pi", "-e", other, "--extension-policy", "permissive"}, Missing, "does not load"},
		{[]string{"pi", "-e", near, "--extension-policy", "permissive"}, Missing, "does not load"},
		{[]string{"pi", "-e", relative, "--extension-policy", "permissive"}, Missing, "does not load"},
		{[]string{"pi", "-e", filepath.Join(dir, "gone.ts"), "--extension-policy", "permissive"}, Missing, "does not load"},
		{[]string{"pi", "-e", ext}, Missing, `--extension-policy is ""`},
		{[]string{"pi", "-e", ext, "--extension-policy", "balanced"}, Missing, `"balanced": only permissive`},
		{[]string{"pi", "-e", ext, "--extension-policy", "permissive", "--extension-policy", "safe"}, Missing, `"safe"`},
		{[]string{"pi", "-e"}, Missing, "does not load"},
	}
	for _, c := range cases {
		got := byGuard(VerifyLaunch(results, c.argv))
		if got[DCG].State != c.want || !strings.Contains(got[DCG].Detail, c.why) || got[RCH].State != Unsupported {
			t.Errorf("%v: %+v", c.argv, got[DCG])
		}
	}
}

// The registry's pi launch is what the check verifies.
func TestPiLaunchFlagsMatchTheCheck(t *testing.T) {
	p := kinds.Lookup("pi")
	if !p.GuardSupported || p.SettingsFlag != "-e" || !slices.Equal(p.SettingsArgs, []string{"--extension-policy", PiPolicy}) ||
		p.GuardEnv["PI_EXTENSION_HOOKS_FAIL_CLOSED"] != "1" {
		t.Fatalf("pi = %+v", p)
	}
	piDCG(t)
	ext, err := WritePiExtension(t.TempDir(), "/usr/bin/dcg")
	if err != nil {
		t.Fatal(err)
	}
	argv := append(append([]string{"pi", p.SettingsFlag, ext}, p.SettingsArgs...), "--model", "m")
	if got := byGuard(VerifyLaunch(Check("pi", Env{Home: t.TempDir()}), argv)); got[DCG].State != VerifiedForLaunch {
		t.Fatalf("pi's own launch = %+v", got[DCG])
	}
}
