package kinds

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tests need no agent CLI on PATH: each kind resolves to its own file.
func init() {
	ResolveProgram = func(name string) (string, error) { return "/opt/" + name + "/bin/" + name, nil }
}

func withResolved(t *testing.T, resolved string) {
	t.Helper()
	old := ResolveProgram
	ResolveProgram = func(string) (string, error) { return resolved, nil }
	t.Cleanup(func() { ResolveProgram = old })
}

// The program on PATH must resolve to the kind's CLI, not to a shell or
// an interpreter behind a symlink (the review of 3643fe5).
func TestProgramIsKind(t *testing.T) {
	for resolved, want := range map[string]bool{
		"/home/u/.local/share/claude/versions/2.1.296":     true,
		"/home/u/.grok/downloads/grok-linux-x86_64":        true,
		"/usr/lib/node_modules/@openai/codex/bin/codex.js": true,
		"/home/u/.local/bin/codex":                         true,
		"/usr/bin/bash":                                    false,
		"/home/u/claude/bin/python3.12":                    false,
		"/opt/claude/node":                                 false,
		"/usr/local/bin/other":                             false,
		"/home/u/claudette/bin/x":                          false,
	} {
		withResolved(t, resolved)
		if got := ProgramIsKind("claude", "claude") || ProgramIsKind("grok", "grok") || ProgramIsKind("codex", "codex"); got != want {
			t.Errorf("%s: %v, want %v", resolved, got, want)
		}
	}
	c, _ := NewRegistry().Get("claude")
	withResolved(t, "/usr/bin/dash")
	if got := c.Resume("s-1", "", "", ""); got != nil {
		t.Fatalf("claude as dash resumes: %v", got)
	}
	old := ResolveProgram
	ResolveProgram = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { ResolveProgram = old }()
	if got := c.Resume("s-1", "", "", ""); got != nil {
		t.Fatalf("claude not on PATH resumes: %v", got)
	}
}

