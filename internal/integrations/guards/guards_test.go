package guards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexFixture's expected hashes come from ACFS's exp.6 predictor
// (evidence/exp6/codex_hook_hash.py), which matched Codex 0.162.1's own
// hooks/list on every entry. They cover a short-timeout event, async, a
// statusMessage with characters JSON may escape, a non-default context
// limit, and a Stop matcher that Codex drops.
const codexFixture = `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/user/.local/bin/dcg"}]},
      {"matcher": "Bash|Edit", "hooks": [{"type": "command", "command": "/home/user/.local/bin/rch", "timeout": 45, "statusMessage": "rch <check> & go"}]}
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "bash '/home/user/.codex/herdr-agent-state.sh' session", "additionalContextLimit": 4000}]}
    ],
    "Stop": [
      {"matcher": "ignored", "hooks": [{"type": "command", "command": "bash /home/user/.acfs/scripts/lib/agent_mail_hook.sh stop", "timeout": 600}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "echo bye", "timeout": 9, "async": true}]}
    ]
  }
}`

var codexWant = map[string]string{
	"pre_tool_use:0:0":  "sha256:ed55698e4e2dc9d21ea0bb04f6e73514ef866f7f59e39b9377b1edc3f81ea24e",
	"pre_tool_use:1:0":  "sha256:9e71f42c1f9701d2e5c7ab789df89be664f857f44f57126573f6f46dc3b1f81c",
	"session_start:0:0": "sha256:06f24a057c0bdb0679c2531fd53e94d4e3cd425fc58043f3921089f46c000172",
	"stop:0:0":          "sha256:cd9f7fd25c657f8d1f5797822fc8dbd7931df3ee298d5cd5a0e9a7b4263e5f1e",
	"session_end:0:0":   "sha256:a625282d8ed369ba31732dc91f3cd9d1ca61a1200e9cb9bb0b9261844b5b26f4",
}

func TestCodexHashMatchesTheExp6Predictor(t *testing.T) {
	var f struct {
		Hooks map[string][]codexGroup `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(codexFixture), &f); err != nil {
		t.Fatal(err)
	}
	n := 0
	for event, groups := range f.Hooks {
		for gi, g := range groups {
			for hi, h := range g.Hooks {
				got, err := CodexHash(event, g.Matcher, h)
				if err != nil {
					t.Fatal(err)
				}
				key := strings.TrimPrefix(CodexKey("", event, gi, hi), ":")
				if got != codexWant[key] {
					t.Errorf("%s: %s, want %s", key, got, codexWant[key])
				}
				n++
			}
		}
	}
	if n != len(codexWant) {
		t.Fatalf("checked %d handlers, want %d", n, len(codexWant))
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func byGuard(rs []Result) map[string]Result {
	out := map[string]Result{}
	for _, r := range rs {
		out[r.Guard] = r
	}
	return out
}

func TestCodexTrustStates(t *testing.T) {
	codexHome := t.TempDir()
	hooks := filepath.Join(codexHome, "hooks.json")
	write(t, hooks, codexFixture)
	env := Env{Home: t.TempDir(), CodexHome: codexHome}

	// No trust recorded: the hooks are held but do not run.
	got := byGuard(Check("codex", env))
	if got[DCG].State != Untrusted || got[RCH].State != Untrusted || got[AgentMailStop].State != Untrusted {
		t.Fatalf("untrusted = %+v", got)
	}
	trust := func(entries map[string]string) {
		var b strings.Builder
		b.WriteString("model = \"x\"\n[hooks.state]\n")
		for k, v := range entries {
			b.WriteString("[hooks.state.\"" + hooks + ":" + k + "\"]\ntrusted_hash = \"" + v + "\"\n")
		}
		write(t, filepath.Join(codexHome, "config.toml"), b.String())
	}
	trust(map[string]string{"pre_tool_use:0:0": codexWant["pre_tool_use:0:0"], "stop:0:0": codexWant["stop:0:0"],
		"pre_tool_use:1:0": "sha256:old"})
	got = byGuard(Check("codex", env))
	if got[DCG].State != Configured || got[AgentMailStop].State != Configured {
		t.Fatalf("trusted = %+v", got)
	}
	if got[RCH].State != Untrusted || !strings.Contains(got[RCH].Detail, "changed since it was trusted") {
		t.Fatalf("changed = %+v", got[RCH])
	}
	if !strings.HasSuffix(got[DCG].Sources[0], "hooks.json:pre_tool_use:0:0") {
		t.Fatalf("source = %v", got[DCG].Sources)
	}
}

func TestCodexProjectLayerNeedsProjectTrust(t *testing.T) {
	codexHome, project := t.TempDir(), t.TempDir()
	projHooks := filepath.Join(project, ".codex", "hooks.json")
	write(t, projHooks, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/local/bin/dcg"}]}]}}`)
	h, _ := CodexHash("PreToolUse", strPtr("Bash"), codexHandler{Type: "command", Command: "/usr/local/bin/dcg"})
	cfg := "[hooks.state.\"" + projHooks + ":pre_tool_use:0:0\"]\ntrusted_hash = \"" + h + "\"\n"
	write(t, filepath.Join(codexHome, "config.toml"), cfg)
	env := Env{Home: t.TempDir(), CodexHome: codexHome, ProjectRoot: project}
	if got := byGuard(Check("codex", env)); got[DCG].State != Missing {
		t.Fatalf("an untrusted project's hooks counted: %+v", got[DCG])
	}
	write(t, filepath.Join(codexHome, "config.toml"), cfg+"[projects.\""+project+"\"]\ntrust_level = \"trusted\"\n")
	if got := byGuard(Check("codex", env)); got[DCG].State != Configured {
		t.Fatalf("a trusted project's hooks did not count: %+v", got[DCG])
	}
}