func TestBuiltins(t *testing.T) {
	r := NewRegistry()
	if got := strings.Join(r.Names(), ","); got != "claude,codex,agy,pi,omp,opencode,grok" {
		t.Fatalf("names = %s", got)
	}
	c, _ := r.Get("claude")
	if !c.Builtin || c.ModelFlag != "--model" || !slices.Equal(c.InterruptKeys, []string{"esc"}) || !c.CodeReview || c.Continue == nil {
		t.Fatalf("claude = %+v", c)
	}
	// A resumed Claude runs as guarded as the spawned one (the review of
	// b0902bb): the model, the effort and the hooks' --settings stay.
	if got := c.Resume("s-1", "opus", "", `{"hooks":{}}`); !slices.Equal(got, []string{"claude", "--dangerously-skip-permissions",
		"--resume", "s-1", "--model", "opus", "--effort", "xhigh", "--settings", `{"hooks":{}}`}) {
		t.Fatalf("resume = %v", got)
	}
	if got := c.Resume("s-1", "", "high", ""); !slices.Equal(got, []string{"claude", "--dangerously-skip-permissions", "--resume", "s-1", "--effort", "high"}) {
		t.Fatalf("resume without model and settings = %v", got)
	}
	// A session id that is not a plain id never reaches an argv.
	for _, bad := range []string{"-rf", "--help", "", "a b", "x;y", "../x"} {
		if got := c.Resume(bad, "", "", ""); got != nil {
			t.Errorf("resume %q = %v", bad, got)
		}
	}
	// ntm's launch flags and effort.
	if !slices.Equal(c.LaunchArgs, []string{"--dangerously-skip-permissions"}) || !slices.Equal(c.Effort(""), []string{"--effort", "xhigh"}) ||
		!slices.Equal(c.Effort("high"), []string{"--effort", "high"}) || c.Effort("x y") != nil || c.SettingsFlag != "--settings" {
		t.Fatalf("claude launch = %+v", c)
	}
	x, _ := r.Get("codex")
	if got := x.Resume("thr-1", "", "", ""); !slices.Equal(got, []string{"codex", "--no-daemon", "--dangerously-bypass-approvals-and-sandbox",
		"resume", "thr-1", "-c", "model_reasoning_effort=xhigh"}) {
		t.Fatalf("codex resume = %v", got)
	}
	if x.CodeReview || !slices.Equal(x.LaunchArgs, []string{"--no-daemon", "--dangerously-bypass-approvals-and-sandbox"}) ||
		!slices.Equal(x.Effort(""), []string{"-c", "model_reasoning_effort=xhigh"}) || !x.Hooks.Trust || x.NewSession != "/new" {
		t.Fatalf("codex = %+v", x)
	}
	if a, _ := r.Get("agy"); a.NTMType != "antigravity" || a.ModelFlag != "" || a.InterruptKeys != nil || a.Resume("s", "", "", "") != nil || len(a.Cannot) == 0 {
		t.Fatalf("agy = %+v", a)
	}
	if p, _ := r.Get("pi"); p.GuardSupported {
		t.Fatal("pi supports guards")
	}
	for flag, want := range map[string]string{"cc": "claude", "cod": "codex", "oc": "opencode", "agy": "agy", "codex": "codex"} {
		if a, ok := r.ByFlag(flag); !ok || a.Name != want {
			t.Errorf("flag %s = %s %v", flag, a.Name, ok)
		}
	}
	if _, ok := r.ByFlag("zz"); ok {
		t.Fatal("an unknown flag selects a kind")
	}
	// The limit recognisers: each kind's own, and the generic one.
	if !c.LimitPattern().MatchString("You've hit your limit · resets 5pm") || !x.LimitPattern().MatchString("429 Too Many Requests") {
		t.Fatal("a limit was not recognised")
	}
	if g, _ := r.Get("grok"); g.LimitPattern() != GenericLimit {
		t.Fatal("grok has no generic limit")
	}
	if Lookup("weird").Name != "weird" || Lookup("").NTMType != "unknown" {
		t.Fatal("lookup of an unregistered kind")
	}
}

// The built-ins meet the rules that config kinds must meet.
func TestBuiltinsMeetTheConfigRules(t *testing.T) {
	for _, a := range NewRegistry().All() {
		if err := checkResumeArgv(a.Name, a.ResumeArgv, a.LaunchArgs); err != nil {
			t.Errorf("%s resume_argv: %v", a.Name, err)
		}
		for _, k := range a.InterruptKeys {
			if !KeyAllowed(k) {
				t.Errorf("%s interrupt key %q", a.Name, k)
			}
		}
		if a.TrustKeys != nil {
			for _, screen := range []string{
				"Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n",
				"Trust this folder?\n› 1. Trust and continue\n",
			} {
				for _, k := range a.TrustKeys(screen) {
					if !KeyAllowed(k) {
						t.Errorf("%s trust key %q", a.Name, k)
					}
				}
			}
		}
		if reserved[a.Name] {
			t.Errorf("%s is reserved", a.Name)
		}
	}
}

func TestTrustKeysAndPrograms(t *testing.T) {
	c, _ := Get("claude")
	x, _ := Get("codex")
	claudeNo := "Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n"
	claudeYes := "Is this a project you created or one you trust?\n ❯ Yes, I trust this folder\n   No, exit\n"
	if got := c.TrustKeys(claudeNo); !slices.Equal(got, []string{"down", "enter"}) {
		t.Fatalf("claude, cursor on no = %v", got)
	}
	if got := c.TrustKeys(claudeYes); !slices.Equal(got, []string{"enter"}) {
		t.Fatalf("claude, cursor on yes = %v", got)
	}
	if c.TrustKeys("Something else entirely") != nil || x.TrustKeys(claudeYes) != nil {
		t.Fatal("a screen that is not the dialog was answered")
	}
	if got := x.TrustKeys("Trust this folder?\n› 1. Trust and continue\n  2. Quit\n"); !slices.Equal(got, []string{"enter"}) {
		t.Fatalf("codex = %v", got)
	}
	if c.Program != "claude-code" || x.Program != "codex-cli" {
		t.Fatalf("programs = %s %s", c.Program, x.Program)
	}
	// A kind without its own program records its kind name.
	if p, _ := Get("pi"); p.Program != "pi" {
		t.Fatalf("pi program = %q", p.Program)
	}
}