func strPtr(s string) *string { return &s }

const claudeUser = `{"hooks":{
  "PreToolUse":[{"matcher":"Bash|PowerShell|Monitor","hooks":[{"type":"command","command":"/home/user/.local/bin/dcg"}]}],
  "Stop":[{"hooks":[{"type":"command","command":"bash /home/user/.acfs/scripts/lib/agent_mail_hook.sh stop"}]}]}}`

func TestClaudeSourcesAddUp(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, ".claude", "settings.json"), claudeUser)
	write(t, filepath.Join(project, ".claude", "settings.local.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/home/user/.local/bin/rch"}]}]}}`)
	got := byGuard(Check("claude", Env{Home: home, ProjectRoot: project}))
	if got[DCG].State != Configured || got[DCG].Sources[0] != "user" || got[RCH].State != Configured || got[RCH].Sources[0] != "local" ||
		got[AgentMailStop].State != Configured {
		t.Fatalf("claude = %+v", got)
	}
	// Without the project, rch is missing, with a fix.
	got = byGuard(Check("claude", Env{Home: home}))
	if got[RCH].State != Missing || got[RCH].Fix == "" {
		t.Fatalf("no project = %+v", got[RCH])
	}
	// A matcher that leaves out Bash does not guard Bash.
	write(t, filepath.Join(home, ".claude", "settings.json"),
		`{"hooks":{"PreToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"dcg"}]}]}}`)
	if got := byGuard(Check("claude", Env{Home: home})); got[DCG].State != Missing {
		t.Fatalf("Edit-only matcher = %+v", got[DCG])
	}
	write(t, filepath.Join(home, ".claude", "settings.json"), `{broken`)
	if got := byGuard(Check("claude", Env{Home: home})); got[DCG].State != Missing || !strings.Contains(got[DCG].Detail, "unreadable") {
		t.Fatalf("broken settings = %+v", got[DCG])
	}
}

func TestVerifyLaunchAppliesClaudeMergeRules(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, ".claude", "settings.json"), claudeUser)
	base := Check("claude", Env{Home: home, ProjectRoot: project})
	for _, c := range []struct {
		argv []string
		want State
	}{
		{[]string{"--model", "opus"}, VerifiedForLaunch},
		{[]string{"--bare"}, Missing},
		{[]string{"--setting-sources", "project,local"}, Missing},
		{[]string{"--setting-sources=user,project"}, VerifiedForLaunch},
	} {
		got := byGuard(VerifyLaunch(base, c.argv))
		if got[DCG].State != c.want {
			t.Errorf("%v: %s, want %s (%s)", c.argv, got[DCG].State, c.want, got[DCG].Detail)
		}
	}
	if byGuard(base)[DCG].State != Configured {
		t.Fatal("VerifyLaunch changed its input")
	}
}

func TestAgyPiAndUnknownKinds(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gemini", "config", "hooks.json"),
		`{"dcg":{"enabled":true,"PreToolUse":[{"matcher":"run_command","hooks":[{"type":"command","command":"/home/user/.gemini/config/hooks/dcg-antigravity-hook.py","timeout":6}]}]},
		  "off":{"enabled":false,"PreToolUse":[]}}`)
	got := byGuard(Check("agy", Env{Home: home}))
	if got[DCG].State != Configured || got[RCH].State != Unsupported {
		t.Fatalf("agy = %+v", got)
	}
	// agy's launch is never verified (exp.6): it stays configured.
	if v := byGuard(VerifyLaunch(Check("agy", Env{Home: home}), nil)); v[DCG].State != Configured {
		t.Fatalf("agy verify = %+v", v[DCG])
	}
	if got := byGuard(Check("agy", Env{Home: t.TempDir()})); got[DCG].State != Missing {
		t.Fatalf("agy without hooks = %+v", got[DCG])
	}
	for _, r := range Check("pi", Env{Home: home}) {
		if r.State != Unsupported {
			t.Fatalf("pi = %+v", r)
		}
	}
	for _, r := range Check("grok", Env{Home: home}) {
		if r.State != Missing || r.Fix == "" {
			t.Fatalf("grok = %+v", r)
		}
	}
}

func TestProgram(t *testing.T) {
	for cmd, want := range map[string]string{
		"/home/user/.local/bin/dcg":         "dcg",
		`"/opt/x/dcg" --hook`:               "dcg",
		"bash '/home/user/hook.sh' session": "hook.sh",
		"":                                  "",
	} {
		if got := program(cmd); got != want {
			t.Errorf("program(%q) = %q, want %q", cmd, got, want)
		}
	}
}