func TestContinuationRules(t *testing.T) {
	c, _ := Get("claude")
	d := c.Continue(Event{HookEvent: "SessionStart", Source: "compact", SessionID: "s-1", Entrypoint: "cli"}, "s-1", false)
	if d.Verdict != Continue || !d.Compacted {
		t.Fatalf("claude compact = %+v", d)
	}
	if d := c.Continue(Event{HookEvent: "SessionStart", Source: "clear", SessionID: "s-2", Entrypoint: "cli"}, "s-1", false); d.Verdict != Displace {
		t.Fatalf("claude clear = %+v", d)
	}
	x, _ := Get("codex")
	if d := x.Continue(Event{HookEvent: "SessionStart", Source: "resume", SessionID: "s-1"}, "s-1", false); d.Verdict != Continue || !d.Resumed {
		t.Fatalf("codex resume = %+v", d)
	}
	if a, _ := Get("agy"); a.Continue != nil {
		t.Fatal("agy has a continuation rule")
	}
}

func TestRegisterAndConfig(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Adapter{Name: "claude"}); err == nil {
		t.Fatal("a built-in was replaced")
	}
	if err := r.Register(Adapter{Name: "mine", Flags: []string{"cc"}}); err == nil {
		t.Fatal("a flag selects two kinds")
	}
	if err := r.Register(Adapter{Name: "Bad Name"}); err == nil {
		t.Fatal("a bad name was registered")
	}
	if err := r.Register(Adapter{Name: "cc"}); err == nil {
		t.Fatal("a kind took another kind's flag as its name")
	}
	for _, a := range []Adapter{{Name: "force"}, {Name: "all"}, {Name: "help"}, {Name: "mine", Flags: []string{"wait"}}} {
		if err := r.Register(a); err == nil {
			t.Errorf("%+v took a verb flag", a)
		}
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := r.LoadConfig(path); err != nil {
		t.Fatalf("missing file = %v", err)
	}
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`[memory]
cm = "off"
[kinds.mycli]
flags = ["my"]
interrupt_keys = ["C-c"]
limit = "out of credits"
code_review = false
[kinds.mycli.hooks]
session_start = true
`)
	if err := r.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	m, ok := r.ByFlag("my")
	if !ok || m.Name != "mycli" || m.Builtin || m.CodeReview || !m.GuardSupported || m.NTMType != "mycli" || m.QuotaPool != "mycli" ||
		!m.Hooks.SessionStart || m.Continue != nil || !m.LimitPattern().MatchString("OUT OF CREDITS") || m.Resume("x", "", "", "") != nil {
		t.Fatalf("mycli = %+v", m)
	}
	if r.Names()[len(r.Names())-1] != "mycli" {
		t.Fatalf("names = %v", r.Names())
	}
	// The process registry: the config's kinds, or none when one fails.
	if d, err := withConfig(path); err != nil || len(d.Names()) != 8 {
		t.Fatalf("with config = %v %v", d.Names(), err)
	}
	write("[kinds.good]\n[kinds.zbad]\nlimit = \"(\"\n")
	if d, err := withConfig(path); err == nil || len(d.Names()) != 7 {
		t.Fatalf("half a config = %v %v", d.Names(), err)
	}
	for _, bad := range []string{
		"[kinds.mycli]\ninterupt_keys = [\"x\"]\n",
		"[kinds.claude]\nflags = [\"c2\"]\n",
		"[kinds.x]\nlimit = \"(\"\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\"]\n",
		"[kinds.x]\nguard_settings = \"../etc/passwd\"\n",
		// resume_argv runs the kind itself, by its bare name, and only an
		// agent CLI that herdr starts: an allowlist (the review of
		// b0902bb), so no shell, launcher, interpreter or symlinked name.
		"[kinds.x]\nresume_argv = [\"x\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"sh\", \"-c\", \"gemini {session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"find\", \"-exec\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"python3.12\", \"-c\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"/usr/local/bin/gemini\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"claude\", \"--resume\", \"{session}\"]\n",
		"[kinds.bash]\nresume_argv = [\"bash\", \"-c\", \"{session}\"]\n",
		// {session} only as a named flag's value or after resume; "--" is
		// not a flag, and a bare positional is refused.
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"{session}\", \"run\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--\", \"{session}\", \"y\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"chat\", \"{session}\"]\n",
		// {session} only as a resume flag's value: after another flag it
		// could name a file the CLI loads (the review of 3643fe5).
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--settings\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--mcp-config\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"-e\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--extensions\", \"resume\", \"{session}\"]\n",
		"[kinds.gemini]\nlaunch_args = [\"--yolo\"]\nresume_argv = [\"gemini\", \"--yolo\", \"-e\", \"resume\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\", \"{session}\", \"-r\", \"{session}\"]\n",
		// No "--" anywhere: nhm's flags would become prompt text.
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume\", \"{session}\", \"--\"]\n",
		"[kinds.x]\neffort_args = [\"--effort\"]\n",
		"[kinds.x]\ndefault_effort = \"high\"\n",
		"[kinds.x]\neffort_args = [\"--effort\", \"{effort}\"]\ndefault_effort = \"x y\"\n",
		"[kinds.gemini]\nresume_argv = [\"/usr/bin/env\", \"{session}\"]\n",
		"[kinds.gemini]\nresume_argv = [\"gemini\", \"--resume={session}\"]\n",
		// Pane-typed fields take no control characters; keys are herdr's.
		"[kinds.x]\nlaunch_args = [\"--a\\n--b\"]\n",
		"[kinds.x]\nlaunch_args = [\"\"]\n",
		"[kinds.x]\nnew_session = \"/new\\r\"\n",
		"[kinds.x]\nexit_command = \"/quit\\n\"\n",
		"[kinds.x]\nmodel_flag = \"model\"\n",
		"[kinds.x]\ninterrupt_keys = [\"Escape\"]\n",
		"[kinds.x]\ninterrupt_keys = [\"esc; rm\"]\n",
		"[kinds.force]\n",
		"[kinds.x]\nflags = [\"dry-run\"]\n",
	} {
		write(bad)
		if err := NewRegistry().LoadConfig(path); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
	// An agent CLI that herdr starts, with the session as a named flag's
	// value, or after the resume subcommand.
	for _, good := range []string{
		"[kinds.gemini]\nmodel_flag = \"-m\"\nresume_argv = [\"gemini\", \"--resume\", \"{session}\"]\n",
		"[kinds.gemini]\nmodel_flag = \"-m\"\nresume_argv = [\"gemini\", \"resume\", \"{session}\"]\n",
		"[kinds.gemini]\nmodel_flag = \"-m\"\nresume_argv = [\"gemini\", \"-r\", \"{session}\", \"--quiet\"]\n",
	} {
		write(good)
		r := NewRegistry()
		if err := r.LoadConfig(path); err != nil {
			t.Fatalf("refused:\n%s%v", good, err)
		}
		g, _ := r.Get("gemini")
		// nhm's flags follow the session id, before the kind's own tail.
		if got := g.Resume("s-9", "flash", "", ""); len(got) < 5 || got[2] != "s-9" || got[3] != "-m" || got[4] != "flash" {
			t.Fatalf("gemini resume = %v", got)
		}
	}
}
